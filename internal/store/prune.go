package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Storage limits. Without them the database grows with every posting ever
// seen: on 2026-11-05, 337,000 of 911,000 stored postings were closed, and
// descriptions were 70% of the jobs table.
const (
	// closedJobLifetime is how long a closed posting is kept. A posting
	// back after that is fetched again as new. Postings a user tracks, or
	// wrote a cover letter or resume for, are kept for good.
	closedJobLifetime = 90 * 24 * time.Hour
	// closedVectorLifetime is how long a closed posting keeps its
	// embedding and quick grades, which only rank open postings.
	closedVectorLifetime = 30 * 24 * time.Hour
	// supersededAnalysisLifetime is how long an old score is kept once a
	// newer one replaced it (after a profile edit), so "new matches" can
	// still tell a rescored job from a new one.
	supersededAnalysisLifetime = 30 * 24 * time.Hour
	// pruneBatch bounds one delete statement, so pruning never holds
	// locks for long.
	pruneBatch = 5000
)

// PruneStats is what one Prune removed.
type PruneStats struct {
	Jobs, Vectors, Grades, Analyses int64
}

// Prune deletes what no one will read again, in batches, until done or
// maxTime is spent: long-closed postings (with their facts, embeddings and
// per-user rows), embeddings and quick grades of closed postings, and
// scores superseded long ago.
func Prune(ctx context.Context, pool *pgxpool.Pool, maxTime time.Duration) (PruneStats, error) {
	now := time.Now()
	until := now.Add(maxTime)
	var st PruneStats
	var err error
	if st.Jobs, err = inBatches(ctx, pool, until, `
		DELETE FROM jobs WHERE id IN (
		  SELECT j.id FROM jobs j
		  WHERE j.closed_at < $1
		    AND NOT EXISTS (SELECT 1 FROM applications a WHERE a.job_id = j.id)
		    AND NOT EXISTS (SELECT 1 FROM cover_letters c WHERE c.job_id = j.id)
		    AND NOT EXISTS (SELECT 1 FROM resumes r WHERE r.job_id = j.id)
		  LIMIT $2)`, now.Add(-closedJobLifetime)); err != nil {
		return st, fmt.Errorf("prune closed jobs: %w", err)
	}
	if st.Vectors, err = inBatches(ctx, pool, until, `
		DELETE FROM job_embeddings WHERE job_id IN (
		  SELECT e.job_id FROM job_embeddings e JOIN jobs j ON j.id = e.job_id
		  WHERE j.closed_at < $1 LIMIT $2)`, now.Add(-closedVectorLifetime)); err != nil {
		return st, fmt.Errorf("prune embeddings: %w", err)
	}
	if st.Grades, err = inBatches(ctx, pool, until, `
		DELETE FROM quick_grades WHERE (user_id, job_id) IN (
		  SELECT q.user_id, q.job_id FROM quick_grades q JOIN jobs j ON j.id = q.job_id
		  WHERE j.closed_at < $1 LIMIT $2)`, now.Add(-closedVectorLifetime)); err != nil {
		return st, fmt.Errorf("prune quick grades: %w", err)
	}
	if st.Analyses, err = inBatches(ctx, pool, until, `
		DELETE FROM analyses WHERE id IN (
		  SELECT a.id FROM analyses a
		  WHERE a.user_id IS NOT NULL
		    AND EXISTS (SELECT 1 FROM analyses b
		                WHERE b.job_id = a.job_id AND b.user_id = a.user_id AND b.kind = a.kind
		                  AND b.created_at > a.created_at AND b.created_at < $1)
		    AND NOT EXISTS (SELECT 1 FROM user_jobs uj WHERE uj.analysis_id = a.id)
		    AND NOT EXISTS (SELECT 1 FROM applications ap WHERE ap.user_id = a.user_id AND ap.job_id = a.job_id)
		  LIMIT $2)`, now.Add(-supersededAnalysisLifetime)); err != nil {
		return st, fmt.Errorf("prune analyses: %w", err)
	}
	return st, nil
}

// inBatches runs a delete taking a cutoff ($1) and a batch size ($2) until
// it deletes less than a batch or the time is past until, and returns the
// rows deleted. Whatever is left, the next run continues.
func inBatches(ctx context.Context, pool *pgxpool.Pool, until time.Time, q string, cutoff time.Time) (int64, error) {
	var total int64
	for time.Now().Before(until) {
		tag, err := pool.Exec(ctx, q, cutoff, pruneBatch)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < pruneBatch {
			break
		}
	}
	return total, nil
}
