package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrEmailTaken is returned by CreateUser for a duplicate email.
var ErrEmailTaken = errors.New("an account with this email already exists")

// User is one account (never carries the password hash).
type User struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	IsAdmin   bool      `json:"is_admin"`
	CreatedAt time.Time `json:"created_at"`
}

const userColumns = `id, email, name, is_admin, created_at`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &u.IsAdmin, &u.CreatedAt); err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateUser inserts an account. email must already be normalized.
func CreateUser(ctx context.Context, pool *pgxpool.Pool, email, name, passwordHash string, isAdmin bool) (*User, error) {
	u, err := scanUser(pool.QueryRow(ctx, `
		INSERT INTO users (email, name, password_hash, is_admin)
		VALUES ($1, $2, $3, $4)
		RETURNING `+userColumns, email, name, passwordHash, isAdmin))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return nil, ErrEmailTaken
	}
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	return u, nil
}

// UserCredentials returns the account and its password hash ("" when the
// account has none) for a login attempt. pgx.ErrNoRows when unknown.
func UserCredentials(ctx context.Context, pool *pgxpool.Pool, email string) (*User, string, error) {
	var u User
	var hash *string
	err := pool.QueryRow(ctx, `SELECT `+userColumns+`, password_hash
		FROM users WHERE lower(email) = lower($1)`, email).
		Scan(&u.ID, &u.Email, &u.Name, &u.IsAdmin, &u.CreatedAt, &hash)
	if err != nil {
		return nil, "", err
	}
	if hash == nil {
		return &u, "", nil
	}
	return &u, *hash, nil
}

// UserByEmail looks an account up by email (case-insensitive).
func UserByEmail(ctx context.Context, pool *pgxpool.Pool, email string) (*User, error) {
	return scanUser(pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE lower(email) = lower($1)`, email))
}

// PasswordHash returns a user's password hash ("" when unset).
func PasswordHash(ctx context.Context, pool *pgxpool.Pool, userID int64) (string, error) {
	var hash *string
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, userID).Scan(&hash); err != nil {
		return "", err
	}
	if hash == nil {
		return "", nil
	}
	return *hash, nil
}

// SetPassword replaces a user's password hash and ends their other
// sessions (keepTokenHash, when set, survives) and extension tokens.
func SetPassword(ctx context.Context, pool *pgxpool.Pool, userID int64, hash, keepTokenHash string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := setPassword(ctx, tx, userID, hash, keepTokenHash); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// setPassword stores the hash and signs out every other session and every
// extension, inside the caller's transaction.
func setPassword(ctx context.Context, tx pgx.Tx, userID int64, hash, keepTokenHash string) error {
	tag, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, userID, hash)
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND token_hash <> $2`, userID, keepTokenHash); err != nil {
		return fmt.Errorf("end sessions: %w", err)
	}
	// A new password also disconnects the browser extension(s).
	if _, err := tx.Exec(ctx, `DELETE FROM api_tokens WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("revoke extension tokens: %w", err)
	}
	return nil
}

// CreateSession stores a session token hash and records the login.
func CreateSession(ctx context.Context, pool *pgxpool.Pool, userID int64, tokenHash string, ttl time.Duration) error {
	if _, err := pool.Exec(ctx, `
		INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, now() + $3::interval)`,
		tokenHash, userID, fmt.Sprintf("%d seconds", int64(ttl.Seconds()))); err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	_, err := pool.Exec(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, userID)
	return err
}

// SessionUser resolves a live session to its user (pgx.ErrNoRows when the
// token is unknown or expired).
func SessionUser(ctx context.Context, pool *pgxpool.Pool, tokenHash string) (*User, error) {
	return scanUser(pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.name, u.is_admin, u.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`, tokenHash))
}

// DeleteSession ends one session (logout).
func DeleteSession(ctx context.Context, pool *pgxpool.Pool, tokenHash string) error {
	_, err := pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// DeleteExpiredSessions removes expired sessions; returns how many.
func DeleteExpiredSessions(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	tag, err := pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	return tag.RowsAffected(), err
}
