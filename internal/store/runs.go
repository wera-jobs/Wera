package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TryAdvisoryLock attempts the run lock (PLAN.md section 7, key 4242) on a
// dedicated pool connection. When acquired, the returned release function
// must be called (with a live context) to unlock and return the
// connection. Advisory locks are session-scoped, so the connection is
// held between the two calls.
func TryAdvisoryLock(ctx context.Context, pool *pgxpool.Pool, key int64) (acquired bool, release func(context.Context), err error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return false, nil, fmt.Errorf("acquire lock connection: %w", err)
	}
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&acquired); err != nil {
		conn.Release()
		return false, nil, fmt.Errorf("try advisory lock: %w", err)
	}
	if !acquired {
		conn.Release()
		return false, nil, nil
	}
	return true, func(releaseCtx context.Context) {
		defer conn.Release()
		if _, uerr := conn.Exec(releaseCtx, "SELECT pg_advisory_unlock($1)", key); uerr != nil {
			// The connection may be dead; Postgres frees the lock with it.
			_ = uerr
		}
	}, nil
}

// TryUserLock takes the per-user matching lock (advisory lock pair
// (userLockClass, userID)) so a background match after a profile save and
// a scheduled run never score the same user at once. Same contract as
// TryAdvisoryLock.
func TryUserLock(ctx context.Context, pool *pgxpool.Pool, userID int64) (acquired bool, release func(context.Context), err error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return false, nil, fmt.Errorf("acquire lock connection: %w", err)
	}
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, $2)", userLockClass, int32(userID)).Scan(&acquired); err != nil {
		conn.Release()
		return false, nil, fmt.Errorf("try user lock: %w", err)
	}
	if !acquired {
		conn.Release()
		return false, nil, nil
	}
	return true, func(releaseCtx context.Context) {
		defer conn.Release()
		_, _ = conn.Exec(releaseCtx, "SELECT pg_advisory_unlock($1, $2)", userLockClass, int32(userID))
	}, nil
}

// WaitUserLock waits for the per-user matching lock (see TryUserLock)
// until it is free or ctx ends.
func WaitUserLock(ctx context.Context, pool *pgxpool.Pool, userID int64) (release func(context.Context), err error) {
	for {
		got, release, err := TryUserLock(ctx, pool, userID)
		if err != nil || got {
			return release, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// userLockClass is the first key of the per-user advisory locks.
const userLockClass int32 = 4243

// RunTotals is the summary written to a run row when it finishes.
type RunTotals struct {
	Status           string // ok | partial | failed
	CompaniesOK      int
	CompaniesFailed  int
	JobsSeen         int
	JobsNew          int
	JobsExcluded     int
	JobsScored       int
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
	CostUSD          float64
	Error            string // non-empty only for fatal errors
}

// StartRun inserts a runs row and returns its id. The caller must finish
// it with FinishRun, even on error (deferred with a suitable status).
func StartRun(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var id int64
	err := pool.QueryRow(ctx, `INSERT INTO runs DEFAULT VALUES RETURNING id`).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert run row: %w", err)
	}
	return id, nil
}

// FinishRun writes the summary and closes the run.
func FinishRun(ctx context.Context, pool *pgxpool.Pool, id int64, t RunTotals) error {
	_, err := pool.Exec(ctx, `
		UPDATE runs
		SET finished_at = now(),
		    status = $2,
		    companies_ok = $3,
		    companies_failed = $4,
		    jobs_seen = $5,
		    jobs_new = $6,
		    jobs_excluded = $7,
		    jobs_scored = $8,
		    prompt_tokens = $9,
		    cached_tokens = $10,
		    completion_tokens = $11,
		    cost_usd = $12,
		    error = NULLIF($13, '')
		WHERE id = $1`,
		id, t.Status, t.CompaniesOK, t.CompaniesFailed, t.JobsSeen, t.JobsNew,
		t.JobsExcluded, t.JobsScored, t.PromptTokens, t.CachedTokens,
		t.CompletionTokens, t.CostUSD, t.Error)
	if err != nil {
		return fmt.Errorf("finish run %d: %w", id, err)
	}
	return nil
}
