package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/scoring"
)

// PendingJob is a job waiting to be scored for one user. Cached is set
// when an analysis for the user's exact profile text already exists (the
// user, or someone with an identical profile, paid for it): it can be
// reused without an LLM call.
type PendingJob struct {
	Job    scoring.Job
	Cached *CachedAnalysis
}

// CachedAnalysis is a reusable score analysis.
type CachedAnalysis struct {
	ID       int64
	Analysis *scoring.Analysis // nil when the stored reply was unparseable
}

// PendingScoreJobs loads up to limit of the user's jobs in stage
// 'pending_score' (highest relevance estimate first, less for postings up
// for weeks (see queueOrder), then newest, so the fresh jobs most likely
// to fit are scored first), with the company name needed for the prompt and any
// reusable analysis for profileHash. limit <= 0 means no limit.
// onlyIDs, when not empty, restricts the result to those jobs.
func PendingScoreJobs(ctx context.Context, pool *pgxpool.Pool, userID int64, profileHash string, limit int, onlyIDs []int64) ([]PendingJob, error) {
	q := `
		SELECT j.id, c.name, j.title, COALESCE(j.location_raw, ''), j.url, ` + listedAt + `,
		       COALESCE(j.description, ''),
		       a.id, a.fit_score, a.verdict, a.seniority, a.years_required,
		       a.sponsorship, a.sponsorship_quote, a.work_mode, a.us_eligible,
		       a.location_summary, a.skills_matched, a.skills_missing, a.reason
		FROM user_jobs uj
		JOIN jobs j ON j.id = uj.job_id
		JOIN companies c ON c.id = j.company_id
		LEFT JOIN analyses a ON a.job_id = j.id AND a.kind = 'score' AND a.profile_hash = $2
		WHERE uj.user_id = $1 AND uj.stage = 'pending_score' AND j.closed_at IS NULL
		  AND (cardinality($3::bigint[]) = 0 OR j.id = ANY($3::bigint[]))
		  AND NOT EXISTS (SELECT 1 FROM user_hidden_companies h
		                  WHERE h.user_id = uj.user_id AND h.company_id = j.company_id)
		ORDER BY ` + queueOrder + `, j.posted_at DESC NULLS LAST, j.id DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	if onlyIDs == nil {
		onlyIDs = []int64{}
	}
	return queryPending(ctx, pool, q, userID, profileHash, onlyIDs)
}

// PendingWithCachedAnalysis lists the user's pending jobs that already
// have an analysis for this exact profile text under one of keys (see
// scoring.ScoreKeys; the last, the current scoring method, is preferred),
// without descriptions: they can be linked for free instead of scored. A
// score from an older method is rescored later like any stale score.
func PendingWithCachedAnalysis(ctx context.Context, pool *pgxpool.Pool, userID int64, keys []string) ([]PendingJob, error) {
	return queryPending(ctx, pool, `
		SELECT j.id, c.name, j.title, COALESCE(j.location_raw, ''), j.url, `+listedAt+`, '',
		       a.id, a.fit_score, a.verdict, a.seniority, a.years_required,
		       a.sponsorship, a.sponsorship_quote, a.work_mode, a.us_eligible,
		       a.location_summary, a.skills_matched, a.skills_missing, a.reason
		FROM user_jobs uj
		JOIN jobs j ON j.id = uj.job_id
		JOIN companies c ON c.id = j.company_id
		JOIN LATERAL (
		  SELECT * FROM analyses a
		  WHERE a.job_id = j.id AND a.kind = 'score' AND a.profile_hash = ANY($2::text[])
		  ORDER BY array_position($2::text[], a.profile_hash) DESC
		  LIMIT 1) a ON true
		WHERE uj.user_id = $1 AND uj.stage = 'pending_score' AND j.closed_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM user_hidden_companies h
		                  WHERE h.user_id = uj.user_id AND h.company_id = j.company_id)`, userID, keys)
}

// ScoredWithAnalysis lists the user's open scored jobs with the analysis
// each shows, so the post-scoring rules can be applied again after a
// preference change.
func ScoredWithAnalysis(ctx context.Context, pool *pgxpool.Pool, userID int64) ([]PendingJob, error) {
	return queryPending(ctx, pool, `
		SELECT j.id, c.name, j.title, COALESCE(j.location_raw, ''), j.url, `+listedAt+`, '',
		       a.id, a.fit_score, a.verdict, a.seniority, a.years_required,
		       a.sponsorship, a.sponsorship_quote, a.work_mode, a.us_eligible,
		       a.location_summary, a.skills_matched, a.skills_missing, a.reason
		FROM user_jobs uj
		JOIN jobs j ON j.id = uj.job_id
		JOIN companies c ON c.id = j.company_id
		JOIN analyses a ON a.id = uj.analysis_id
		WHERE uj.user_id = $1 AND uj.stage = 'scored' AND j.closed_at IS NULL`, userID)
}

// StaleScoredJobs loads up to limit of the user's scored jobs whose score
// came from an older scoring method or profile text (its analysis is not
// stored under scoreKey), best score first, with any analysis for
// scoreKey that can be reused. They keep showing their old score until
// the new one replaces it.
func StaleScoredJobs(ctx context.Context, pool *pgxpool.Pool, userID int64, scoreKey string, limit int) ([]PendingJob, error) {
	return queryPending(ctx, pool, `
		SELECT j.id, c.name, j.title, COALESCE(j.location_raw, ''), j.url, `+listedAt+`,
		       COALESCE(j.description, ''),
		       a.id, a.fit_score, a.verdict, a.seniority, a.years_required,
		       a.sponsorship, a.sponsorship_quote, a.work_mode, a.us_eligible,
		       a.location_summary, a.skills_matched, a.skills_missing, a.reason
		FROM user_jobs uj
		JOIN jobs j ON j.id = uj.job_id
		JOIN companies c ON c.id = j.company_id
		JOIN analyses old ON old.id = uj.analysis_id
		LEFT JOIN analyses a ON a.job_id = j.id AND a.kind = 'score' AND a.profile_hash = $2
		WHERE uj.user_id = $1 AND uj.stage = 'scored' AND j.closed_at IS NULL
		  AND old.profile_hash <> $2
		ORDER BY old.fit_score DESC NULLS LAST, j.id DESC
		LIMIT $3`, userID, scoreKey, limit)
}

// queueOrder orders a user's unscored matches for the AI: best estimate
// first, less the posting's age penalty (see agePenalty), so a fresh
// posting is scored before an old one that is only slightly better.
var queueOrder = "uj.estimated_score - " + agePenalty + " DESC NULLS LAST"

// queryPending runs a query selecting a job and an optional analysis (the
// columns of PendingScoreJobs) and reads the rows.
func queryPending(ctx context.Context, pool *pgxpool.Pool, q string, args ...any) ([]PendingJob, error) {
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("load pending jobs: %w", err)
	}
	defer rows.Close()

	var jobs []PendingJob
	for rows.Next() {
		var pj PendingJob
		var aid *int64
		var fit, years *int
		var verdict, seniority, sponsorship, quote, workMode, locSummary, reason *string
		var usEligible *bool
		var matched, missing []string
		if err := rows.Scan(&pj.Job.ID, &pj.Job.Company, &pj.Job.Title, &pj.Job.Location,
			&pj.Job.URL, &pj.Job.ListedAt, &pj.Job.Description,
			&aid, &fit, &verdict, &seniority, &years, &sponsorship, &quote, &workMode,
			&usEligible, &locSummary, &matched, &missing, &reason); err != nil {
			return nil, err
		}
		if aid != nil {
			pj.Cached = &CachedAnalysis{ID: *aid}
			if fit != nil {
				pj.Cached.Analysis = &scoring.Analysis{
					FitScore: *fit, Verdict: deref(verdict), Seniority: deref(seniority),
					YearsRequired: years, Sponsorship: deref(sponsorship), SponsorshipQuote: quote,
					USEligible: usEligible, WorkMode: deref(workMode), LocationSummary: deref(locSummary),
					SkillsMatched: matched, SkillsMissing: missing, Reason: deref(reason),
				}
			}
		}
		jobs = append(jobs, pj)
	}
	return jobs, rows.Err()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// BenchJobs returns jobs for the scoring benchmark from one user's
// matches: pending jobs first, then the most recent scored postings, never
// excluded/closed ones. The benchmark re-scores them without persisting,
// so nothing changes stage.
func BenchJobs(ctx context.Context, pool *pgxpool.Pool, userID int64, limit int) ([]scoring.Job, error) {
	rows, err := pool.Query(ctx, `
		SELECT j.id, c.name, j.title, COALESCE(j.location_raw, ''), j.url, `+listedAt+`,
		       COALESCE(j.description, '')
		FROM user_jobs uj
		JOIN jobs j ON j.id = uj.job_id
		JOIN companies c ON c.id = j.company_id
		WHERE uj.user_id = $1
		  AND j.closed_at IS NULL
		  AND uj.stage IN ('pending_score', 'scored')
		ORDER BY (uj.stage = 'pending_score') DESC, j.posted_at DESC NULLS LAST, j.id
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("load bench jobs: %w", err)
	}
	defer rows.Close()

	var jobs []scoring.Job
	for rows.Next() {
		var j scoring.Job
		if err := rows.Scan(&j.ID, &j.Company, &j.Title, &j.Location, &j.URL, &j.ListedAt, &j.Description); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// Link points one of a user's jobs at an existing analysis.
type Link struct {
	JobID, AnalysisID       int64
	Stage, Reason, Evidence string
}

// LinkAnalyses applies many links in one round trip (see LinkAnalysis).
func LinkAnalyses(ctx context.Context, pool *pgxpool.Pool, userID int64, links []Link) error {
	if len(links) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, l := range links {
		b.Queue(`
			UPDATE user_jobs
			SET analysis_id = $3, stage = $4, exclude_reason = $5, exclude_evidence = $6, updated_at = now()
			WHERE user_id = $1 AND job_id = $2`,
			userID, l.JobID, l.AnalysisID, l.Stage, nilIfEmpty(l.Reason), nilIfEmpty(l.Evidence))
	}
	if err := pool.SendBatch(ctx, b).Close(); err != nil {
		return fmt.Errorf("link analyses: %w", err)
	}
	return nil
}

// LinkAnalysis points a user's job at an existing analysis and sets the
// stage the user's rules give it (no LLM call, no cost).
func LinkAnalysis(ctx context.Context, pool *pgxpool.Pool, userID, jobID, analysisID int64, stage, reason, evidence string) error {
	if _, err := pool.Exec(ctx, `
		UPDATE user_jobs
		SET analysis_id = $3, stage = $4, exclude_reason = $5, exclude_evidence = $6, updated_at = now()
		WHERE user_id = $1 AND job_id = $2`,
		userID, jobID, analysisID, stage, nilIfEmpty(reason), nilIfEmpty(evidence)); err != nil {
		return fmt.Errorf("link analysis for job %d: %w", jobID, err)
	}
	return nil
}

// SaveAnalysis persists one scoring outcome for a user: it upserts the
// analyses row (by job, kind, profile hash; userID is recorded as the
// payer) and moves the user's job to its resulting stage. A score_failed
// outcome saves the raw model text in analyses.raw.
func SaveAnalysis(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64, model, profileHash string, out scoring.Outcome) error {
	a := out.Analysis
	rawText := out.Raw
	if rawText == "" {
		rawText = "null"
	}

	// raw is a JSONB column: store the model's (fence-stripped) text as a
	// JSON string so even unparseable replies are preserved.
	const q = `
		INSERT INTO analyses (job_id, kind, model, profile_hash,
		                      fit_score, verdict, seniority, years_required,
		                      sponsorship, sponsorship_quote, work_mode, us_eligible,
		                      location_summary, skills_matched, skills_missing, reason,
		                      raw, prompt_tokens, cached_tokens, completion_tokens,
		                      cost_usd, latency_ms, user_id)
		VALUES ($1, 'score', $2, $3,
		        $4, $5, $6, $7,
		        $8, $9, $10, $11,
		        $12, $13, $14, $15,
		        to_jsonb($16::text), $17, $18, $19,
		        $20, $21, $22)
		ON CONFLICT (job_id, kind, profile_hash) DO UPDATE
		SET fit_score          = EXCLUDED.fit_score,
		    verdict            = EXCLUDED.verdict,
		    seniority          = EXCLUDED.seniority,
		    years_required     = EXCLUDED.years_required,
		    sponsorship        = EXCLUDED.sponsorship,
		    sponsorship_quote  = EXCLUDED.sponsorship_quote,
		    work_mode          = EXCLUDED.work_mode,
		    us_eligible        = EXCLUDED.us_eligible,
		    location_summary   = EXCLUDED.location_summary,
		    skills_matched     = EXCLUDED.skills_matched,
		    skills_missing     = EXCLUDED.skills_missing,
		    reason             = EXCLUDED.reason,
		    raw                = EXCLUDED.raw,
		    prompt_tokens      = EXCLUDED.prompt_tokens,
		    cached_tokens      = EXCLUDED.cached_tokens,
		    completion_tokens  = EXCLUDED.completion_tokens,
		    cost_usd           = EXCLUDED.cost_usd,
		    latency_ms         = EXCLUDED.latency_ms,
		    user_id            = EXCLUDED.user_id
		RETURNING id`
	var analysisID int64
	if err := pool.QueryRow(ctx, q,
		jobID, model, profileHash,
		scoreOf(a),
		strOrNil(a, func(x *scoring.Analysis) string { return x.Verdict }),
		strOrNil(a, func(x *scoring.Analysis) string { return x.Seniority }),
		yearsOrNil(a),
		strOrNil(a, func(x *scoring.Analysis) string { return x.Sponsorship }),
		quoteOrNil(a),
		strOrNil(a, func(x *scoring.Analysis) string { return x.WorkMode }),
		boolOrNil(a),
		strOrNil(a, func(x *scoring.Analysis) string { return x.LocationSummary }),
		skillsOf(a, 0), skillsOf(a, 1),
		strOrNil(a, func(x *scoring.Analysis) string { return x.Reason }),
		rawText,
		out.Usage.PromptTokens, out.Usage.CachedTokens, out.Usage.CompletionTokens,
		out.CostUSD, out.LatencyMS, userID,
	).Scan(&analysisID); err != nil {
		return fmt.Errorf("save analysis for job %d: %w", jobID, err)
	}

	// Move the user's job to its stage; skipped outcomes leave it pending.
	if out.Stage == "" {
		return nil
	}
	return LinkAnalysis(ctx, pool, userID, jobID, analysisID, out.Stage, out.ExcludeReason, out.ExcludeEvidence)
}

// --- helpers to safely project a possibly-nil analysis ---

func scoreOf(a *scoring.Analysis) any {
	if a == nil {
		return nil
	}
	return a.FitScore
}

func strOrNil(a *scoring.Analysis, get func(*scoring.Analysis) string) any {
	if a == nil {
		return nil
	}
	v := get(a)
	if v == "" {
		return nil
	}
	return v
}

func yearsOrNil(a *scoring.Analysis) any {
	if a == nil || a.YearsRequired == nil {
		return nil
	}
	return *a.YearsRequired
}

func quoteOrNil(a *scoring.Analysis) any {
	if a == nil || a.SponsorshipQuote == nil || *a.SponsorshipQuote == "" {
		return nil
	}
	return *a.SponsorshipQuote
}

func boolOrNil(a *scoring.Analysis) any {
	if a == nil || a.USEligible == nil {
		return nil
	}
	return *a.USEligible
}

// skillsOf returns a slice as a text[] parameter: 0 = matched, 1 = missing.
func skillsOf(a *scoring.Analysis, which int) any {
	if a == nil {
		return []string{}
	}
	s := a.SkillsMatched
	if which == 1 {
		s = a.SkillsMissing
	}
	if s == nil {
		return []string{}
	}
	return s
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// RequeueScored resets open scored jobs back to pending_score for
// `wera rescore --all` (userID 0 = every user). Jobs are scored again on
// the next run unless an analysis for the user's current profile text
// already exists.
func RequeueScored(ctx context.Context, pool *pgxpool.Pool, userID int64) (int64, error) {
	tag, err := pool.Exec(ctx, `
		UPDATE user_jobs uj SET stage = 'pending_score', updated_at = now()
		FROM jobs j
		WHERE j.id = uj.job_id AND j.closed_at IS NULL
		  AND uj.stage IN ('scored','score_failed')
		  AND ($1 = 0 OR uj.user_id = $1)`, userID)
	if err != nil {
		return 0, fmt.Errorf("requeue scored jobs: %w", err)
	}
	return tag.RowsAffected(), nil
}

// RequeueForRefilter has the next filter pass re-apply roles.yaml to every
// open job (userID 0 = every user), for `wera refilter --all`, and returns
// how many users it marked. No LLM cost: scored jobs that still pass keep
// their scores.
func RequeueForRefilter(ctx context.Context, pool *pgxpool.Pool, userID int64) (int64, error) {
	tag, err := pool.Exec(ctx, `
		UPDATE profiles SET filtered_preferences = NULL
		WHERE ($1 = 0 OR user_id = $1)`, userID)
	if err != nil {
		return 0, fmt.Errorf("requeue jobs for refilter: %w", err)
	}
	return tag.RowsAffected(), nil
}
