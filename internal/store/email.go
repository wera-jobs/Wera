package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrTokenInvalid means a link token is unknown, used or expired.
var ErrTokenInvalid = errors.New("this link is invalid or has expired; ask for a new one")

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// CreateEmailToken issues a single-use token for purpose ("verify" or
// "reset") sent to email, valid for ttl. Earlier unused tokens for the
// same purpose stop working. It returns the raw token for the link.
func CreateEmailToken(ctx context.Context, pool *pgxpool.Pool, userID int64, purpose, email string, ttl time.Duration) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	raw := base64.RawURLEncoding.EncodeToString(b)
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE email_tokens SET used_at = now() WHERE user_id = $1 AND purpose = $2 AND used_at IS NULL`, userID, purpose); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO email_tokens (user_id, purpose, token_hash, email, expires_at) VALUES ($1, $2, $3, $4, now() + $5::interval)`,
		userID, purpose, hashToken(raw), email, ttl.String()); err != nil {
		return "", err
	}
	return raw, tx.Commit(ctx)
}

// UseEmailToken consumes a token: it returns the user and the address it
// was sent to, or ErrTokenInvalid. A token works once.
func UseEmailToken(ctx context.Context, pool *pgxpool.Pool, purpose, raw string) (int64, string, error) {
	var userID int64
	var email string
	err := pool.QueryRow(ctx, `
		UPDATE email_tokens SET used_at = now()
		WHERE token_hash = $1 AND purpose = $2 AND used_at IS NULL AND expires_at > now()
		RETURNING user_id, email`, hashToken(raw), purpose).Scan(&userID, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrTokenInvalid
	}
	return userID, email, err
}

// MarkEmailVerified records that the user confirmed email, if it is still
// their address (a link for an old address does nothing).
func MarkEmailVerified(ctx context.Context, pool *pgxpool.Pool, userID int64, email string) (bool, error) {
	tag, err := pool.Exec(ctx, `UPDATE users SET email_verified_at = now() WHERE id = $1 AND lower(email) = lower($2)`, userID, email)
	return tag.RowsAffected() == 1, err
}

// EmailVerified reports whether the user confirmed their current address.
func EmailVerified(ctx context.Context, pool *pgxpool.Pool, userID int64) (bool, error) {
	var at *time.Time
	err := pool.QueryRow(ctx, `SELECT email_verified_at FROM users WHERE id = $1`, userID).Scan(&at)
	return at != nil, err
}

// RecentEmailCount is how many emails with tag went to the user in the
// last window (to rate-limit resends).
func RecentEmailCount(ctx context.Context, pool *pgxpool.Pool, userID int64, tag string, window time.Duration) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox WHERE user_id = $1 AND tag = $2 AND created_at > now() - $3::interval`,
		userID, tag, window.String()).Scan(&n)
	return n, err
}

// EmailSent reports whether an email with tag has gone to the user, or
// is on its way.
func EmailSent(ctx context.Context, pool *pgxpool.Pool, userID int64, tag string) (bool, error) {
	var sent bool
	err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM email_outbox WHERE user_id = $1 AND tag = $2 AND status <> 'failed')`,
		userID, tag).Scan(&sent)
	return sent, err
}

// OutboxEmail is a queued email.
type OutboxEmail struct {
	ID       int64             `json:"id"`
	UserID   *int64            `json:"user_id"`
	To       string            `json:"to"`
	Tag      string            `json:"tag"`
	Subject  string            `json:"subject"`
	HTML     string            `json:"-"`
	Text     string            `json:"-"`
	Headers  map[string]string `json:"-"`
	Status   string            `json:"status"`
	Attempts int               `json:"attempts"`
	Error    *string           `json:"last_error"`
	Created  time.Time         `json:"created_at"`
	SentAt   *time.Time        `json:"sent_at"`
}

// QueueEmail adds an email to the outbox.
func QueueEmail(ctx context.Context, pool *pgxpool.Pool, userID *int64, to, tag, subject, html, text string, headers map[string]string) (int64, error) {
	if headers == nil {
		headers = map[string]string{}
	}
	var id int64
	err := pool.QueryRow(ctx, `
		INSERT INTO email_outbox (user_id, to_addr, tag, subject, html, text, headers) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		userID, to, tag, subject, html, text, headers).Scan(&id)
	return id, err
}

// ClaimEmails takes up to limit due emails for sending (other senders
// skip them). Emails stuck in "sending" for 10 minutes are retried.
func ClaimEmails(ctx context.Context, pool *pgxpool.Pool, limit int) ([]OutboxEmail, error) {
	rows, err := pool.Query(ctx, `
		UPDATE email_outbox SET status = 'sending', attempts = attempts + 1, next_attempt_at = now() + interval '10 minutes'
		WHERE id IN (
		  SELECT id FROM email_outbox
		  WHERE (status = 'queued' OR status = 'sending') AND next_attempt_at <= now()
		  ORDER BY next_attempt_at LIMIT $1 FOR UPDATE SKIP LOCKED)
		RETURNING id, user_id, to_addr, tag, subject, html, text, headers, status, attempts, last_error, created_at, sent_at`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (OutboxEmail, error) {
		var e OutboxEmail
		err := r.Scan(&e.ID, &e.UserID, &e.To, &e.Tag, &e.Subject, &e.HTML, &e.Text, &e.Headers, &e.Status, &e.Attempts, &e.Error, &e.Created, &e.SentAt)
		return e, err
	})
}

// MarkEmailSent records a delivery.
func MarkEmailSent(ctx context.Context, pool *pgxpool.Pool, id int64, providerID string) error {
	_, err := pool.Exec(ctx, `UPDATE email_outbox SET status = 'sent', sent_at = now(), provider_id = $2, last_error = NULL WHERE id = $1`, id, providerID)
	return err
}

// MaxEmailAttempts is how often a failing email is tried.
const MaxEmailAttempts = 5

// MarkEmailFailed records a failed attempt: retried with backoff (1, 4,
// 16, 64 minutes) until MaxEmailAttempts, then given up.
func MarkEmailFailed(ctx context.Context, pool *pgxpool.Pool, id int64, attempts int, cause string) error {
	if len(cause) > 500 {
		cause = cause[:500]
	}
	if attempts >= MaxEmailAttempts {
		_, err := pool.Exec(ctx, `UPDATE email_outbox SET status = 'failed', last_error = $2 WHERE id = $1`, id, cause)
		return err
	}
	backoff := time.Duration(1<<(2*(attempts-1))) * time.Minute
	_, err := pool.Exec(ctx, `UPDATE email_outbox SET status = 'queued', last_error = $2, next_attempt_at = now() + $3::interval WHERE id = $1`,
		id, cause, backoff.String())
	return err
}

// RecentEmails lists the newest outbox entries (for admins).
func RecentEmails(ctx context.Context, pool *pgxpool.Pool, limit int) ([]OutboxEmail, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, user_id, to_addr, tag, subject, '', '', '{}'::jsonb, status, attempts, last_error, created_at, sent_at
		FROM email_outbox ORDER BY id DESC LIMIT $1`, normalizeLimit(limit, 50, 200))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (OutboxEmail, error) {
		var e OutboxEmail
		err := r.Scan(&e.ID, &e.UserID, &e.To, &e.Tag, &e.Subject, &e.HTML, &e.Text, &e.Headers, &e.Status, &e.Attempts, &e.Error, &e.Created, &e.SentAt)
		return e, err
	})
}
