package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InterviewPrep is a user's stored prep for one job. Prep is the JSON
// written by profile.ParseInterviewPrep, kept as is.
type InterviewPrep struct {
	Prep      json.RawMessage `json:"prep"`
	Model     string          `json:"model"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// GetInterviewPrep returns the user's prep for a job, or nil when there is none.
func GetInterviewPrep(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64) (*InterviewPrep, error) {
	var p InterviewPrep
	err := pool.QueryRow(ctx, `
		SELECT prep, model, created_at, updated_at FROM interview_preps
		WHERE user_id = $1 AND job_id = $2`, userID, jobID).
		Scan(&p.Prep, &p.Model, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// SaveInterviewPrep stores (or replaces) the user's prep for a job.
func SaveInterviewPrep(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64, prep any, model string) (*InterviewPrep, error) {
	b, err := json.Marshal(prep)
	if err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO interview_preps (user_id, job_id, prep, model) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, job_id) DO UPDATE
		SET prep = EXCLUDED.prep, model = EXCLUDED.model, updated_at = now()`, userID, jobID, b, model); err != nil {
		return nil, err
	}
	return GetInterviewPrep(ctx, pool, userID, jobID)
}
