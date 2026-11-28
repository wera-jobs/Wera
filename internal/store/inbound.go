package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/inbound"
)

// InboundAddress is a user's forwarding address for recruiter replies.
type InboundAddress struct {
	Token       string     `json:"-"`
	CreatedAt   time.Time  `json:"created_at"`
	LastEmailAt *time.Time `json:"last_email_at"`
	ConfirmCode *string    `json:"confirm_code"`
	ConfirmURL  *string    `json:"confirm_url"`
	ConfirmAt   *time.Time `json:"confirm_at"`
}

// GetInboundAddress returns the user's address, or nil when it is off.
func GetInboundAddress(ctx context.Context, pool *pgxpool.Pool, userID int64) (*InboundAddress, error) {
	a := &InboundAddress{}
	err := pool.QueryRow(ctx, `
		SELECT token, created_at, last_email_at, confirm_code, confirm_url, confirm_at
		FROM inbound_addresses WHERE user_id = $1`, userID).
		Scan(&a.Token, &a.CreatedAt, &a.LastEmailAt, &a.ConfirmCode, &a.ConfirmURL, &a.ConfirmAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

// tokenAlphabet is lower case and digits without look-alikes, for an
// address people may type.
const tokenAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

func newInboundToken() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = tokenAlphabet[int(b[i])%len(tokenAlphabet)]
	}
	return string(b)
}

// NewInboundAddress turns the user's address on, or gives them a new one:
// mail to the old address is refused from then on.
func NewInboundAddress(ctx context.Context, pool *pgxpool.Pool, userID int64) (*InboundAddress, error) {
	if _, err := pool.Exec(ctx, `
		INSERT INTO inbound_addresses (user_id, token) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET token = EXCLUDED.token, created_at = now(),
		  last_email_at = NULL, confirm_code = NULL, confirm_url = NULL, confirm_at = NULL`,
		userID, newInboundToken()); err != nil {
		return nil, fmt.Errorf("save address: %w", err)
	}
	return GetInboundAddress(ctx, pool, userID)
}

// DeleteInboundAddress turns the user's address off.
func DeleteInboundAddress(ctx context.Context, pool *pgxpool.Pool, userID int64) error {
	_, err := pool.Exec(ctx, `DELETE FROM inbound_addresses WHERE user_id = $1`, userID)
	return err
}

// InboundUser finds whose address a token is, and notes that mail came.
// It returns 0 for a token no one has.
func InboundUser(ctx context.Context, pool *pgxpool.Pool, token string) (int64, error) {
	var id int64
	err := pool.QueryRow(ctx, `
		UPDATE inbound_addresses SET last_email_at = now() WHERE token = $1 RETURNING user_id`, token).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// SaveForwardConfirmation keeps Gmail's forwarding confirmation for the
// user to finish setup with.
func SaveForwardConfirmation(ctx context.Context, pool *pgxpool.Pool, userID int64, code, url string) error {
	_, err := pool.Exec(ctx, `
		UPDATE inbound_addresses SET confirm_code = NULLIF($2, ''), confirm_url = NULLIF($3, ''), confirm_at = now()
		WHERE user_id = $1`, userID, code, url)
	return err
}

// InboundApps lists the user's open applications for matching replies.
func InboundApps(ctx context.Context, pool *pgxpool.Pool, userID int64) ([]inbound.App, error) {
	rows, err := pool.Query(ctx, `
		SELECT ap.job_id, c.name, c.token, j.title, ap.status
		FROM applications ap
		JOIN jobs j ON j.id = ap.job_id
		JOIN companies c ON c.id = j.company_id
		WHERE ap.user_id = $1 AND ap.archived_at IS NULL
		  AND ap.status IN ('saved', 'applied', 'interviewing', 'offer')
		ORDER BY ap.updated_at DESC LIMIT 500`, userID)
	if err != nil {
		return nil, fmt.Errorf("list applications: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (inbound.App, error) {
		var a inbound.App
		err := r.Scan(&a.JobID, &a.Company, &a.Token, &a.Title, &a.Status)
		return a, err
	})
}

// EmailSuggestion is a tracker change a forwarded reply suggested.
type EmailSuggestion struct {
	ID         int64     `json:"id"`
	JobID      int64     `json:"job_id"`
	Company    string    `json:"company"`
	Title      string    `json:"title"`
	Current    string    `json:"current_status"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	FromDomain string    `json:"from_domain"`
	Subject    string    `json:"subject"`
	ReceivedAt time.Time `json:"received_at"`
}

// maxPendingSuggestions caps what one user's mail can pile up.
const maxPendingSuggestions = 100

// SaveEmailSuggestion records a suggestion, or refreshes the pending one
// for the same job and status. It saves nothing past the cap.
func SaveEmailSuggestion(ctx context.Context, pool *pgxpool.Pool, userID int64, s inbound.Suggestion, fromDomain, subject string, at time.Time) error {
	if r := []rune(subject); len(r) > 140 {
		subject = string(r[:139]) + "…"
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO email_suggestions (user_id, job_id, kind, status, from_domain, subject, received_at)
		SELECT $1, $2, $3, $4, $5, $6, $7
		WHERE (SELECT count(*) FROM email_suggestions WHERE user_id = $1 AND state = 'pending') < $8
		ON CONFLICT (user_id, job_id, status) WHERE state = 'pending'
		DO UPDATE SET kind = EXCLUDED.kind, from_domain = EXCLUDED.from_domain, subject = EXCLUDED.subject,
		  received_at = GREATEST(email_suggestions.received_at, EXCLUDED.received_at)`,
		userID, s.App.JobID, s.Kind, s.Status, fromDomain, subject, at, maxPendingSuggestions)
	return err
}

// PendingEmailSuggestions lists the suggestions waiting for the user,
// newest first, leaving out those the tracker has caught up with.
func PendingEmailSuggestions(ctx context.Context, pool *pgxpool.Pool, userID int64) ([]EmailSuggestion, error) {
	rows, err := pool.Query(ctx, `
		SELECT s.id, s.job_id, c.name, j.title, coalesce(ap.status, ''), s.kind, s.status, s.from_domain, s.subject, s.received_at
		FROM email_suggestions s
		JOIN jobs j ON j.id = s.job_id
		JOIN companies c ON c.id = j.company_id
		LEFT JOIN applications ap ON ap.job_id = s.job_id AND ap.user_id = s.user_id
		WHERE s.user_id = $1 AND s.state = 'pending' AND ap.status IS DISTINCT FROM s.status
		ORDER BY s.received_at DESC LIMIT $2`, userID, maxPendingSuggestions)
	if err != nil {
		return nil, fmt.Errorf("list suggestions: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (EmailSuggestion, error) {
		var s EmailSuggestion
		err := r.Scan(&s.ID, &s.JobID, &s.Company, &s.Title, &s.Current, &s.Kind, &s.Status, &s.FromDomain, &s.Subject, &s.ReceivedAt)
		return s, err
	})
}

// ErrSuggestionNotFound is a suggestion that is not the user's or not pending.
var ErrSuggestionNotFound = errors.New("suggestion not found")

// DecideEmailSuggestion marks a pending suggestion accepted or dismissed and
// returns its job and status.
func DecideEmailSuggestion(ctx context.Context, pool *pgxpool.Pool, userID, id int64, accept bool) (jobID int64, status string, err error) {
	state := "dismissed"
	if accept {
		state = "accepted"
	}
	err = pool.QueryRow(ctx, `
		UPDATE email_suggestions SET state = $3, decided_at = now()
		WHERE id = $1 AND user_id = $2 AND state = 'pending' RETURNING job_id, status`, id, userID, state).Scan(&jobID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrSuggestionNotFound
	}
	return jobID, status, err
}
