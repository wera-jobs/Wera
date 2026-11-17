package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/relevance"
)

// RankDocs lists the user's open pending jobs to rank. Only jobs without
// current shared facts carry their description (the TF-IDF fallback reads
// it); jobs with facts are ranked from the facts, so hasFacts marks them and
// their Body is empty, which keeps a long queue from reading every posting.
func RankDocs(ctx context.Context, pool *pgxpool.Pool, userID int64) (docs []relevance.Doc, hasFacts map[int64]bool, err error) {
	rows, err := pool.Query(ctx, `
		SELECT j.id, j.title, f.job_id IS NOT NULL,
		       CASE WHEN f.job_id IS NULL THEN left(COALESCE(j.description, ''), 4000) ELSE '' END
		FROM user_jobs uj JOIN jobs j ON j.id = uj.job_id
		LEFT JOIN job_facts f ON f.job_id = j.id AND f.content_hash = j.content_hash
		WHERE uj.user_id = $1 AND uj.stage = 'pending_score' AND j.closed_at IS NULL`, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("load jobs to rank: %w", err)
	}
	defer rows.Close()
	hasFacts = map[int64]bool{}
	for rows.Next() {
		var d relevance.Doc
		var withFacts bool
		if err := rows.Scan(&d.ID, &d.Title, &withFacts, &d.Body); err != nil {
			return nil, nil, err
		}
		if withFacts {
			hasFacts[d.ID] = true
		}
		docs = append(docs, d)
	}
	return docs, hasFacts, rows.Err()
}

// SetEstimates stores relevance estimates for a user's jobs in one
// statement, writing only the rows whose estimate changed (most do not
// between runs, and every write leaves a dead row behind).
func SetEstimates(ctx context.Context, pool *pgxpool.Pool, userID int64, scores map[int64]int) error {
	if len(scores) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(scores))
	vals := make([]int16, 0, len(scores))
	for id, s := range scores {
		ids = append(ids, id)
		vals = append(vals, int16(min(max(s, -1<<15), 1<<15-1))) //nolint:gosec // clamped to the column's range
	}
	_, err := pool.Exec(ctx, `
		UPDATE user_jobs uj SET estimated_score = v.score
		FROM unnest($2::bigint[], $3::smallint[]) AS v(job_id, score)
		WHERE uj.user_id = $1 AND uj.job_id = v.job_id
		  AND uj.estimated_score IS DISTINCT FROM v.score`, userID, ids, vals)
	if err != nil {
		return fmt.Errorf("save estimates: %w", err)
	}
	return nil
}

// PendingJobIDs lists the user's open jobs waiting to be scored.
func PendingJobIDs(ctx context.Context, pool *pgxpool.Pool, userID int64) ([]int64, error) {
	rows, err := pool.Query(ctx, `
		SELECT uj.job_id FROM user_jobs uj JOIN jobs j ON j.id = uj.job_id
		WHERE uj.user_id = $1 AND uj.stage = 'pending_score' AND j.closed_at IS NULL`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
