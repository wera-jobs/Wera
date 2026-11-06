package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/config"
	"wera/internal/filter"
	"wera/internal/metrics"
	"wera/internal/store"
)

// advisoryLockKey is the fixed key (PLAN.md section 7) that serializes
// pipeline runs across processes.
const advisoryLockKey = 4242

// Pipeline is one full fetch + filter + score run, shared by
// `wera pipeline` and `wera worker`.
type Pipeline struct {
	Pool      *pgxpool.Pool
	Env       *config.Env
	Log       *slog.Logger
	Companies []config.Company
	Engine    *filter.Engine
	Metrics   *metrics.Registry // optional

	// CatchUpAt is set by RunOnce when companies were deferred for job
	// board maintenance: the time a catch-up run can fetch them (zero
	// otherwise).
	CatchUpAt time.Time
}

// RunOnce executes a single pipeline run under the Postgres advisory
// lock. It returns ran=false (with a log line, no error) when another
// process already holds the lock, so overlapping runs never happen.
func (p *Pipeline) RunOnce(ctx context.Context) (ran bool, err error) {
	// The advisory lock is session-scoped, so it lives on one held
	// connection for the whole run.
	gotLock, release, err := store.TryAdvisoryLock(ctx, p.Pool, advisoryLockKey)
	if err != nil {
		return false, err
	}
	if !gotLock {
		p.Log.Warn("another pipeline run is in progress; skipping this run")
		return false, nil
	}
	defer func() {
		// Use a fresh context so a canceled worker still unlocks.
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		release(unlockCtx)
	}()

	runID, err := store.StartRun(ctx, p.Pool)
	if err != nil {
		return true, err
	}
	// Even on failure the run row must be closed; use a fresh context so
	// a canceled worker still records the outcome.
	finalize := func(totals store.RunTotals) {
		finCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if ferr := store.FinishRun(finCtx, p.Pool, runID, totals); ferr != nil {
			p.Log.Error("recording run failed", "run_id", runID, "err", ferr)
		}
	}

	p.housekeeping(ctx)

	fetcher := &Fetcher{Pool: p.Pool, Env: p.Env, Log: p.Log, Sources: NewSourceRegistry(p.Env), Metrics: p.Metrics}
	comps, err := WithAdded(ctx, p.Pool, p.Companies)
	if err != nil {
		p.Log.Error("loading added boards failed; fetching companies.yaml only", "err", err)
		comps = p.Companies
	}
	fstats, fetchErr := fetcher.Run(ctx, comps, "")
	p.CatchUpAt = time.Time{}
	if fstats != nil {
		p.CatchUpAt = fstats.RetryAt
	}

	var excluded int
	var sstats *ScoreStats
	var scoreErr error
	if fetchErr == nil {
		p.closeUnreadBoards(ctx, fstats)
		p.embedBackfill(ctx)
		excluded, sstats, scoreErr = p.matchAll(ctx)
	}

	totals := store.RunTotals{Status: "ok", JobsExcluded: excluded}
	if fstats != nil {
		totals.CompaniesOK = fstats.CompaniesOK
		totals.CompaniesFailed = fstats.CompaniesFailed
		totals.JobsSeen = fstats.JobsSeen
		totals.JobsNew = fstats.JobsNew
	}
	if sstats != nil {
		totals.JobsScored = sstats.Scored + sstats.Excluded + sstats.Failed
		totals.PromptTokens = sstats.PromptTokens
		totals.CachedTokens = sstats.CachedTokens
		totals.CompletionTokens = sstats.CompletionTokens
		totals.CostUSD = sstats.CostUSD
	}
	switch {
	case ctx.Err() != nil:
		totals.Status = "failed"
		totals.Error = "run canceled: " + ctx.Err().Error()
	case fetchErr != nil:
		totals.Status = "failed"
		totals.Error = fetchErr.Error()
	case scoreErr != nil:
		totals.Status = "failed"
		totals.Error = scoreErr.Error()
	case totals.CompaniesFailed > 0:
		totals.Status = "partial"
	}
	if p.Metrics != nil && totals.Status != "failed" {
		p.Metrics.RunLastSuccess.SetToCurrentTime()
	}
	finalize(totals)

	p.Log.Info("pipeline run finished", "run_id", runID, "status", totals.Status,
		"companies_ok", totals.CompaniesOK, "companies_failed", totals.CompaniesFailed,
		"companies_deferred", deferredOf(fstats),
		"jobs_seen", totals.JobsSeen, "jobs_new", totals.JobsNew,
		"jobs_excluded", totals.JobsExcluded, "jobs_scored", totals.JobsScored,
		"prompt_tokens", totals.PromptTokens, "cached_tokens", totals.CachedTokens,
		"cost_usd", fmt.Sprintf("%.6f", totals.CostUSD))

	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	return true, fetchErr
}

// matchAll filters and scores new jobs for every user with a ready
// profile (see MatchUser). One user's failure is logged and does not stop
// the others; the first error is returned so the run is marked failed.
func (p *Pipeline) matchAll(ctx context.Context) (excluded int, total *ScoreStats, firstErr error) {
	profiles, err := store.ReadyProfiles(ctx, p.Pool)
	if err != nil {
		return 0, nil, fmt.Errorf("load profiles: %w", err)
	}
	total = &ScoreStats{}
	for _, prof := range profiles {
		if ctx.Err() != nil {
			return excluded, total, ctx.Err()
		}
		ex, st, err := p.MatchUser(ctx, prof)
		excluded += ex
		total.Add(st)
		if err != nil {
			p.Log.Error("matching failed", "user_id", prof.UserID, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return excluded, total, firstErr
}

// MatchUser filters and scores one user's new jobs under the per-user
// lock. Spending is limited by Allowance (per-run cap, the user's monthly
// budget, the global monthly cap); jobs left over stay pending. When the
// user is already being matched elsewhere it returns without doing
// anything.
func (p *Pipeline) MatchUser(ctx context.Context, prof *store.Profile) (excluded int, st *ScoreStats, err error) {
	got, release, err := store.TryUserLock(ctx, p.Pool, prof.UserID)
	if err != nil {
		return 0, nil, err
	}
	if !got {
		p.Log.Info("user is already being matched; skipping", "user_id", prof.UserID)
		return 0, nil, nil
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		release(unlockCtx)
	}()

	fst, err := FilterUser(ctx, p.Pool, p.Engine, NewEmbedder(p.Env, p.Log), prof, p.Metrics)
	if err != nil {
		return 0, nil, err
	}
	allowance, why, err := Allowance(ctx, p.Pool, p.Env, prof.UserID)
	if err != nil {
		return fst.Excluded, nil, fmt.Errorf("load budget: %w", err)
	}
	if allowance <= 0 {
		p.Log.Warn("not scoring: "+why, "user_id", prof.UserID)
		return fst.Excluded, nil, nil
	}
	st, err = ScoreUser(ctx, p.Pool, p.Env, p.Log, prof, 0, nil, allowance, p.Metrics)
	return fst.Excluded, st, err
}

func deferredOf(s *FetchStats) int {
	if s == nil {
		return 0
	}
	return s.CompaniesDeferred
}

// ScoreNow scores one of the user's unscored jobs right away (the user
// opened it), within the user's allowance. Already-scored jobs and jobs
// the user has no match for are left alone.
func (p *Pipeline) ScoreNow(ctx context.Context, userID, jobID int64) error {
	prof, err := store.GetProfile(ctx, p.Pool, userID)
	if err != nil || prof == nil || !prof.Ready() {
		return err
	}
	allowance, why, err := Allowance(ctx, p.Pool, p.Env, userID)
	if err != nil {
		return err
	}
	if allowance <= 0 {
		return fmt.Errorf("not scoring: %s", why)
	}
	_, err = ScoreUser(ctx, p.Pool, p.Env, p.Log, prof, 1, []int64{jobID}, allowance, p.Metrics)
	return err
}

// ShareJob offers a job one user added to everyone else: each user's
// rules filter it like a fetched posting, and the users it suits get it
// scored now (within their allowance), so it reaches their lists and
// digests without waiting for the next run. Users being matched right now
// are skipped; that match or the next run picks the job up.
func (p *Pipeline) ShareJob(ctx context.Context, jobID, addedBy int64) (offered, scored int, err error) {
	profiles, err := store.ReadyProfiles(ctx, p.Pool)
	if err != nil {
		return 0, 0, fmt.Errorf("load profiles: %w", err)
	}
	for _, prof := range profiles {
		if prof.UserID == addedBy {
			continue
		}
		if ctx.Err() != nil {
			return offered, scored, ctx.Err()
		}
		suits, err := p.offer(ctx, prof, jobID)
		if err != nil {
			p.Log.Warn("sharing an added job failed", "user_id", prof.UserID, "job_id", jobID, "err", err)
			continue
		}
		offered++
		if !suits {
			continue
		}
		if err := p.ScoreNow(ctx, prof.UserID, jobID); err != nil {
			p.Log.Info("added job left for the next run", "user_id", prof.UserID, "job_id", jobID, "why", err)
			continue
		}
		if stage, _ := store.UserJobStage(ctx, p.Pool, prof.UserID, jobID); stage != "pending_score" {
			scored++
		}
	}
	return offered, scored, nil
}

// offer filters the user's new jobs (the shared one among them) under
// their lock and reports whether the job passed their rules.
func (p *Pipeline) offer(ctx context.Context, prof *store.Profile, jobID int64) (bool, error) {
	got, release, err := store.TryUserLock(ctx, p.Pool, prof.UserID)
	if err != nil || !got {
		return false, err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		release(unlockCtx)
	}()
	if _, err := FilterUser(ctx, p.Pool, p.Engine, NewEmbedder(p.Env, p.Log), prof, p.Metrics); err != nil {
		return false, err
	}
	stage, err := store.UserJobStage(ctx, p.Pool, prof.UserID, jobID)
	return stage == "pending_score", err
}

// embedBackfill embeds postings that have facts but no embedding yet
// (after a deploy or a model change), for at most ten minutes a run, so
// ranking users rarely waits on it. Failures are logged; ranking falls
// back to word matching for jobs without embeddings.
func (p *Pipeline) embedBackfill(ctx context.Context) {
	e := NewEmbedder(p.Env, p.Log)
	if e == nil {
		return
	}
	n, err := e.Backfill(ctx, p.Pool, 10*time.Minute)
	if err != nil {
		p.Log.Warn("embedding postings failed", "embedded", n, "err", err)
	} else if n > 0 {
		p.Log.Info("embedded postings", "jobs", n)
	}
}

// unreadAfter is how long a board can go without a successful read before
// its postings are closed. It spans a long weekend between weekday-only
// runs and a few days of the worker being down, with room to spare.
const unreadAfter = 7 * 24 * time.Hour

// closeUnreadBoards closes the postings of boards not read for unreadAfter,
// after this run's fetch, so every board that answered is current. A run
// where most boards failed says more about this machine's network than
// about the boards, so it closes nothing.
func (p *Pipeline) closeUnreadBoards(ctx context.Context, f *FetchStats) {
	if f == nil || f.CompaniesOK <= f.CompaniesFailed {
		return
	}
	boards, jobs, err := store.CloseUnreadBoards(ctx, p.Pool, time.Now().Add(-unreadAfter))
	if err != nil {
		p.Log.Error("closing postings of unread boards failed", "err", err)
	} else if jobs > 0 {
		p.Log.Info("closed postings of boards unread for a week", "boards", boards, "jobs", jobs)
	}
}

// housekeeping closes added postings past their lifetime, drops per-user
// rows for long-closed postings nobody acted on, and prunes old data (see
// store.Prune). Failures are
// logged; they never stop a run.
func (p *Pipeline) housekeeping(ctx context.Context) {
	if n, err := store.CloseStaleManualJobs(ctx, p.Pool); err != nil {
		p.Log.Error("closing stale added jobs failed", "err", err)
	} else if n > 0 {
		p.Log.Info("closed stale added jobs", "jobs", n)
	}
	if n, err := store.PruneClosedMatches(ctx, p.Pool); err != nil {
		p.Log.Error("pruning closed matches failed", "err", err)
	} else if n > 0 {
		p.Log.Info("pruned matches for closed jobs", "rows", n)
	}
	if st, err := store.Prune(ctx, p.Pool, pruneTime); err != nil {
		p.Log.Error("pruning old data failed", "err", err)
	} else if st != (store.PruneStats{}) {
		p.Log.Info("pruned old data", "jobs", st.Jobs, "embeddings", st.Vectors,
			"quick_grades", st.Grades, "analyses", st.Analyses)
	}
}

// pruneTime bounds the pruning in one run; the next run continues.
const pruneTime = 5 * time.Minute
