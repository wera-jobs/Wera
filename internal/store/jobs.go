package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/config"
	"wera/internal/filter"
	"wera/internal/scoring"
)

// JobInput is one normalized job ready to be upserted.
type JobInput struct {
	CompanyID   int64
	Source      string
	ExtID       string
	Title       string
	LocationRaw string
	IsRemote    *bool
	URL         string
	Department  string
	Description string
	ContentHash string
	PostedAt    *time.Time
}

// SaveJobs upserts one company's jobs by (source, ext_id) using one batch,
// writing only new or changed postings. It returns
// the number of jobs seen and how many were new insertions.
//
// Reappearance: an upsert clears closed_at and refreshes last_seen_at, so
// a reposted job is simply visible again (still deduped by content_hash).
// A changed content_hash, or a closed posting listed again, moves the
// job's changed_at, so the next filter pass of every user reads it again
// (see FilterForUser).
func SaveJobs(ctx context.Context, pool *pgxpool.Pool, jobs []JobInput) (seen, newly int, err error) {
	if len(jobs) == 0 {
		return 0, 0, nil
	}
	// Only new or changed postings are written; unchanged ones (same
	// content hash) just get last_seen_at in one statement.
	stored, err := storedHashes(ctx, pool, jobs[0].CompanyID)
	if err != nil {
		return 0, 0, err
	}
	var changed []JobInput
	var unchangedIDs []string
	for _, j := range jobs {
		if h, ok := stored[j.Source+"\x00"+j.ExtID]; ok && h == j.ContentHash {
			unchangedIDs = append(unchangedIDs, j.ExtID)
		} else {
			changed = append(changed, j)
		}
	}
	if err := TouchJobs(ctx, pool, jobs[0].CompanyID, unchangedIDs); err != nil {
		return 0, 0, err
	}
	seen = len(unchangedIDs)
	jobs = changed
	if len(jobs) == 0 {
		return seen, 0, nil
	}
	const q = `
		INSERT INTO jobs (company_id, source, ext_id, title, location_raw,
		                  is_remote, url, department, description,
		                  content_hash, posted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (source, ext_id) DO UPDATE
		SET title           = EXCLUDED.title,
		    location_raw    = EXCLUDED.location_raw,
		    is_remote       = EXCLUDED.is_remote,
		    url             = EXCLUDED.url,
		    department      = EXCLUDED.department,
		    description     = EXCLUDED.description,
		    content_hash    = EXCLUDED.content_hash,
		    posted_at       = EXCLUDED.posted_at,
		    last_seen_at    = now(),
		    changed_at      = CASE WHEN jobs.closed_at IS NOT NULL OR jobs.content_hash <> EXCLUDED.content_hash
		                           THEN now() ELSE jobs.changed_at END,
		    closed_at       = NULL
		RETURNING (xmax = 0) AS is_new`

	b := &pgx.Batch{}
	for _, j := range jobs {
		b.Queue(q, j.CompanyID, j.Source, j.ExtID, j.Title, j.LocationRaw,
			j.IsRemote, j.URL, j.Department, j.Description, j.ContentHash, j.PostedAt)
	}
	br := pool.SendBatch(ctx, b)
	defer br.Close()
	for range jobs {
		var isNew bool
		if err := br.QueryRow().Scan(&isNew); err != nil {
			return seen, newly, fmt.Errorf("upsert job: %w", err)
		}
		seen++
		if isNew {
			newly++
		}
	}
	return seen, newly, nil
}

// CloseMissingJobs marks jobs that no longer appear in a company's feed
// as closed. It is only called after a successful fetch, so a flaky feed
// never causes mass closing. Jobs users added stay open: they are filed
// under the company but the feed never listed them (see
// CloseStaleManualJobs). Returns how many jobs were closed.
func CloseMissingJobs(ctx context.Context, pool *pgxpool.Pool, companyID int64, openExtIDs []string) (int64, error) {
	if openExtIDs == nil {
		openExtIDs = []string{}
	}
	tag, err := pool.Exec(ctx, `
		UPDATE jobs
		SET closed_at = now(), last_seen_at = now()
		WHERE company_id = $1
		  AND closed_at IS NULL
		  AND source <> 'manual'
		  AND NOT (ext_id = ANY($2::text[]))`, companyID, openExtIDs)
	if err != nil {
		return 0, fmt.Errorf("close missing jobs for company %d: %w", companyID, err)
	}
	return tag.RowsAffected(), nil
}

// FilterOutcome is what one rule-filter pass did.
type FilterOutcome struct {
	Excluded, Pending int            // excluded now, and waiting to be scored
	ByReason          map[string]int // excluded jobs per reason, for metrics
	// Refiltered is set when the user's preferences had changed since the
	// last pass, so every open job was filtered again. The caller re-applies
	// the post-scoring rules to scored jobs, then calls MarkFiltered.
	Refiltered bool
}

// FilterForUser runs the rule engine over the open jobs added, changed or
// reopened since the user's last pass (all of them on a first pass, or
// when the preferences changed since the last one), and stores the
// outcomes in user_jobs. Only matches get a row: a job the rules reject
// leaves none, and a row it had is deleted unless the user tracks the job.
// Only outcomes that changed are written: a new role family touches the
// jobs it lets in, not every job.
//
// A scored job that still passes keeps its score, so a preference change
// never sends the user's scored matches back to the queue. A job that
// changed content, or that passes now after being excluded, goes to the
// queue, where an analysis it already has is linked again for free.
//
// It is built for a user facing hundreds of thousands of open jobs: title
// and location rules run first on light rows, descriptions are loaded only
// for the jobs that pass them, and outcomes are written in bulk (see
// writeOutcomes) instead of a statement per job.
func FilterForUser(ctx context.Context, pool *pgxpool.Pool, eng *filter.Engine, userID int64, prefs *config.Preferences) (*FilterOutcome, error) {
	prefsJSON, err := json.Marshal(prefs)
	if err != nil {
		return nil, err
	}
	var all bool
	var through *time.Time
	var started time.Time
	if err := pool.QueryRow(ctx, `SELECT now()`).Scan(&started); err != nil {
		return nil, err
	}
	if err := pool.QueryRow(ctx, `
		SELECT filtered_preferences IS DISTINCT FROM $2::jsonb, filtered_through FROM profiles WHERE user_id = $1`,
		userID, prefsJSON).Scan(&all, &through); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("compare preferences: %w", err)
	}
	var since *time.Time
	if through != nil && !all {
		// A posting saved while the last pass ran can carry a time just
		// before it started: read a margin again.
		t := through.Add(-filterOverlap)
		since = &t
	}
	rows, err := pool.Query(ctx, `
		WITH todo AS (
		  SELECT id FROM jobs WHERE closed_at IS NULL AND ($2::timestamptz IS NULL OR changed_at > $2)
		  UNION
		  SELECT uj.job_id FROM user_jobs uj JOIN jobs j ON j.id = uj.job_id
		  WHERE uj.user_id = $1 AND j.closed_at IS NULL AND uj.content_hash <> j.content_hash)
		SELECT j.id, j.content_hash, j.title, COALESCE(j.location_raw, ''),
		       uj.job_id IS NOT NULL, COALESCE(uj.stage, ''), COALESCE(uj.content_hash, ''),
		       uj.matched_categories, uj.exclude_reason, uj.exclude_evidence, uj.flags
		FROM todo
		JOIN jobs j ON j.id = todo.id
		LEFT JOIN user_jobs uj ON uj.job_id = j.id AND uj.user_id = $1
		WHERE uj.job_id IS NULL OR j.added_by IS DISTINCT FROM $1`, userID, since)
	if err != nil {
		return nil, fmt.Errorf("load jobs to filter: %w", err)
	}
	type outcome struct {
		id         int64
		hash       string
		title, loc string
		result     filter.Result
		old        *filterRow
	}
	var outs []outcome
	for rows.Next() {
		var o outcome
		var had bool
		var old filterRow
		var reason, evidence *string
		if err := rows.Scan(&o.id, &o.hash, &o.title, &o.loc,
			&had, &old.stage, &old.hash, &old.cats, &reason, &evidence, &old.flags); err != nil {
			rows.Close()
			return nil, err
		}
		if had {
			old.reason, old.evidence = deref(reason), deref(evidence)
			o.old = &old
		}
		outs = append(outs, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := &FilterOutcome{ByReason: map[string]int{}, Refiltered: all}
	if len(outs) == 0 {
		return out, markThrough(ctx, pool, userID, started)
	}
	// Title and location rules on every core (the engine is read-only).
	workers := runtime.GOMAXPROCS(0)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := w; i < len(outs); i += workers {
				outs[i].result = eng.ApplyTitle(prefs, outs[i].title, outs[i].loc)
			}
		}(w)
	}
	wg.Wait()
	var survivors []int64
	for _, o := range outs {
		if o.result.Stage != filter.StageExcluded {
			survivors = append(survivors, o.id)
		}
	}

	// Descriptions only for the jobs that passed the title and location rules.
	descs := make(map[int64]string, len(survivors))
	if len(survivors) > 0 {
		drows, err := pool.Query(ctx, `SELECT id, COALESCE(description, '') FROM jobs WHERE id = ANY($1::bigint[])`, survivors)
		if err != nil {
			return nil, fmt.Errorf("load descriptions: %w", err)
		}
		for drows.Next() {
			var id int64
			var d string
			if err := drows.Scan(&id, &d); err != nil {
				drows.Close()
				return nil, err
			}
			descs[id] = d
		}
		drows.Close()
		if err := drows.Err(); err != nil {
			return nil, err
		}
	}

	// Outcomes for jobs the user has no row for are copied straight into
	// user_jobs (a new user's matches); changed ones go through a staging
	// table and an upsert. Rejected jobs get no row, and lose the one they
	// had (see writeOutcomes).
	var added, changed [][]any
	var dropped []int64
	for i := range outs {
		o := &outs[i]
		if o.result.Stage != filter.StageExcluded {
			o.result = eng.ApplyDescription(prefs, o.result, descs[o.id])
		}
		next := filterRow{stage: o.result.Stage, hash: o.hash, cats: o.result.Categories, flags: o.result.Flags}
		if o.result.Stage == filter.StageExcluded {
			out.Excluded++
			out.ByReason[o.result.Reason]++
			next.reason, next.evidence = o.result.Reason, o.result.Evidence
		} else if keepsStage(o.old, o.hash) {
			next.stage, next.reason, next.evidence = o.old.stage, o.old.reason, o.old.evidence
		}
		if next.stage == filter.StagePendingScore {
			out.Pending++
		}
		if o.old != nil && o.old.same(next) {
			continue
		}
		if next.stage == filter.StageExcluded {
			if o.old == nil {
				continue
			}
			dropped = append(dropped, o.id)
		}
		row := []any{userID, o.id, next.hash, next.stage, orEmpty(next.cats),
			nilIfEmpty(next.reason), nilIfEmpty(next.evidence), orEmpty(next.flags)}
		if o.old == nil {
			added = append(added, row)
		} else {
			changed = append(changed, row)
		}
	}
	if err := writeOutcomes(ctx, pool, userID, added, changed, dropped); err != nil {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			return nil, err
		}
		// Another pass added some of the rows meanwhile: upsert them all.
		if err := writeOutcomes(ctx, pool, userID, nil, append(added, changed...), dropped); err != nil {
			return nil, err
		}
	}
	return out, markThrough(ctx, pool, userID, started)
}

// filterOverlap is how far before the last pass's start a pass reads
// again, for postings saved while it ran.
const filterOverlap = 10 * time.Minute

// markThrough records that the user's jobs are filtered up to a pass that
// started at t. Users without a profile row are filtered in full each time.
func markThrough(ctx context.Context, pool *pgxpool.Pool, userID int64, t time.Time) error {
	_, err := pool.Exec(ctx, `UPDATE profiles SET filtered_through = $2 WHERE user_id = $1`, userID, t)
	return err
}

var outcomeColumns = []string{"user_id", "job_id", "content_hash", "stage", "matched_categories", "exclude_reason", "exclude_evidence", "flags"}

// writeOutcomes stores rule outcomes in one transaction: added rows are
// copied into user_jobs, changed rows upserted, and the rows of dropped
// jobs (now rejected) deleted, except for jobs the user tracks, which keep
// a row marked excluded.
func writeOutcomes(ctx context.Context, pool *pgxpool.Pool, userID int64, added, changed [][]any, dropped []int64) error {
	if len(added) == 0 && len(changed) == 0 {
		return nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if len(added) > 0 {
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"user_jobs"}, outcomeColumns, pgx.CopyFromRows(added)); err != nil {
			return fmt.Errorf("copy new rule outcomes: %w", err)
		}
	}
	if len(changed) > 0 {
		if _, err := tx.Exec(ctx, `
			CREATE TEMP TABLE filter_in (
			  user_id BIGINT, job_id BIGINT, content_hash TEXT, stage TEXT, matched_categories TEXT[],
			  exclude_reason TEXT, exclude_evidence TEXT, flags TEXT[]
			) ON COMMIT DROP`); err != nil {
			return fmt.Errorf("create staging table: %w", err)
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"filter_in"}, outcomeColumns, pgx.CopyFromRows(changed)); err != nil {
			return fmt.Errorf("copy rule outcomes: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO user_jobs (user_id, job_id, content_hash, stage, matched_categories,
			                       exclude_reason, exclude_evidence, flags, updated_at)
			SELECT user_id, job_id, content_hash, stage, matched_categories, exclude_reason, exclude_evidence, flags, now()
			FROM filter_in
			ON CONFLICT (user_id, job_id) DO UPDATE
			SET content_hash = EXCLUDED.content_hash, stage = EXCLUDED.stage,
			    matched_categories = EXCLUDED.matched_categories,
			    exclude_reason = EXCLUDED.exclude_reason, exclude_evidence = EXCLUDED.exclude_evidence,
			    flags = EXCLUDED.flags, updated_at = now()`); err != nil {
			return fmt.Errorf("save rule outcomes: %w", err)
		}
	}
	if len(dropped) > 0 {
		if _, err := tx.Exec(ctx, `
			DELETE FROM user_jobs uj
			WHERE uj.user_id = $1 AND uj.job_id = ANY($2::bigint[]) AND uj.stage = 'excluded'
			  AND NOT EXISTS (SELECT 1 FROM applications a WHERE a.user_id = uj.user_id AND a.job_id = uj.job_id)`,
			userID, dropped); err != nil {
			return fmt.Errorf("drop rejected jobs: %w", err)
		}
	}
	return tx.Commit(ctx)
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// filterRow is the part of a user_jobs row the rule filter writes.
type filterRow struct {
	stage, hash      string
	cats, flags      []string
	reason, evidence string
}

func (r *filterRow) same(o filterRow) bool {
	return r.stage == o.stage && r.hash == o.hash && r.reason == o.reason && r.evidence == o.evidence &&
		slices.Equal(r.cats, o.cats) && slices.Equal(r.flags, o.flags)
}

// keepsStage reports whether a job that passes the rules keeps the stage it
// has: a score (or failed score) for the same content, or a place in the
// queue. Anything excluded before goes back to the queue, where the
// post-scoring rules decide again.
func keepsStage(old *filterRow, hash string) bool {
	if old == nil || old.hash != hash {
		return false
	}
	switch old.stage {
	case scoring.StageScored, scoring.StageFailed, filter.StagePendingScore:
		return true
	}
	return false
}

// MarkFiltered records the preferences the user's jobs were filtered with,
// once a pass that re-filtered them all has finished.
func MarkFiltered(ctx context.Context, pool *pgxpool.Pool, userID int64, prefs *config.Preferences) error {
	prefsJSON, err := json.Marshal(prefs)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `UPDATE profiles SET filtered_preferences = $2::jsonb WHERE user_id = $1`, userID, prefsJSON)
	return err
}

// KnownExtIDs returns the ext_ids already stored for a company (open or
// closed), so detail-per-job sources only fetch new postings.
func KnownExtIDs(ctx context.Context, pool *pgxpool.Pool, companyID int64) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `SELECT ext_id FROM jobs WHERE company_id = $1`, companyID)
	if err != nil {
		return nil, fmt.Errorf("load known jobs for company %d: %w", companyID, err)
	}
	defer rows.Close()
	known := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		known[id] = true
	}
	return known, rows.Err()
}

// TouchJobs marks already-stored postings as still listed: last_seen_at
// moves to now and a closed posting that reappeared is reopened (and
// changed, so users' filters read it again). Their content is left as
// stored.
func TouchJobs(ctx context.Context, pool *pgxpool.Pool, companyID int64, extIDs []string) error {
	if len(extIDs) == 0 {
		return nil
	}
	_, err := pool.Exec(ctx, `
		UPDATE jobs SET last_seen_at = now(), closed_at = NULL,
		       changed_at = CASE WHEN closed_at IS NOT NULL THEN now() ELSE changed_at END
		WHERE company_id = $1 AND ext_id = ANY($2::text[])`, companyID, extIDs)
	if err != nil {
		return fmt.Errorf("touch jobs for company %d: %w", companyID, err)
	}
	return nil
}

// storedHashes maps source+NUL+ext_id to the stored content hash for a
// company's jobs.
func storedHashes(ctx context.Context, pool *pgxpool.Pool, companyID int64) (map[string]string, error) {
	rows, err := pool.Query(ctx, `SELECT source, ext_id, content_hash FROM jobs WHERE company_id = $1`, companyID)
	if err != nil {
		return nil, fmt.Errorf("load stored hashes for company %d: %w", companyID, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var src, id, h string
		if err := rows.Scan(&src, &id, &h); err != nil {
			return nil, err
		}
		out[src+"\x00"+id] = h
	}
	return out, rows.Err()
}

// UserJobStage returns the user's stage for a job ("" when they have none).
func UserJobStage(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64) (string, error) {
	var stage string
	err := pool.QueryRow(ctx, `SELECT stage FROM user_jobs WHERE user_id = $1 AND job_id = $2`, userID, jobID).Scan(&stage)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return stage, err
}

// prunedAfter is how long a closed posting keeps its per-user rule
// outcomes. Matches nobody acted on are dropped after it (a posting that
// reopens is simply filtered again); scored ones and jobs in a user's
// tracker are kept.
const prunedAfter = 30 * 24 * time.Hour

// PruneClosedMatches deletes per-user rows for postings closed longer than
// prunedAfter that were never scored or tracked, so user_jobs grows with
// open postings rather than with every posting ever seen.
func PruneClosedMatches(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	tag, err := pool.Exec(ctx, `
		DELETE FROM user_jobs uj USING jobs j
		WHERE j.id = uj.job_id AND j.closed_at < $1
		  AND uj.stage IN ('excluded', 'pending_score', 'score_failed') AND uj.analysis_id IS NULL
		  AND NOT EXISTS (SELECT 1 FROM applications a WHERE a.user_id = uj.user_id AND a.job_id = uj.job_id)`,
		time.Now().Add(-prunedAfter))
	return tag.RowsAffected(), err
}
