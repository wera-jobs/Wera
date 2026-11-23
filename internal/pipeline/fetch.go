// Package pipeline implements the fetch-filter-score steps shared by the
// `wera fetch`, `wera pipeline`, and `wera worker` commands.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"wera/internal/config"
	"wera/internal/metrics"
	"wera/internal/normalize"
	"wera/internal/sources"
	"wera/internal/store"
)

// FetchStats summarizes one fetch pass.
type FetchStats struct {
	CompaniesOK        int
	CompaniesFailed    int
	CompaniesDeferred  int // board down for maintenance; retried later
	CompaniesUnchanged int // answered 304 Not Modified (counted in CompaniesOK)
	JobsSeen           int
	JobsNew            int
	// RetryAt is when deferred companies can be fetched again (zero when
	// nothing was deferred).
	RetryAt time.Time
}

// unscheduledRetry is when to retry a board that redirected to a
// maintenance page outside any known window.
const unscheduledRetry = time.Hour

// deferUntil records a company skipped for maintenance until retryAt.
func (s *FetchStats) deferUntil(retryAt time.Time) {
	s.CompaniesDeferred++
	if s.RetryAt.IsZero() || retryAt.After(s.RetryAt) {
		s.RetryAt = retryAt
	}
}

// Fetcher coordinates fetching all enabled companies concurrently.
// One company failing (404, bad token, timeout) never fails the run.
type Fetcher struct {
	Pool    *pgxpool.Pool
	Env     *config.Env
	Log     *slog.Logger
	Sources map[string]sources.Source
	Metrics *metrics.Registry // optional

	// DetailBudget overrides detailBudget (zero keeps it).
	DetailBudget time.Duration
	detailUntil  time.Time // when this run stops requesting details
}

// Run fetches every company in the list (all enabled ones when only is
// empty, otherwise the company matching only by name or token), upserts
// their jobs, and closes postings that disappeared. Rule filtering is per
// user and happens afterwards (FilterUser).
func (f *Fetcher) Run(ctx context.Context, companies []config.Company, only string) (*FetchStats, error) {
	ids, err := store.SyncCompanies(ctx, f.Pool, companies)
	if err != nil {
		return nil, fmt.Errorf("sync companies: %w", err)
	}

	var todo []config.Company
	for _, c := range companies {
		if only != "" {
			if strings.EqualFold(c.Name, only) || c.Token == only {
				todo = append(todo, c)
			}
			continue
		}
		if c.IsEnabled() {
			todo = append(todo, c)
		}
	}
	if only != "" && len(todo) == 0 {
		return nil, fmt.Errorf("company %q not found in companies.yaml", only)
	}

	stats := &FetchStats{}
	var mu sync.Mutex
	budget := detailBudget
	if f.DetailBudget != 0 {
		budget = f.DetailBudget
	}
	f.detailUntil = time.Now().Add(budget)

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(f.Env.FetchConcurrency)
	now := time.Now()
	for _, c := range todo {

		// Known maintenance window: skip without a request.
		if ma, ok := f.Sources[c.ATS].(sources.MaintenanceAware); ok {
			if end, in := ma.MaintenanceUntil(now); in {
				mu.Lock()
				stats.deferUntil(end)
				mu.Unlock()
				continue
			}
		}
		g.Go(func() error {
			f.fetchCompany(gctx, c, ids[c.Name], stats, &mu)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return stats, err
	}
	if f.Metrics != nil {
		f.Metrics.JobsNewTotal.Add(float64(stats.JobsNew))
	}
	if stats.CompaniesUnchanged > 0 {
		f.Log.Info("boards unchanged since last fetch (304)", "companies", stats.CompaniesUnchanged)
	}
	if stats.CompaniesDeferred > 0 {
		f.Log.Info("companies deferred for job board maintenance",
			"companies", stats.CompaniesDeferred, "retry_at", stats.RetryAt.Format(time.RFC3339))
	}
	return stats, nil
}

// fullSweepEvery is how often an IncrementalLister board is listed in full.
const fullSweepEvery = 20 * time.Hour

// maxDetailsPerRun bounds how many new postings of one company a
// detail-per-job source fetches in a run; the rest follow next run.
const maxDetailsPerRun = 250

// detailBudget bounds the time a run spends fetching job details. With
// thousands of boards, the first runs after adding many detail-per-job
// boards (Workday, SmartRecruiters) would otherwise take hours; past the
// budget, boards are still listed (so closed postings close) and their new
// postings are detailed on later runs.
const detailBudget = 45 * time.Minute

// detailConcurrency is the parallel detail requests per company.
const detailConcurrency = 4

// fetchCompany fetches one company end to end and updates stats/log lines.
func (f *Fetcher) fetchCompany(ctx context.Context, c config.Company, companyID int64, stats *FetchStats, mu *sync.Mutex) {
	src, err := sources.Get(f.Sources, c.ATS)
	if err == nil {
		var listed int
		var seen, newly int
		start := time.Now()
		if cs, ok := src.(sources.ConditionalSource); ok {
			var unchanged bool
			listed, seen, newly, unchanged, err = f.fetchConditional(ctx, c, cs, companyID)
			if err == nil && unchanged {
				if uerr := store.UpdateFetchStatus(ctx, f.Pool, companyID, true, ""); uerr != nil {
					f.Log.Error("recording fetch status failed", "company", c.Name, "err", uerr.Error())
				}
				mu.Lock()
				stats.CompaniesOK++
				stats.CompaniesUnchanged++
				mu.Unlock()
				return
			}
		} else if ds, ok := src.(sources.DetailSource); ok {
			listed, seen, newly, err = f.fetchIncremental(ctx, c, ds, companyID)
		} else {
			var raw []sources.RawJob
			raw, err = src.Fetch(ctx, c.Token)
			listed = len(raw)
			if err == nil {
				seen, newly, err = f.saveCompanyJobs(ctx, src, companyID, raw, nil, true)
			}
		}
		if f.Metrics != nil {
			f.Metrics.FetchDuration.Observe(time.Since(start).Seconds())
		}
		if err == nil {
			f.Log.Info("company fetched", "company", c.Name, "ats", c.ATS,
				"jobs", listed, "new", newly, "ms", time.Since(start).Milliseconds())
			if f.Metrics != nil {
				f.Metrics.FetchTotal.WithLabelValues(c.Name, "ok").Inc()
			}
			mu.Lock()
			stats.CompaniesOK++
			stats.JobsSeen += seen
			stats.JobsNew += newly
			mu.Unlock()
			return
		}
	}

	// A board down for maintenance is retried later, not a failure.
	if errors.Is(err, sources.ErrUnavailable) {
		f.Log.Info("board down for maintenance; deferred", "company", c.Name, "ats", c.ATS)
		mu.Lock()
		stats.deferUntil(time.Now().Add(unscheduledRetry))
		mu.Unlock()
		return
	}

	// Any failure here is recorded and never fails the run.
	f.Log.Warn("fetch failed", "company", c.Name, "ats", c.ATS, "err", err.Error())
	if f.Metrics != nil {
		f.Metrics.FetchTotal.WithLabelValues(c.Name, "error").Inc()
	}
	if uerr := store.UpdateFetchStatus(ctx, f.Pool, companyID, false, err.Error()); uerr != nil {
		f.Log.Error("recording fetch failure failed", "company", c.Name, "err", uerr.Error())
	}
	mu.Lock()
	stats.CompaniesFailed++
	mu.Unlock()
}

// fetchConditional fetches a board with the ETag of its last successful
// fetch. A 304 means nothing changed: no download, no writes (unchanged =
// true). Otherwise the jobs are saved and only then the new ETag stored,
// so a failed save never hides changes from the next run.
func (f *Fetcher) fetchConditional(ctx context.Context, c config.Company, src sources.ConditionalSource, companyID int64) (listed, seen, newly int, unchanged bool, err error) {
	etag, err := store.CompanyETag(ctx, f.Pool, companyID)
	if err != nil {
		return 0, 0, 0, false, err
	}
	raw, newETag, err := src.FetchIfChanged(ctx, c.Token, etag)
	if errors.Is(err, sources.ErrNotModified) {
		return 0, 0, 0, true, nil
	}
	if err != nil {
		return 0, 0, 0, false, err
	}
	seen, newly, err = f.saveCompanyJobs(ctx, src, companyID, raw, nil, true)
	if err != nil {
		return len(raw), seen, newly, false, err
	}
	if err := store.SetCompanyETag(ctx, f.Pool, companyID, newETag); err != nil {
		f.Log.Warn("storing etag failed", "company", c.Name, "err", err)
	}
	return len(raw), seen, newly, false, nil
}

// fetchIncremental lists a detail-per-job board, fetches details for up to
// maxDetailsPerRun postings it has not stored yet, saves those, and marks
// the already-stored ones as still listed. A posting whose detail request
// fails is skipped this run (and retried next run) without failing the
// company.
func (f *Fetcher) fetchIncremental(ctx context.Context, c config.Company, src sources.DetailSource, companyID int64) (listed, seen, newly int, err error) {
	known, err := store.KnownExtIDs(ctx, f.Pool, companyID)
	if err != nil {
		return 0, 0, 0, err
	}
	// Boards that can list just their new postings do so on most runs;
	// a full sweep (which detects closed postings) runs once a day.
	var jobs []sources.RawJob
	complete := true
	il, incremental := src.(sources.IncrementalLister)
	if incremental {
		last, lerr := store.LastFullFetch(ctx, f.Pool, companyID)
		if lerr != nil {
			return 0, 0, 0, lerr
		}
		incremental = time.Since(last) < fullSweepEvery
	}
	if incremental {
		jobs, complete, err = il.ListNew(ctx, c.Token, known)
	} else {
		jobs, err = src.List(ctx, c.Token)
		if errors.Is(err, sources.ErrPartialList) {
			// Save what was listed, but close nothing on a sweep with
			// pages missing, and sweep again next run.
			f.Log.Warn("full sweep missed pages; not closing postings", "company", c.Name, "listed", len(jobs), "err", err)
			complete, err = false, nil
		}
	}
	if err != nil {
		return 0, 0, 0, err
	}
	limit := maxDetailsPerRun
	if dl, ok := src.(sources.DetailLimiter); ok {
		limit = dl.MaxDetailsPerRun()
	}
	if !f.detailUntil.IsZero() && time.Now().After(f.detailUntil) {
		limit = 0
	}
	var fresh []sources.RawJob
	var stillListed, allIDs []string
	capped := false // new postings left for a later run
	for _, j := range jobs {
		allIDs = append(allIDs, j.ExtID)
		switch {
		case known[j.ExtID]:
			stillListed = append(stillListed, j.ExtID)
		case len(fresh) < limit:
			fresh = append(fresh, j)
		default:
			capped = true
		}
	}

	var mu sync.Mutex
	var detailed []sources.RawJob
	failed := 0
	var firstErr error
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(detailConcurrency)
	for i := range fresh {
		j := fresh[i]
		g.Go(func() error {
			if derr := src.Detail(gctx, c.Token, &j); derr != nil {
				mu.Lock()
				failed++
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", j.ExtID, derr)
				}
				mu.Unlock()
				return nil // skipped and retried next run, not fatal
			}
			mu.Lock()
			detailed = append(detailed, j)
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	if ctx.Err() != nil {
		return len(jobs), 0, 0, ctx.Err()
	}
	if failed > 0 {
		f.Log.Warn("some job details failed; retrying next run", "company", c.Name, "failed", failed, "first_err", firstErr)
	}
	if len(fresh) > 0 && len(detailed) == 0 {
		return len(jobs), 0, 0, fmt.Errorf("all %d job detail requests failed: %w", len(fresh), firstErr)
	}

	if err := store.TouchJobs(ctx, f.Pool, companyID, stillListed); err != nil {
		return len(jobs), 0, 0, err
	}
	if !complete {
		allIDs = nil // a partial listing must not close anything
	}
	seen, newly, err = f.saveCompanyJobs(ctx, src, companyID, detailed, allIDs, complete)
	// A sweep that left new postings for later is not recorded: the next
	// run's incremental listing stops at the first page it already knows,
	// so the rest would wait for the next full sweep, 20 hours away.
	if err == nil && complete && !capped {
		if merr := store.MarkFullFetch(ctx, f.Pool, companyID); merr != nil {
			f.Log.Warn("recording full sweep failed", "company", c.Name, "err", merr)
		}
	}
	return len(jobs), seen + len(stillListed), newly, err
}

// saveCompanyJobs upserts the fetched postings, closes postings that
// disappeared (when closeMissing; only after a complete listing), and
// marks the fetch as successful. openIDs lists every posting still on the
// board (nil = exactly the saved ones). It returns
// the number of jobs seen and newly inserted.
func (f *Fetcher) saveCompanyJobs(ctx context.Context, src sources.Source, companyID int64, raw []sources.RawJob, openIDs []string, closeMissing bool) (seen, newly int, err error) {
	inputs := make([]store.JobInput, 0, len(raw))
	collectIDs := openIDs == nil
	for _, rj := range raw {
		if collectIDs {
			openIDs = append(openIDs, rj.ExtID)
		}
		// One posting Postgres refuses would fail the company's whole batch.
		title, location, desc := normalize.Text(rj.Title), normalize.Text(rj.LocationRaw), normalize.Text(rj.DescriptionText)
		inputs = append(inputs, store.JobInput{
			CompanyID:   companyID,
			Source:      src.Name(),
			ExtID:       rj.ExtID,
			Title:       title,
			LocationRaw: location,
			IsRemote:    rj.IsRemote,
			URL:         normalize.Text(rj.URL),
			Department:  normalize.Text(rj.Department),
			Description: desc,
			ContentHash: normalize.ContentHash(title, location, desc),
			PostedAt:    rj.PostedAt,
		})
	}
	seen, newly, err = store.SaveJobs(ctx, f.Pool, inputs)
	if err != nil {
		return seen, newly, fmt.Errorf("save jobs: %w", err)
	}
	// After a partial listing, postings not listed may still be open.
	if closeMissing {
		if _, err := store.CloseMissingJobs(ctx, f.Pool, companyID, openIDs); err != nil {
			return seen, newly, fmt.Errorf("close missing: %w", err)
		}
	}
	if err := store.UpdateFetchStatus(ctx, f.Pool, companyID, true, ""); err != nil {
		return seen, newly, fmt.Errorf("update status: %w", err)
	}
	return seen, newly, nil
}
