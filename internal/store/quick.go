package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/scoring"
)

// QuickGrades returns the user's quick grades for the profile text with
// scoreKey, by job, and deletes any made for another text (or scoring
// method), so a grade that is stored is always current.
func QuickGrades(ctx context.Context, pool *pgxpool.Pool, userID int64, scoreKey string) (map[int64]int, error) {
	if _, err := pool.Exec(ctx, `DELETE FROM quick_grades WHERE user_id = $1 AND score_key <> $2`, userID, scoreKey); err != nil {
		return nil, fmt.Errorf("drop old quick grades: %w", err)
	}
	rows, err := pool.Query(ctx, `SELECT job_id, score FROM quick_grades WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("load quick grades: %w", err)
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var s int
		if err := rows.Scan(&id, &s); err != nil {
			return nil, err
		}
		out[id] = s
	}
	return out, rows.Err()
}

// SaveQuickGrades stores quick grades for the profile text with scoreKey.
func SaveQuickGrades(ctx context.Context, pool *pgxpool.Pool, userID int64, scoreKey string, grades []scoring.QuickGrade) error {
	if len(grades) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, g := range grades {
		b.Queue(`
			INSERT INTO quick_grades (user_id, job_id, score_key, score) VALUES ($1, $2, $3, $4)
			ON CONFLICT (user_id, job_id) DO UPDATE
			SET score_key = EXCLUDED.score_key, score = EXCLUDED.score, created_at = now()`,
			userID, g.JobID, scoreKey, g.Score)
	}
	if err := pool.SendBatch(ctx, b).Close(); err != nil {
		return fmt.Errorf("save quick grades: %w", err)
	}
	return nil
}

// Ungraded counts the user's open jobs waiting for a score that have no
// quick grade yet: what is left of grading their queue.
func Ungraded(ctx context.Context, pool *pgxpool.Pool, userID int64) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM user_jobs uj JOIN jobs j ON j.id = uj.job_id
		WHERE uj.user_id = $1 AND uj.stage = 'pending_score' AND j.closed_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM quick_grades q WHERE q.user_id = uj.user_id AND q.job_id = uj.job_id)`,
		userID).Scan(&n)
	return n, err
}
