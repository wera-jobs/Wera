package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AssistantThread is one Ask Wera conversation.
type AssistantThread struct {
	ID        int64     `json:"id"`
	JobID     *int64    `json:"job_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AssistantMessage is one turn of a thread.
type AssistantMessage struct {
	ID        int64     `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Sources   []string  `json:"sources"`
	CreatedAt time.Time `json:"created_at"`
}

// ErrThreadNotFound is a thread that is not the user's, or no longer exists.
var ErrThreadNotFound = errors.New("thread not found")

// CreateAssistantThread starts a thread, about a job when jobID is set.
func CreateAssistantThread(ctx context.Context, pool *pgxpool.Pool, userID int64, jobID *int64, title string) (*AssistantThread, error) {
	t := &AssistantThread{JobID: jobID, Title: title}
	err := pool.QueryRow(ctx, `
		INSERT INTO assistant_threads (user_id, job_id, title) VALUES ($1, $2, $3)
		RETURNING id, created_at, updated_at`, userID, jobID, title).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create thread: %w", err)
	}
	return t, nil
}

// GetAssistantThread returns the user's thread.
func GetAssistantThread(ctx context.Context, pool *pgxpool.Pool, userID, id int64) (*AssistantThread, error) {
	t := &AssistantThread{ID: id}
	err := pool.QueryRow(ctx, `
		SELECT job_id, title, created_at, updated_at FROM assistant_threads WHERE id = $1 AND user_id = $2`,
		id, userID).Scan(&t.JobID, &t.Title, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrThreadNotFound
	}
	return t, err
}

// AssistantThreads lists the user's threads, latest first; with jobID set,
// only those about that job.
func AssistantThreads(ctx context.Context, pool *pgxpool.Pool, userID int64, jobID *int64, limit int) ([]AssistantThread, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, job_id, title, created_at, updated_at FROM assistant_threads
		WHERE user_id = $1 AND ($2::bigint IS NULL OR job_id = $2)
		ORDER BY updated_at DESC, id DESC LIMIT $3`, userID, jobID, limit)
	if err != nil {
		return nil, fmt.Errorf("list threads: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (AssistantThread, error) {
		var t AssistantThread
		err := r.Scan(&t.ID, &t.JobID, &t.Title, &t.CreatedAt, &t.UpdatedAt)
		return t, err
	})
}

// AssistantMessages returns a thread's turns, oldest first.
func AssistantMessages(ctx context.Context, pool *pgxpool.Pool, userID, threadID int64) ([]AssistantMessage, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, role, content, sources, created_at FROM assistant_messages
		WHERE thread_id = $1 AND user_id = $2 ORDER BY id`, threadID, userID)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (AssistantMessage, error) {
		var m AssistantMessage
		err := r.Scan(&m.ID, &m.Role, &m.Content, &m.Sources, &m.CreatedAt)
		return m, err
	})
}

// AddAssistantMessage appends a turn and marks the thread as updated.
func AddAssistantMessage(ctx context.Context, pool *pgxpool.Pool, userID, threadID int64, role, content string, sources []string) (*AssistantMessage, error) {
	if sources == nil {
		sources = []string{}
	}
	m := &AssistantMessage{Role: role, Content: content, Sources: sources}
	err := pool.QueryRow(ctx, `
		WITH t AS (UPDATE assistant_threads SET updated_at = now() WHERE id = $1 AND user_id = $2 RETURNING id)
		INSERT INTO assistant_messages (thread_id, user_id, role, content, sources)
		SELECT t.id, $2, $3, $4, $5 FROM t
		RETURNING id, created_at`, threadID, userID, role, content, sources).Scan(&m.ID, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrThreadNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("add message: %w", err)
	}
	return m, nil
}

// DeleteAssistantThread deletes one of the user's threads and its turns.
func DeleteAssistantThread(ctx context.Context, pool *pgxpool.Pool, userID, id int64) error {
	tag, err := pool.Exec(ctx, `DELETE FROM assistant_threads WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrThreadNotFound
	}
	return nil
}

// DeleteAssistantThreads deletes all of the user's threads.
func DeleteAssistantThreads(ctx context.Context, pool *pgxpool.Pool, userID int64) error {
	_, err := pool.Exec(ctx, `DELETE FROM assistant_threads WHERE user_id = $1`, userID)
	return err
}

// AssistantQuestionsSince counts the user's questions since t, for the
// daily limit. Deleting threads does not reset it: the questions were
// asked.
func AssistantQuestionsSince(ctx context.Context, pool *pgxpool.Pool, userID int64, t time.Time) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM llm_usage WHERE user_id = $1 AND kind = 'assistant' AND created_at >= $2`, userID, t).Scan(&n)
	return n, err
}

// TrackedApplications lists the user's tracker for the assistant: company,
// title, status and applied date, most recently changed first.
func TrackedApplications(ctx context.Context, pool *pgxpool.Pool, userID int64, limit int) ([]TrackedApplication, error) {
	rows, err := pool.Query(ctx, `
		SELECT c.name, j.title, ap.status, ap.applied_at
		FROM applications ap
		JOIN jobs j ON j.id = ap.job_id
		JOIN companies c ON c.id = j.company_id
		WHERE ap.user_id = $1 AND ap.archived_at IS NULL
		ORDER BY ap.updated_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list tracker: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (TrackedApplication, error) {
		var a TrackedApplication
		err := r.Scan(&a.Company, &a.Title, &a.Status, &a.AppliedAt)
		return a, err
	})
}

// TrackedApplication is one row of TrackedApplications.
type TrackedApplication struct {
	Company, Title, Status string
	AppliedAt              *time.Time
}
