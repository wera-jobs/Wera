package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TodayJobs returns the review queue for GET /api/today: scored, open jobs
// that have no application status yet (or are only saved), ordered by fit
// score descending. Jobs stay until they are applied to, dismissed, or close,
// regardless of when they were first seen. While fewer than limit are
// scored (a new user's first minutes), the rest of the list is filled with
// the best unscored matches by relevance estimate; pending is how many
// matches still wait for a score.
func TodayJobs(ctx context.Context, pool *pgxpool.Pool, userID int64, limit int) (jobs []JobView, pending int, err error) {
	limit = normalizeLimit(limit, 100, 200)
	scored, err := queryJobViews(ctx, pool, jobViewSelect+`
	  AND j.closed_at IS NULL
	  AND uj.stage = 'scored'
	  AND (ap.status IS NULL OR ap.status = 'saved')
	ORDER BY a.fit_score DESC NULLS LAST, j.first_seen_at DESC
	LIMIT $2`, userID, limit)
	if err != nil {
		return nil, 0, err
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM user_jobs uj JOIN jobs j ON j.id = uj.job_id
		WHERE uj.user_id = $1 AND uj.stage = 'pending_score' AND j.closed_at IS NULL`,
		userID).Scan(&pending); err != nil {
		return nil, 0, err
	}
	if room := min(limit, 30) - len(scored); room > 0 && pending > 0 {
		estimated, err := queryJobViews(ctx, pool, jobViewSelect+`
		  AND j.closed_at IS NULL
		  AND uj.stage = 'pending_score'
		  AND (ap.status IS NULL OR ap.status = 'saved')
		ORDER BY uj.estimated_score DESC NULLS LAST, j.first_seen_at DESC
		LIMIT $2`, userID, room)
		if err != nil {
			return nil, 0, err
		}
		scored = append(scored, estimated...)
	}
	return scored, pending, nil
}

// NewMatches returns the user's open, unapplied jobs first scored for them
// since a time, best first. Scoring can come days after a job is first
// seen, and a profile edit rescores jobs the user already knows, so the
// job's first score for this user is what makes it new.
func NewMatches(ctx context.Context, pool *pgxpool.Pool, userID int64, since time.Time, limit int) ([]JobView, error) {
	return queryJobViews(ctx, pool, jobViewSelect+`
	  AND j.closed_at IS NULL
	  AND uj.stage = 'scored'
	  AND (ap.status IS NULL OR ap.status = 'saved')
	  AND a.created_at >= $2
	  AND NOT EXISTS (SELECT 1 FROM analyses old
	                  WHERE old.user_id = uj.user_id AND old.job_id = uj.job_id AND old.kind = 'score' AND old.created_at < $2)
	ORDER BY a.fit_score DESC NULLS LAST, j.first_seen_at DESC
	LIMIT $3`, userID, since, normalizeLimit(limit, 100, 200))
}

func queryJobViews(ctx context.Context, pool *pgxpool.Pool, sqlText string, args ...any) ([]JobView, error) {
	rows, err := pool.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobView
	for rows.Next() {
		var v JobView
		if err := rows.Scan(jobViewScan(&v)...); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ApplicationInput is the PUT /api/jobs/{id}/application body.
type ApplicationInput struct {
	Status string
	Notes  *string // nil keeps the notes already saved
}

// ValidApplicationStatuses mirrors the applications.status constraint.
var ValidApplicationStatuses = map[string]bool{
	"saved": true, "applied": true, "interviewing": true,
	"offer": true, "rejected": true, "not_interested": true,
}

// PutApplication upserts the user's application row; applied_at is set
// the first time the status becomes 'applied' and never overwritten. A
// job the user has no match row for is pgx.ErrNoRows.
func PutApplication(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64, in ApplicationInput) error {
	tag, err := pool.Exec(ctx, `
		INSERT INTO applications (user_id, job_id, status, notes, applied_at, updated_at)
		SELECT $4, $1, $2, coalesce($3, ''), CASE WHEN $2 = 'applied' THEN now() END, now()
		WHERE EXISTS (SELECT 1 FROM user_jobs WHERE user_id = $4 AND job_id = $1)
		ON CONFLICT (user_id, job_id) DO UPDATE
		SET status     = EXCLUDED.status,
		    notes      = coalesce($3, applications.notes),
		    applied_at = CASE WHEN applications.status <> 'applied'
		                       AND EXCLUDED.status = 'applied'
		                      THEN now() ELSE applications.applied_at END,
		    updated_at = now()`,
		jobID, in.Status, in.Notes, userID)
	if err != nil {
		return fmt.Errorf("put application for job %d: %w", jobID, err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ExcludedJobs returns the user's audit view: excluded jobs with reason
// and evidence, optionally narrowed to one reason (GET /api/excluded).
func ExcludedJobs(ctx context.Context, pool *pgxpool.Pool, userID int64, reason string, limit int) ([]JobView, error) {
	where := "uj.stage = 'excluded' AND j.closed_at IS NULL"
	args := []any{userID}
	if reason != "" {
		args = append(args, reason)
		where += fmt.Sprintf(" AND uj.exclude_reason = $%d", len(args))
	}
	args = append(args, normalizeLimit(limit, 50, 200))
	sqlText := jobViewSelect + "\nAND " + where +
		fmt.Sprintf("\nORDER BY j.first_seen_at DESC LIMIT $%d", len(args))
	rows, err := pool.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobView
	for rows.Next() {
		var v JobView
		if err := rows.Scan(jobViewScan(&v)...); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
