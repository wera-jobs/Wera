package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Contact is the one person an application is with: a recruiter, a
// referrer, a hiring manager. Link is an email address or a URL.
type Contact struct {
	Name string `json:"name"`
	Role string `json:"role"`
	Link string `json:"link"`
}

// Empty reports whether the contact says nothing.
func (c Contact) Empty() bool { return c.Name == "" && c.Role == "" && c.Link == "" }

// ApplicationDetails changes what a tracker keeps per application beyond
// its status and notes. A nil field is left as it is.
type ApplicationDetails struct {
	NextStep   *string    // "" clears it
	NextStepAt *time.Time // the zero time clears it
	Contact    *Contact   // an empty contact clears it
	Archived   *bool      // archived applications leave the board, history kept
	ResumeKind *string    // the resume sent (tailored, base or upload); "" clears it
	LetterSent *bool      // whether a cover letter went with it
}

// SetApplicationDetails applies d to the user's application for jobID. A
// job the user has no application for is pgx.ErrNoRows.
func SetApplicationDetails(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64, d ApplicationDetails) error {
	var nextAt *time.Time
	if d.NextStepAt != nil && !d.NextStepAt.IsZero() {
		nextAt = d.NextStepAt
	}
	var contact *Contact
	if d.Contact != nil && !d.Contact.Empty() {
		contact = d.Contact
	}
	tag, err := pool.Exec(ctx, `
		UPDATE applications SET
		  next_step    = CASE WHEN $3::bool THEN nullif($4::text, '') ELSE next_step END,
		  next_step_at = CASE WHEN $5::bool THEN $6::timestamptz ELSE next_step_at END,
		  contact      = CASE WHEN $7::bool THEN $8::jsonb ELSE contact END,
		  archived_at  = CASE WHEN $9::bool IS NULL THEN archived_at
		                      WHEN $9::bool THEN coalesce(archived_at, now()) END,
		  resume_kind  = CASE WHEN $10::bool THEN nullif($11::text, '') ELSE resume_kind END,
		  letter_sent  = coalesce($12::bool, letter_sent),
		  updated_at   = now()
		WHERE user_id = $1 AND job_id = $2`,
		userID, jobID,
		d.NextStep != nil, deref(d.NextStep),
		d.NextStepAt != nil, nextAt,
		d.Contact != nil, contact,
		d.Archived,
		d.ResumeKind != nil, deref(d.ResumeKind), d.LetterSent)
	if err != nil {
		return fmt.Errorf("set application details for job %d: %w", jobID, err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// EventKinds mirrors the application_events.kind constraint.
var EventKinds = map[string]bool{
	"phone_screen": true, "interview": true, "onsite": true,
	"assessment": true, "offer_call": true, "other": true,
}

// Event is a dated step in an application: an interview, a screen, a
// take-home due.
type Event struct {
	ID      int64     `json:"id"`
	JobID   int64     `json:"job_id"`
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	With    string    `json:"with"`
	Notes   string    `json:"notes"`
	Title   string    `json:"title,omitempty"`   // the job's, in lists across jobs
	Company string    `json:"company,omitempty"` // likewise
}

// AddEvent records an event on the user's application for jobID; no
// application is pgx.ErrNoRows.
func AddEvent(ctx context.Context, pool *pgxpool.Pool, userID int64, e Event) (Event, error) {
	err := pool.QueryRow(ctx, `
		INSERT INTO application_events (user_id, job_id, at, kind, with_whom, notes)
		SELECT $1, $2, $3, $4, $5, $6
		WHERE EXISTS (SELECT 1 FROM applications WHERE user_id = $1 AND job_id = $2)
		RETURNING id`, userID, e.JobID, e.At, e.Kind, e.With, e.Notes).Scan(&e.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return e, err
		}
		return e, fmt.Errorf("add event for job %d: %w", e.JobID, err)
	}
	return e, nil
}

// DeleteEvent removes one of the user's events; not theirs or not there is
// pgx.ErrNoRows.
func DeleteEvent(ctx context.Context, pool *pgxpool.Pool, userID, jobID, eventID int64) error {
	tag, err := pool.Exec(ctx, `DELETE FROM application_events WHERE id = $1 AND user_id = $2 AND job_id = $3`, eventID, userID, jobID)
	if err != nil {
		return fmt.Errorf("delete event %d: %w", eventID, err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// JobEvents lists the user's events for one job, soonest first.
func JobEvents(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64) ([]Event, error) {
	return queryEvents(ctx, pool, `
		SELECT e.id, e.job_id, e.at, e.kind, e.with_whom, e.notes, '', ''
		FROM application_events e WHERE e.user_id = $1 AND e.job_id = $2
		ORDER BY e.at`, userID, jobID)
}

// UpcomingEvents lists the user's events from one time to another across
// all their applications, archived ones left out, soonest first.
func UpcomingEvents(ctx context.Context, pool *pgxpool.Pool, userID int64, from, to time.Time) ([]Event, error) {
	return queryEvents(ctx, pool, `
		SELECT e.id, e.job_id, e.at, e.kind, e.with_whom, e.notes, j.title, c.name
		FROM application_events e
		JOIN applications ap ON ap.user_id = e.user_id AND ap.job_id = e.job_id
		JOIN jobs j ON j.id = e.job_id JOIN companies c ON c.id = j.company_id
		WHERE e.user_id = $1 AND e.at >= $2 AND e.at < $3 AND ap.archived_at IS NULL
		ORDER BY e.at`, userID, from, to)
}

func queryEvents(ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) ([]Event, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.JobID, &e.At, &e.Kind, &e.With, &e.Notes, &e.Title, &e.Company); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Reminder is something due on an application, for notifications: an
// event or the next step the user set.
type Reminder struct {
	UserID  int64     `json:"user_id"`
	JobID   int64     `json:"job_id"`
	Title   string    `json:"title"`
	Company string    `json:"company"`
	What    string    `json:"what"` // the event kind, or the next step's text
	Event   bool      `json:"event"`
	At      time.Time `json:"at"`
}

// DueReminders lists, across all users, the events and next steps due in
// [from, to) on applications that are not archived, soonest first.
func DueReminders(ctx context.Context, pool *pgxpool.Pool, from, to time.Time) ([]Reminder, error) {
	rows, err := pool.Query(ctx, `
		SELECT * FROM (
			SELECT e.user_id, e.job_id, j.title, c.name, e.kind, true, e.at
			FROM application_events e
			JOIN applications ap ON ap.user_id = e.user_id AND ap.job_id = e.job_id
			JOIN jobs j ON j.id = e.job_id JOIN companies c ON c.id = j.company_id
			WHERE e.at >= $1 AND e.at < $2 AND ap.archived_at IS NULL
			UNION ALL
			SELECT ap.user_id, ap.job_id, j.title, c.name, coalesce(ap.next_step, 'Next step'), false, ap.next_step_at
			FROM applications ap JOIN jobs j ON j.id = ap.job_id JOIN companies c ON c.id = j.company_id
			WHERE ap.next_step_at >= $1 AND ap.next_step_at < $2 AND ap.archived_at IS NULL
		) due ORDER BY 7`, from, to)
	if err != nil {
		return nil, fmt.Errorf("due reminders: %w", err)
	}
	defer rows.Close()
	var out []Reminder
	for rows.Next() {
		var r Reminder
		if err := rows.Scan(&r.UserID, &r.JobID, &r.Title, &r.Company, &r.What, &r.Event, &r.At); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
