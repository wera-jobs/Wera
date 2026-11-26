package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TodayJobs returns the review queue for GET /api/today: scored, open jobs
// that have no application status yet (or are only saved), best fit first
// less each posting's age penalty (see agePenalty), so fresh strong
// matches lead. Jobs stay until they are applied to, dismissed, or close,
// regardless of when they were first seen. While fewer than limit are
// scored (a new user's first minutes), the rest of the list is filled with
// the best unscored matches by relevance estimate; pending is how many
// matches still wait for a score. followed keeps only companies the user
// follows (pending still counts every match).
func TodayJobs(ctx context.Context, pool *pgxpool.Pool, userID int64, limit int, followed bool) (jobs []JobView, pending int, err error) {
	limit = normalizeLimit(limit, 100, 200)
	only := ""
	if followed {
		only = "\n\t  AND " + followedOnly
	}
	scored, err := queryJobViews(ctx, pool, jobViewSelect+only+`
	  AND j.closed_at IS NULL
	  AND uj.stage = 'scored'
	  AND (ap.status IS NULL OR ap.status = 'saved') AND ap.archived_at IS NULL
	  AND `+oneSibling+`
	ORDER BY a.fit_score - `+agePenalty+` DESC NULLS LAST, j.first_seen_at DESC
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
		estimated, err := queryJobViews(ctx, pool, jobViewSelect+only+`
		  AND j.closed_at IS NULL
		  AND uj.stage = 'pending_score'
		  AND (ap.status IS NULL OR ap.status = 'saved') AND ap.archived_at IS NULL
		  AND `+oneSibling+`
		ORDER BY `+queueOrder+`, j.first_seen_at DESC
		LIMIT $2`, userID, room)
		if err != nil {
			return nil, 0, err
		}
		scored = append(scored, estimated...)
	}
	if err := fillAlsoIn(ctx, pool, userID, scored); err != nil {
		return nil, 0, err
	}
	return scored, pending, nil
}

// NewMatches returns the user's open, unapplied jobs first scored for them
// since a time, best first. Scoring can come days after a job is first
// seen, and a profile edit rescores jobs the user already knows, so the
// job's first score for this user is what makes it new.
func NewMatches(ctx context.Context, pool *pgxpool.Pool, userID int64, since time.Time, limit int) ([]JobView, error) {
	views, err := queryJobViews(ctx, pool, jobViewSelect+`
	  AND j.closed_at IS NULL
	  AND uj.stage = 'scored'
	  AND (ap.status IS NULL OR ap.status = 'saved') AND ap.archived_at IS NULL
	  AND `+oneSibling+`
	  AND a.created_at >= $2
	  AND NOT EXISTS (SELECT 1 FROM analyses old
	                  WHERE old.user_id = uj.user_id AND old.job_id = uj.job_id AND old.kind = 'score' AND old.created_at < $2)
	ORDER BY a.fit_score DESC NULLS LAST, j.first_seen_at DESC
	LIMIT $3`, userID, since, normalizeLimit(limit, 100, 200))
	if err != nil {
		return nil, err
	}
	return views, fillAlsoIn(ctx, pool, userID, views)
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
		v.finish()
		out = append(out, v)
	}
	return out, rows.Err()
}

// ApplicationInput is the PUT /api/jobs/{id}/application body.
type ApplicationInput struct {
	Status string
	Notes  *string // nil keeps the notes already saved
	// Reason is why the user passed on the job (status not_interested
	// only): one of ValidDismissReasons, or nil for none. Any other status
	// clears it.
	Reason *string
}

// ValidDismissReasons mirrors the applications.dismiss_reason constraint.
var ValidDismissReasons = map[string]bool{
	"too_senior": true, "too_junior": true, "location": true, "field": true,
	"company": true, "pay": true, "other": true,
}

// LearnedDismissReasons are the reasons that say something about the kind
// of job (its field, level, place or employer), which the ranker learns
// from. Pay and other say nothing a job's text shows, and no reason says
// nothing at all.
var LearnedDismissReasons = []string{"too_senior", "too_junior", "location", "field", "company"}

// ValidApplicationStatuses mirrors the applications.status constraint.
var ValidApplicationStatuses = map[string]bool{
	"saved": true, "applied": true, "interviewing": true,
	"offer": true, "rejected": true, "not_interested": true,
}

// ClearApplication takes a job off the user's tracker: no status and no
// notes, as before they first set one. It is how an accidental status is
// undone.
func ClearApplication(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64) error {
	_, err := pool.Exec(ctx, `DELETE FROM applications WHERE user_id = $1 AND job_id = $2`, userID, jobID)
	if err != nil {
		return fmt.Errorf("clear application for job %d: %w", jobID, err)
	}
	return nil
}

// PutApplication upserts the user's application row; applied_at is set
// the first time the status becomes 'applied' and never overwritten. A
// job the user has no match row for is pgx.ErrNoRows.
func PutApplication(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64, in ApplicationInput) error {
	tag, err := pool.Exec(ctx, `
		INSERT INTO applications (user_id, job_id, status, notes, applied_at, updated_at, dismiss_reason)
		SELECT $4, $1, $2, coalesce($3, ''), CASE WHEN $2 = 'applied' THEN now() END, now(),
		       CASE WHEN $2 = 'not_interested' THEN $5::text END
		WHERE EXISTS (SELECT 1 FROM user_jobs WHERE user_id = $4 AND job_id = $1)
		ON CONFLICT (user_id, job_id) DO UPDATE
		SET status     = EXCLUDED.status,
		    notes      = coalesce($3, applications.notes),
		    applied_at = CASE WHEN applications.status <> 'applied'
		                       AND EXCLUDED.status = 'applied'
		                      THEN now() ELSE applications.applied_at END,
		    updated_at = now(),
		    dismiss_reason = EXCLUDED.dismiss_reason`,
		jobID, in.Status, in.Notes, userID, in.Reason)
	if err != nil {
		return fmt.Errorf("put application for job %d: %w", jobID, err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// Dismissal is a job the user passed on with a reason, and what the AI read
// of its level.
type Dismissal struct {
	Reason        string
	Seniority     *string // the score's level: entry, mid, senior...; nil when unscored
	YearsRequired *int
}

// RecentDismissals returns the user's Not interested jobs with a reason,
// set since the given time.
func RecentDismissals(ctx context.Context, pool *pgxpool.Pool, userID int64, since time.Time) ([]Dismissal, error) {
	rows, err := pool.Query(ctx, `
		SELECT ap.dismiss_reason, a.seniority, a.years_required
		FROM applications ap
		LEFT JOIN user_jobs uj ON uj.user_id = ap.user_id AND uj.job_id = ap.job_id
		LEFT JOIN analyses a ON a.id = uj.analysis_id
		WHERE ap.user_id = $1 AND ap.status = 'not_interested' AND ap.dismiss_reason IS NOT NULL AND ap.updated_at >= $2
		ORDER BY ap.updated_at DESC`, userID, since)
	if err != nil {
		return nil, fmt.Errorf("recent dismissals: %w", err)
	}
	defer rows.Close()
	var out []Dismissal
	for rows.Next() {
		var d Dismissal
		if err := rows.Scan(&d.Reason, &d.Seniority, &d.YearsRequired); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
