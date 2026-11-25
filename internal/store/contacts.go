package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxContactsPerJob bounds how many people one application keeps.
const MaxContactsPerJob = 50

// ErrTooManyContacts is returned when a job already has MaxContactsPerJob.
var ErrTooManyContacts = errors.New("too many contacts for this job")

// JobContact is a person the user is in touch with about one application.
type JobContact struct {
	ID              int64      `json:"id"`
	JobID           int64      `json:"job_id"`
	Name            string     `json:"name"`
	Role            string     `json:"role"`
	Email           string     `json:"email"`
	LinkedInURL     string     `json:"linkedin_url"`
	Notes           string     `json:"notes"`
	LastContactedAt *time.Time `json:"last_contacted_at"`
	FollowUpAt      *time.Time `json:"follow_up_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	// Set by AllContacts: the job the contact is about.
	JobTitle  string  `json:"job_title,omitempty"`
	Company   string  `json:"company,omitempty"`
	CompanyID int64   `json:"company_id,omitempty"`
	AppStatus *string `json:"application_status,omitempty"`
}

// ContactInput is a new contact, or (for a patch) the fields to change:
// a nil field stays as it is. ClearLastContacted and ClearFollowUp empty
// those dates.
type ContactInput struct {
	Name, Role, Email, LinkedInURL, Notes *string
	LastContactedAt, FollowUpAt           *time.Time
	ClearLastContacted, ClearFollowUp     bool
}

const contactColumns = `id, job_id, name, role, email, linkedin_url, notes, last_contacted_at, follow_up_at, created_at, updated_at`

func scanContact(row pgx.Row, extra ...any) (*JobContact, error) {
	var c JobContact
	dest := append([]any{&c.ID, &c.JobID, &c.Name, &c.Role, &c.Email, &c.LinkedInURL, &c.Notes,
		&c.LastContactedAt, &c.FollowUpAt, &c.CreatedAt, &c.UpdatedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	return &c, nil
}

// JobContacts lists the user's contacts for one job, oldest first. It
// returns pgx.ErrNoRows when the job isn't one of the user's.
func JobContacts(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64) ([]JobContact, error) {
	var mine bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_jobs WHERE user_id = $1 AND job_id = $2)`, userID, jobID).Scan(&mine); err != nil {
		return nil, err
	}
	if !mine {
		return nil, pgx.ErrNoRows
	}
	rows, err := pool.Query(ctx, `SELECT `+contactColumns+` FROM user_job_contacts WHERE user_id = $1 AND job_id = $2 ORDER BY created_at, id`, userID, jobID)
	if err != nil {
		return nil, fmt.Errorf("job contacts: %w", err)
	}
	defer rows.Close()
	out := []JobContact{}
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// AllContacts lists every contact of the user with the job it is about:
// follow-ups due first (soonest first), then the most recently in touch.
func AllContacts(ctx context.Context, pool *pgxpool.Pool, userID int64) ([]JobContact, error) {
	rows, err := pool.Query(ctx, `
		SELECT k.id, k.job_id, k.name, k.role, k.email, k.linkedin_url, k.notes, k.last_contacted_at, k.follow_up_at,
		       k.created_at, k.updated_at, j.title, c.name, c.id, ap.status
		FROM user_job_contacts k JOIN jobs j ON j.id = k.job_id JOIN companies c ON c.id = j.company_id
		LEFT JOIN applications ap ON ap.user_id = k.user_id AND ap.job_id = k.job_id
		WHERE k.user_id = $1
		ORDER BY k.follow_up_at ASC NULLS LAST, k.last_contacted_at DESC NULLS LAST, k.id DESC
		LIMIT 1000`, userID)
	if err != nil {
		return nil, fmt.Errorf("all contacts: %w", err)
	}
	defer rows.Close()
	out := []JobContact{}
	for rows.Next() {
		var title, company string
		var companyID int64
		var status *string
		c, err := scanContact(rows, &title, &company, &companyID, &status)
		if err != nil {
			return nil, err
		}
		c.JobTitle, c.Company, c.CompanyID, c.AppStatus = title, company, companyID, status
		out = append(out, *c)
	}
	return out, rows.Err()
}

func strOr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// AddContact saves a new contact for one of the user's jobs. It returns
// pgx.ErrNoRows when the job isn't the user's, ErrTooManyContacts when the
// job is full.
func AddContact(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64, in ContactInput) (*JobContact, error) {
	var out *JobContact
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		// Lock the user's row for this job so two adds can't pass the cap together.
		var n int
		if err := tx.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM user_job_contacts WHERE user_id = $1 AND job_id = $2)
			FROM user_jobs WHERE user_id = $1 AND job_id = $2 FOR UPDATE`, userID, jobID).Scan(&n); err != nil {
			return err
		}
		if n >= MaxContactsPerJob {
			return ErrTooManyContacts
		}
		c, err := scanContact(tx.QueryRow(ctx, `
			INSERT INTO user_job_contacts (user_id, job_id, name, role, email, linkedin_url, notes, last_contacted_at, follow_up_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING `+contactColumns,
			userID, jobID, strOr(in.Name), strOr(in.Role), strOr(in.Email), strOr(in.LinkedInURL), strOr(in.Notes),
			in.LastContactedAt, in.FollowUpAt))
		out = c
		return err
	})
	return out, err
}

// UpdateContact changes the given fields of one of the user's contacts on
// a job. It returns pgx.ErrNoRows when there is no such contact.
func UpdateContact(ctx context.Context, pool *pgxpool.Pool, userID, jobID, id int64, in ContactInput) (*JobContact, error) {
	return scanContact(pool.QueryRow(ctx, `
		UPDATE user_job_contacts SET
		  name = coalesce($4, name), role = coalesce($5, role), email = coalesce($6, email),
		  linkedin_url = coalesce($7, linkedin_url), notes = coalesce($8, notes),
		  last_contacted_at = CASE WHEN $11 THEN NULL ELSE coalesce($9, last_contacted_at) END,
		  follow_up_at = CASE WHEN $12 THEN NULL ELSE coalesce($10, follow_up_at) END,
		  updated_at = now()
		WHERE id = $3 AND user_id = $1 AND job_id = $2
		RETURNING `+contactColumns,
		userID, jobID, id, in.Name, in.Role, in.Email, in.LinkedInURL, in.Notes,
		in.LastContactedAt, in.FollowUpAt, in.ClearLastContacted, in.ClearFollowUp))
}

// DeleteContact removes one of the user's contacts on a job and reports
// whether there was one.
func DeleteContact(ctx context.Context, pool *pgxpool.Pool, userID, jobID, id int64) (bool, error) {
	tag, err := pool.Exec(ctx, `DELETE FROM user_job_contacts WHERE id = $3 AND user_id = $1 AND job_id = $2`, userID, jobID, id)
	return tag.RowsAffected() > 0, err
}
