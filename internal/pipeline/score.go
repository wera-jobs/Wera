package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/config"
	"wera/internal/filter"
	"wera/internal/metrics"
	"wera/internal/scoring"
	"wera/internal/store"
)

// FilterStats summarizes one user's rule-filter pass.
type FilterStats struct {
	Excluded int
	Pending  int
}

// FilterUser applies the rule engine with the user's preferences to every
// open job they have no current outcome for, then ranks what is left (emb
// may be nil: TF-IDF ranking only).
func FilterUser(ctx context.Context, pool *pgxpool.Pool, eng *filter.Engine, emb *Embedder, prof *store.Profile, m *metrics.Registry) (*FilterStats, error) {
	out, err := store.FilterForUser(ctx, pool, eng, prof.UserID, &prof.Preferences)
	if err != nil {
		return nil, fmt.Errorf("filter for user %d: %w", prof.UserID, err)
	}
	if m != nil {
		for reason, n := range out.ByReason {
			m.JobsExcluded.WithLabelValues(reason).Add(float64(n))
		}
	}
	rules := ExclusionsFor(&prof.Preferences)
	byScore := 0
	if out.Refiltered {
		// Scored jobs kept their scores; the new preferences may rule some out.
		if byScore, err = recheckScored(ctx, pool, prof.UserID, rules); err != nil {
			return nil, err
		}
	}
	// Jobs this exact profile text was already scored for (before a
	// refilter, a profile save that kept the text, or under an older
	// scoring method) get their score back now, for free, so lists never
	// show them as unscored.
	pendingCached, err := store.PendingWithCachedAnalysis(ctx, pool, prof.UserID, scoring.ScoreKeys(prof.ProfileHash))
	if err != nil {
		return nil, err
	}
	links := make([]store.Link, len(pendingCached))
	for i, pj := range pendingCached {
		links[i] = linkFor(pj, rules)
	}
	if err := store.LinkAnalyses(ctx, pool, prof.UserID, links); err != nil {
		return nil, err
	}
	byFacts, err := excludeByKnownFacts(ctx, pool, prof)
	if err != nil {
		return nil, err
	}
	if out.Refiltered {
		if err := store.MarkFiltered(ctx, pool, prof.UserID, &prof.Preferences); err != nil {
			return nil, err
		}
	}
	if _, err := RankPending(ctx, pool, emb, prof); err != nil {
		return nil, err
	}
	return &FilterStats{Excluded: out.Excluded + byScore + byFacts, Pending: max(0, out.Pending-len(pendingCached)-byFacts)}, nil
}

// Prefilter applies the rule filter for a user whose profile text is not
// written yet (setup, once they chose what they want), so saving the
// profile has only the ranking left. It takes the user's lock and does
// nothing when someone else holds it. With no text there is nothing to
// rank or score, and no scored job to check again.
func Prefilter(ctx context.Context, pool *pgxpool.Pool, eng *filter.Engine, userID int64) error {
	got, release, err := store.TryUserLock(ctx, pool, userID)
	if err != nil || !got {
		return err
	}
	defer release(context.Background())
	prof, err := store.GetProfile(ctx, pool, userID)
	if err != nil || prof == nil || prof.Markdown != "" || len(prof.Preferences.RoleFamilies) == 0 {
		return err
	}
	out, err := store.FilterForUser(ctx, pool, eng, userID, &prof.Preferences)
	if err != nil {
		return fmt.Errorf("prefilter for user %d: %w", userID, err)
	}
	if out.Refiltered {
		return store.MarkFiltered(ctx, pool, userID, &prof.Preferences)
	}
	return nil
}

// recheckScored applies the post-scoring rules to the user's scored jobs
// and excludes the ones they now rule out (fewer years accepted, say). It
// returns how many it excluded.
func recheckScored(ctx context.Context, pool *pgxpool.Pool, userID int64, rules scoring.Exclusions) (int, error) {
	scored, err := store.ScoredWithAnalysis(ctx, pool, userID)
	if err != nil {
		return 0, err
	}
	var ex []store.Exclusion
	for _, pj := range scored {
		if pj.Cached.Analysis == nil {
			continue
		}
		if reason, evidence := scoring.PostLLMExclusion(pj.Cached.Analysis, rules); reason != "" {
			ex = append(ex, store.Exclusion{JobID: pj.Job.ID, Reason: reason, Evidence: evidence})
		}
	}
	return len(ex), store.ExcludeUserJobs(ctx, pool, userID, ex)
}

// excludeByKnownFacts applies the user's rules to unscored matches whose
// shared facts already exist (extracted for another user): jobs asking
// for too many years, refusing sponsorship, outside the US or too senior
// drop out at filter time, before they are ranked or shown, at no cost.
func excludeByKnownFacts(ctx context.Context, pool *pgxpool.Pool, prof *store.Profile) (int, error) {
	ids, err := store.PendingJobIDs(ctx, pool, prof.UserID)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	facts, err := store.FactsFor(ctx, pool, ids)
	if err != nil {
		return 0, err
	}
	rules := ExclusionsFor(&prof.Preferences)
	var ex []store.Exclusion
	for id, f := range facts {
		if reason, evidence := scoring.PostLLMExclusion(scoring.Merge(f, nil), rules); reason != "" {
			ex = append(ex, store.Exclusion{JobID: id, Reason: reason, Evidence: evidence})
		}
	}
	return len(ex), store.ExcludeUserJobs(ctx, pool, prof.UserID, ex)
}

// linkFor links one of the user's jobs to an existing analysis, with the
// stage their post-scoring rules give it (an unparseable stored reply
// counts as a failed score).
func linkFor(pj store.PendingJob, rules scoring.Exclusions) store.Link {
	l := store.Link{JobID: pj.Job.ID, AnalysisID: pj.Cached.ID, Stage: scoring.StageFailed}
	if pj.Cached.Analysis != nil {
		l.Stage = scoring.StageScored
		if l.Reason, l.Evidence = scoring.PostLLMExclusion(pj.Cached.Analysis, rules); l.Reason != "" {
			l.Stage = scoring.StageExcluded
		}
	}
	return l
}

// ExclusionsFor maps a user's preferences to their post-LLM rules.
func ExclusionsFor(p *config.Preferences) scoring.Exclusions {
	return scoring.Exclusions{
		MaxYears:           p.MaxYearsRequired,
		RequireSponsorship: p.NeedsSponsorship,
		USOnly:             p.USOnly,
		AllowSenior:        p.AcceptsLevel("senior") || p.AcceptsLevel("management"),
	}
}

// staleBatch is how many jobs scored by an older scoring method one run
// scores again (best first; the run's cost budget still applies).
const staleBatch = 300

// ScoreStats summarizes one scoring pass.
type ScoreStats struct {
	Scored           int
	Excluded         int
	Failed           int // score_failed (invalid JSON twice)
	Skipped          int // left pending: budget guard or call error
	Reused           int // linked to an existing analysis, no LLM call
	FactsExtracted   int // postings whose shared facts were extracted now
	ExcludedByFacts  int // excluded from shared facts, no per-user LLM call
	Deferred         int // ranked below the user's score limit: estimate only for now
	QuickGraded      int // given a quick grade (many jobs per call, no reason)
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
	CostUSD          float64

	quickSpent float64
}

// Add accumulates another pass's numbers.
func (s *ScoreStats) Add(o *ScoreStats) {
	if o == nil {
		return
	}
	s.Scored += o.Scored
	s.Excluded += o.Excluded
	s.Failed += o.Failed
	s.Skipped += o.Skipped
	s.Reused += o.Reused
	s.FactsExtracted += o.FactsExtracted
	s.ExcludedByFacts += o.ExcludedByFacts
	s.Deferred += o.Deferred
	s.QuickGraded += o.QuickGraded
	s.PromptTokens += o.PromptTokens
	s.CachedTokens += o.CachedTokens
	s.CompletionTokens += o.CompletionTokens
	s.CostUSD += o.CostUSD
}

// ScoreUser scores up to limit of one user's pending jobs (0 = all) and
// stores their analyses, spending at most maxCostUSD. Jobs that already
// have an analysis for the user's exact profile text reuse it for free.
// It returns (nil, nil) when AI_API_KEY is not set, so the pipeline
// simply skips the LLM stage.
// onlyIDs, when not empty, scores just those jobs (on-demand scoring of
// a job the user opened).
func ScoreUser(ctx context.Context, pool *pgxpool.Pool, env *config.Env, log *slog.Logger, prof *store.Profile, limit int, onlyIDs []int64, maxCostUSD float64, m *metrics.Registry) (*ScoreStats, error) {
	if !env.AI.Enabled() {
		log.Info("AI_API_KEY not set; skipping scoring stage")
		return nil, nil
	}
	if prof.Markdown == "" {
		return nil, scoring.ErrNoProfile
	}
	key := scoring.ScoreKey(prof.ProfileHash)
	pending, err := store.PendingScoreJobs(ctx, pool, prof.UserID, key, limit, onlyIDs)
	if err != nil {
		return nil, err
	}
	// Jobs scored by an older scoring method keep their score until this
	// run replaces it (they are not held to the user's score limit).
	var stalePending []store.PendingJob
	if len(onlyIDs) == 0 {
		if stalePending, err = store.StaleScoredJobs(ctx, pool, prof.UserID, key, staleBatch); err != nil {
			return nil, err
		}
	}
	st := &ScoreStats{}
	rules := ExclusionsFor(&prof.Preferences)

	// Reuse analyses that already exist for this profile text.
	var jobs, stale []scoring.Job
	isStale := map[int64]bool{}
	var links []store.Link
	for i, pj := range append(pending, stalePending...) {
		if pj.Cached == nil {
			if i >= len(pending) {
				stale = append(stale, pj.Job)
				isStale[pj.Job.ID] = true
			} else {
				jobs = append(jobs, pj.Job)
			}
			continue
		}
		links = append(links, linkFor(pj, rules))
	}
	if err := store.LinkAnalyses(ctx, pool, prof.UserID, links); err != nil {
		return st, err
	}
	st.Reused = len(links)
	if len(jobs) == 0 && len(stale) == 0 {
		log.Info("no pending jobs to score", "user_id", prof.UserID, "reused", st.Reused)
		return st, nil
	}

	opts := env.AI.Options(env.AI.Model)
	opts.Log = log
	client := scoring.NewClient(opts)
	scorer := &scoring.Scorer{
		Client:      client,
		Profile:     []byte(prof.Markdown),
		ProfileHash: prof.ProfileHash, // the provider's prompt cache key
		Model:       env.AI.Model,
		Rules:       rules,
		MaxCostUSD:  maxCostUSD,
		Concurrency: env.ScoringConcurrency,
		Log:         log,
	}
	log.Info("scoring jobs", "user_id", prof.UserID, "jobs", len(jobs), "reused", st.Reused,
		"model", env.AI.Model, "concurrency", env.ScoringConcurrency,
		"max_cost_usd", fmt.Sprintf("%.4f", maxCostUSD))

	// Each outcome is saved as soon as it arrives, so the dashboard shows
	// scores while the rest of the batch is still running.
	var mu sync.Mutex
	scorer.OnOutcome = func(out scoring.Outcome) {
		if m != nil {
			m.LLMTokens.WithLabelValues("prompt").Add(float64(out.Usage.PromptTokens))
			m.LLMTokens.WithLabelValues("cached").Add(float64(out.Usage.CachedTokens))
			m.LLMTokens.WithLabelValues("completion").Add(float64(out.Usage.CompletionTokens))
			m.LLMCost.Add(out.CostUSD)
			m.LLMLatency.Observe(float64(out.LatencyMS) / 1000)
			result := out.Stage
			if result == "" {
				result = "skipped"
			}
			m.LLMRequests.WithLabelValues(env.AI.Model, result).Inc()
		}
		// A rescore that failed keeps the job's old score.
		if out.Stage != "" && (!isStale[out.JobID] || out.Stage != scoring.StageFailed) {
			if err := store.SaveAnalysis(ctx, pool, prof.UserID, out.JobID, env.AI.Model, key, out); err != nil {
				log.Error("saving analysis failed", "job_id", out.JobID, "err", err)
			} else {
				log.Debug("job scored", "user_id", prof.UserID, "job_id", out.JobID,
					"stage", out.Stage, "fit_score", scoreInt(out.Analysis),
					"tokens", out.Usage.PromptTokens, "cached", out.Usage.CachedTokens,
					"cost", fmt.Sprintf("%.6f", out.CostUSD), "ms", out.LatencyMS)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		st.PromptTokens += out.Usage.PromptTokens
		st.CachedTokens += out.Usage.CachedTokens
		st.CompletionTokens += out.Usage.CompletionTokens
		switch out.Stage {
		case scoring.StageScored:
			st.Scored++
		case scoring.StageExcluded:
			st.Excluded++
		case scoring.StageFailed:
			st.Failed++
		default:
			st.Skipped++
			if out.Err != nil {
				log.Warn("job skipped after error", "job_id", out.JobID, "err", out.Err)
			}
		}
	}
	extractor := &scoring.Extractor{
		Client:      client,
		Model:       env.AI.Model,
		Concurrency: env.ScoringConcurrency,
		Log:         log,
		OnResult: func(r scoring.FactsResult) {
			if r.Usage.PromptTokens > 0 {
				if err := store.RecordLLMUsage(ctx, pool, prof.UserID, "facts", env.AI.Model,
					r.Usage.PromptTokens, r.Usage.CachedTokens, r.Usage.CompletionTokens, r.CostUSD); err != nil {
					log.Warn("recording facts usage failed", "err", err)
				}
				mu.Lock()
				st.FactsExtracted++
				st.PromptTokens += r.Usage.PromptTokens
				st.CachedTokens += r.Usage.CachedTokens
				st.CompletionTokens += r.Usage.CompletionTokens
				mu.Unlock()
			}
			if r.Facts != nil {
				if err := store.SaveFacts(ctx, pool, r, env.AI.Model); err != nil {
					log.Warn("saving facts failed", "job_id", r.JobID, "err", err)
				}
			}
		},
	}

	// Work through the queue (best estimates first) in small chunks:
	// 1. Shared facts (extracted once per posting, for everyone), best
	// ranked first. They may use at most half of the allowance, so a new
	// user with thousands of unseen postings still gets scores this run.
	all := append(append([]scoring.Job{}, jobs...), stale...)
	ids := make([]int64, len(all))
	for i, j := range all {
		ids[i] = j.ID
	}
	facts, err := store.FactsFor(ctx, pool, ids)
	if err != nil {
		return st, err
	}
	var missing []scoring.Job
	for _, j := range all {
		if facts[j.ID] == nil {
			missing = append(missing, j)
		}
	}
	factsBudget := maxCostUSD / 2
	if len(onlyIDs) > 0 {
		factsBudget = maxCostUSD
	}
	const chunk = 48
	for start := 0; start < len(missing) && ctx.Err() == nil && extractor.Spent() < factsBudget; start += chunk {
		extractor.MaxCostUSD = factsBudget
		for _, r := range extractor.Extract(ctx, missing[start:min(start+chunk, len(missing))]) {
			if r.Facts != nil {
				facts[r.JobID] = r.Facts
			}
		}
	}

	// 2. The user's rules on those facts, at no cost.
	var candidates, rescore []scoring.Job
	var ex []store.Exclusion
	for _, j := range all {
		if f := facts[j.ID]; f != nil {
			if reason, evidence := scoring.PostLLMExclusion(scoring.Merge(f, nil), rules); reason != "" {
				ex = append(ex, store.Exclusion{JobID: j.ID, Reason: reason, Evidence: evidence})
				continue
			}
		}
		if isStale[j.ID] {
			rescore = append(rescore, j)
		} else {
			candidates = append(candidates, j)
		}
	}
	if err := store.ExcludeUserJobs(ctx, pool, prof.UserID, ex); err != nil {
		log.Warn("excluding jobs failed", "user_id", prof.UserID, "err", err)
	} else {
		st.ExcludedByFacts = len(ex)
	}

	// 3. Quick grades for every candidate not graded yet, best estimate
	// first: many jobs per call, so the whole queue is ranked by the AI
	// within minutes, not only the jobs the daily limit lets through. They
	// may use half of what the facts left.
	if len(onlyIDs) == 0 && len(candidates) > 0 {
		n, why, err := ScoreLimit(ctx, pool, env, prof)
		if err != nil {
			return st, err
		}
		if n != 0 || why != noDigestToFill {
			budget := (maxCostUSD - extractor.Spent()) / 2
			if st.QuickGraded, err = quickGrade(ctx, pool, env, log, prof, candidates, facts, budget, st, &mu); err != nil {
				return st, err
			}
		}

		// 4. Rank what is left (embeddings now cover the new facts, quick
		// grades the rest) and keep the best within the user's limit. The
		// rest keep their estimate and get a full score when the user opens
		// them or on a later run.
		if est, err := RankPending(ctx, pool, NewEmbedder(env, log), prof); err != nil {
			log.Warn("ranking before scoring failed; keeping the previous order", "user_id", prof.UserID, "err", err)
		} else {
			sort.SliceStable(candidates, func(a, b int) bool { return est[candidates[a].ID] > est[candidates[b].ID] })
		}
		if n >= 0 && n < len(candidates) {
			st.Deferred = len(candidates) - n
			candidates = candidates[:n]
			log.Info("score limit reached; the rest stay estimated", "user_id", prof.UserID,
				"scoring", n, "deferred", st.Deferred, "why", why)
		}
	}

	// 5. Per-user AI fit scores: new jobs first, then rescores.
	candidates = append(candidates, rescore...)
	if len(candidates) > 0 && ctx.Err() == nil {
		scorer.Facts = facts
		scorer.MaxCostUSD = maxCostUSD - extractor.Spent() - st.quickSpent
		if scorer.MaxCostUSD > 0 {
			scorer.Score(ctx, candidates)
		} else {
			st.Skipped += len(candidates)
		}
	}
	st.CostUSD = scorer.Spent() + extractor.Spent() + st.quickSpent

	log.Info("scoring pass finished", "user_id", prof.UserID,
		"scored", st.Scored, "excluded", st.Excluded, "score_failed", st.Failed,
		"skipped", st.Skipped, "reused", st.Reused,
		"facts_extracted", st.FactsExtracted, "excluded_by_facts", st.ExcludedByFacts,
		"quick_graded", st.QuickGraded, "deferred", st.Deferred,
		"prompt_tokens", st.PromptTokens, "cached_tokens", st.CachedTokens,
		"completion_tokens", st.CompletionTokens, "cost_usd", fmt.Sprintf("%.6f", st.CostUSD))
	return st, nil
}

// noDigestToFill is ScoreLimit's reason for a user who gets no scores at
// all: inactive, with no digest to fill.
const noDigestToFill = "inactive, no digest to fill"

// quickGrade gives quick grades to the candidates (in queue order) that
// have facts and no grade for the current profile text, spending at most
// budget, and records the cost for the user. It returns how many it graded.
func quickGrade(ctx context.Context, pool *pgxpool.Pool, env *config.Env, log *slog.Logger, prof *store.Profile,
	candidates []scoring.Job, facts map[int64]*scoring.Facts, budget float64, st *ScoreStats, mu *sync.Mutex) (int, error) {
	if budget <= 0 {
		return 0, nil
	}
	key := scoring.ScoreKey(prof.ProfileHash)
	have, err := store.QuickGrades(ctx, pool, prof.UserID, key)
	if err != nil {
		return 0, err
	}
	var todo []scoring.QuickJob
	for _, j := range candidates {
		if f := facts[j.ID]; f != nil {
			if _, ok := have[j.ID]; !ok {
				todo = append(todo, scoring.QuickJob{Job: j, Facts: f})
			}
		}
	}
	if len(todo) == 0 {
		return 0, nil
	}
	opts := env.AI.Options(env.AI.Model)
	opts.MaxTokens, opts.Log = 800, log
	graded := 0
	g := &scoring.QuickGrader{
		Client: scoring.NewClient(opts), Profile: []byte(prof.Markdown), ProfileHash: prof.ProfileHash,
		Model: env.AI.Model, MaxCostUSD: budget, Concurrency: env.ScoringConcurrency,
		OnBatch: func(grades []scoring.QuickGrade, u scoring.UsageStats, cost float64) {
			if err := store.RecordLLMUsage(ctx, pool, prof.UserID, "quick", env.AI.Model,
				u.PromptTokens, u.CachedTokens, u.CompletionTokens, cost); err != nil {
				log.Warn("recording quick grade usage failed", "err", err)
			}
			if err := store.SaveQuickGrades(ctx, pool, prof.UserID, key, grades); err != nil {
				log.Warn("saving quick grades failed", "user_id", prof.UserID, "err", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			graded += len(grades)
			st.PromptTokens += u.PromptTokens
			st.CachedTokens += u.CachedTokens
			st.CompletionTokens += u.CompletionTokens
		},
	}
	g.Grade(ctx, todo)
	mu.Lock()
	defer mu.Unlock()
	st.quickSpent = g.Spent()
	return graded, nil
}

func scoreInt(a *scoring.Analysis) any {
	if a == nil {
		return nil
	}
	return a.FitScore
}

// ScoreLimit is how many AI scores the user may get now: -1 for no limit.
// Active users get SCORES_PER_DAY in any 24 hours, at most SCORES_PER_RUN
// per run once their current profile has scores (a new or edited profile
// gets its first day's worth at once). Users unseen for
// INACTIVE_AFTER_DAYS get INACTIVE_SCORES_PER_DAY, enough for their
// digest, or none when no digest would be sent.
func ScoreLimit(ctx context.Context, pool *pgxpool.Pool, env *config.Env, prof *store.Profile) (int, string, error) {
	act, err := store.UserScoreActivity(ctx, pool, prof.UserID, scoring.ScoreKey(prof.ProfileHash))
	if err != nil {
		return 0, "", err
	}
	inactive := env.InactiveAfterDays > 0 &&
		(act.LastSeen == nil || time.Since(*act.LastSeen) > time.Duration(env.InactiveAfterDays)*24*time.Hour)
	daily, why := env.ScoresPerDay, "daily limit"
	switch {
	case inactive && (!env.Mail.Enabled() || !act.DigestRequested):
		return 0, noDigestToFill, nil
	case inactive:
		daily, why = env.InactiveScoresPerDay, "inactive: digest-sized limit"
	case daily == 0 && env.ScoresPerRun == 0:
		return -1, "", nil
	}
	left := -1
	if daily > 0 || inactive {
		left = max(0, daily-act.ScoredLastDay)
	}
	if env.ScoresPerRun > 0 && act.ScoredProfile && !inactive && (left < 0 || env.ScoresPerRun < left) {
		left, why = env.ScoresPerRun, "per-run limit"
	}
	return left, why, nil
}
