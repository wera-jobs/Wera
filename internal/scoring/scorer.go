package scoring

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Job stages set by the scorer.
const (
	StageScored   = "scored"
	StageExcluded = "excluded"
	StageFailed   = "score_failed"
	StageSkipped  = "" // budget reached or call error; job stays pending_score
)

// Outcome is the result of scoring one job.
type Outcome struct {
	JobID           int64
	Stage           string // scored | excluded | score_failed | "" (skipped)
	ExcludeReason   string
	ExcludeEvidence string
	Analysis        *Analysis
	Raw             string // raw model text (fence-stripped)
	Usage           UsageStats
	CostUSD         float64
	LatencyMS       int64
	Err             error
}

// maxCallFailures ends a batch after this many calls in a row fail: the
// provider or the network is down, the jobs stay pending for the next run,
// and thousands more calls would only sit waiting for timeouts.
const maxCallFailures = 12

// retryNote is the extra message appended when the model returns invalid
// JSON, per PLAN.md section 7.4.
const retryNote = "Your previous reply was not valid JSON for the schema. Return only the JSON object."

// Scorer runs the scoring stage over a batch of pending jobs with
// bounded concurrency, a per-run cost budget, and post-LLM exclusions.
type Scorer struct {
	Client      *Client
	Profile     []byte // profile.md, verbatim
	ProfileHash string
	Model       string
	Rules       Exclusions // the user's post-LLM exclusion rules
	MaxCostUSD  float64    // budget guard; 0 disables
	Concurrency int        // SCORING_CONCURRENCY
	Log         *slog.Logger

	// Facts holds shared job facts by job id. A job with facts is scored
	// with the compact fit prompt (profile + job card, ~10x fewer
	// uncached tokens); one without falls back to the full posting.
	Facts map[int64]*Facts

	// OnOutcome, when set, is called with each outcome as soon as it is
	// ready (from several goroutines at once), so callers can persist and
	// show scores while the rest of the batch is still running.
	OnOutcome func(Outcome)

	// Mutable run state.
	skillsOnce sync.Once
	skills     SkillSet     // profile terms, for matching job skills
	limit      atomic.Int32 // current effective concurrency
	inUse      atomic.Int32
	failStreak atomic.Int32 // calls in a row that failed
	costMu     sync.Mutex
	spent      float64
	budgetHit  bool
}

// Score scores every job with up to Concurrency parallel calls, starting
// them in input order (so callers put the most promising jobs first).
// Jobs left unscored because the budget was reached have Stage
// StageSkipped and stay pending for the next run. The returned slice is
// in input order.
func (s *Scorer) Score(ctx context.Context, jobs []Job) []Outcome {
	if s.Concurrency < 1 {
		s.Concurrency = 1
	}
	s.limit.Store(int32(s.Concurrency))
	if s.Client != nil {
		s.Client.OnRateLimit = s.halveConcurrency
	}

	outcomes := make([]Outcome, len(jobs))
	var wg sync.WaitGroup
	for i := range jobs {
		if !s.reserve(ctx) {
			// Budget reached or canceled: the rest stay pending.
			for k := i; k < len(jobs); k++ {
				outcomes[k] = Outcome{JobID: jobs[k].ID, Stage: StageSkipped}
				s.emit(outcomes[k])
			}
			break
		}
		if i == 0 && len(jobs) > 1 {
			// The first call runs alone: it writes the profile to the
			// provider's prompt cache, so the parallel calls after it read
			// the cache instead of each paying to write it again.
			outcomes[0] = s.scoreOne(ctx, jobs[0])
			s.release()
			s.noteCall(outcomes[0])
			s.emit(outcomes[0])
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer s.release()
			outcomes[i] = s.scoreOne(ctx, jobs[i])
			s.noteCall(outcomes[i])
			s.emit(outcomes[i])
		}(i)
	}
	wg.Wait()
	return outcomes
}

// noteCall keeps count of calls that failed in a row.
func (s *Scorer) noteCall(o Outcome) {
	if o.Err == nil {
		s.failStreak.Store(0)
		return
	}
	if s.failStreak.Add(1) == maxCallFailures && s.Log != nil {
		s.Log.Warn("AI calls keep failing; the rest stay pending for the next run", "err", o.Err)
	}
}

func (s *Scorer) emit(o Outcome) {
	if s.OnOutcome != nil {
		s.OnOutcome(o)
	}
}

// scoreOne scores a single job end to end; the caller holds a slot.
func (s *Scorer) scoreOne(ctx context.Context, j Job) Outcome {
	out := Outcome{JobID: j.ID}

	if f := s.Facts[j.ID]; f != nil {
		return s.scoreCompact(ctx, j, f)
	}
	messages := BuildMessages(s.Profile, j)
	start := time.Now()
	comp, err := s.Client.Chat(ctx, CacheKey(s.ProfileHash), messages)
	if err != nil {
		// A transport/API error leaves the job pending for the next run.
		out.Stage = StageSkipped
		out.Err = err
		if s.Log != nil {
			s.Log.Warn("scoring call failed; job stays pending", "job_id", j.ID, "err", err)
		}
		return out
	}
	out.LatencyMS = time.Since(start).Milliseconds()
	s.charge(&out, comp)

	analysis, raw := s.parseWithRetry(ctx, &out, messages, comp.Content)
	out.Raw = raw
	if analysis == nil {
		out.Stage = StageFailed // raw text is saved with the analysis row
		return out
	}
	out.Analysis = analysis

	reason, evidence := PostLLMExclusion(analysis, s.Rules)
	if reason != "" {
		out.Stage = StageExcluded
		out.ExcludeReason = reason
		out.ExcludeEvidence = evidence
	} else {
		out.Stage = StageScored
	}
	return out
}

// parseWithRetry parses the reply; on a schema failure it retries once
// with the retry note appended, charging the retry to out. It returns the
// analysis (nil on final failure) and the raw text to persist.
func (s *Scorer) parseWithRetry(ctx context.Context, out *Outcome, messages []Message, content string) (*Analysis, string) {
	a, err := ParseAnalysis(content)
	if err == nil {
		return a, StripFences(content)
	}
	if s.Log != nil {
		s.Log.Warn("invalid scoring JSON, retrying once", "err", err)
	}
	comp, cerr := s.Client.Chat(ctx, CacheKey(s.ProfileHash),
		append(append([]Message{}, messages...), Message{Role: "user", Content: retryNote}))
	if cerr != nil {
		return nil, content
	}
	s.charge(out, comp)
	a, err = ParseAnalysis(comp.Content)
	if err != nil {
		return nil, comp.Content
	}
	return a, StripFences(comp.Content)
}

// Exclusions are one user's post-LLM exclusion rules, derived from their
// preferences.
type Exclusions struct {
	MaxYears           int  // drop jobs requiring more years; 0 disables
	RequireSponsorship bool // drop explicit refusals (with a quote)
	USOnly             bool // drop jobs the model says are not US-eligible
	AllowSenior        bool // keep jobs the model calls senior
}

// PostLLMExclusion applies the post-LLM exclusion rules from PLAN.md
// section 7.4 for one user, returning the exclude reason and evidence, or
// "" when the job should stay scored. Rules in order: sponsorship refusal,
// years ceiling, US ineligibility, senior seniority.
func PostLLMExclusion(a *Analysis, r Exclusions) (reason, evidence string) {
	switch {
	case r.RequireSponsorship && a.Sponsorship == "no" && a.SponsorshipQuote != nil && *a.SponsorshipQuote != "":
		return "llm:sponsorship_no", *a.SponsorshipQuote
	case r.MaxYears > 0 && a.YearsRequired != nil && *a.YearsRequired > r.MaxYears:
		return "llm:years>" + strconv.Itoa(r.MaxYears), a.Reason
	case r.USOnly && a.USEligible != nil && !*a.USEligible:
		return "llm:non_us", a.LocationSummary
	case !r.AllowSenior && a.Seniority == "senior":
		return "llm:senior", a.Reason
	}
	return "", ""
}

// reserve takes a concurrency slot, respecting the (possibly halved)
// dynamic limit and the budget. It returns false when the run must stop.
func (s *Scorer) reserve(ctx context.Context) bool {
	for {
		if s.budgetReached() || s.failStreak.Load() >= maxCallFailures {
			return false
		}
		lim := s.limit.Load()
		use := s.inUse.Load()
		if use < lim && s.inUse.CompareAndSwap(use, use+1) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// release returns a concurrency slot.
func (s *Scorer) release() { s.inUse.Add(-1) }

// halveConcurrency drops the effective concurrency for the rest of the
// run after a 429, never below 1.
func (s *Scorer) halveConcurrency() {
	for {
		cur := s.limit.Load()
		next := cur / 2
		if next < 1 {
			next = 1
		}
		if s.limit.CompareAndSwap(cur, next) {
			if s.Log != nil {
				s.Log.Warn("429 seen; halving scoring concurrency", "new_limit", next)
			}
			return
		}
	}
}

// addCost accumulates spend under the budget guard.
func (s *Scorer) addCost(c float64) {
	s.costMu.Lock()
	defer s.costMu.Unlock()
	s.spent += c
	if s.MaxCostUSD > 0 && s.spent >= s.MaxCostUSD {
		s.budgetHit = true
	}
}

// budgetReached reports whether the per-run cost budget is exhausted.
func (s *Scorer) budgetReached() bool {
	s.costMu.Lock()
	defer s.costMu.Unlock()
	return s.budgetHit
}

// Spent returns the accumulated cost of this scoring run.
func (s *Scorer) Spent() float64 {
	s.costMu.Lock()
	defer s.costMu.Unlock()
	return s.spent
}

// ErrNoProfile is returned when the profile file is missing or empty.
var ErrNoProfile = errors.New("candidate profile is missing or empty")

// scoreCompact scores a job from its shared facts: the fit prompt sees the
// profile and a short job card, and the result merges facts and fit.
func (s *Scorer) scoreCompact(ctx context.Context, j Job, f *Facts) Outcome {
	out := Outcome{JobID: j.ID}
	messages := BuildFitMessages(s.Profile, f.Card(j))
	start := time.Now()
	comp, err := s.Client.Chat(ctx, CacheKey(s.ProfileHash), messages)
	if err != nil {
		out.Stage, out.Err = StageSkipped, err
		return out
	}
	out.LatencyMS = time.Since(start).Milliseconds()
	s.charge(&out, comp)

	fit, err := ParseFit(comp.Content)
	raw := StripFences(comp.Content)
	if err != nil {
		retry, cerr := s.Client.Chat(ctx, CacheKey(s.ProfileHash),
			append(append([]Message{}, messages...), Message{Role: "user", Content: retryNote}))
		if cerr == nil {
			s.charge(&out, retry)
			raw = StripFences(retry.Content)
			fit, err = ParseFit(retry.Content)
		}
	}
	out.Raw = raw
	if err != nil {
		out.Stage = StageFailed
		return out
	}
	if len(fit.SkillsMatched) == 0 && len(fit.SkillsMissing) == 0 {
		s.skillsOnce.Do(func() { s.skills = NewSkillSet(string(s.Profile)) })
		fit.SkillsMatched, fit.SkillsMissing = MatchSkills(s.skills, f.SkillsRequired)
	}
	fit.Finish()
	out.Analysis = Merge(f, fit)
	if reason, evidence := PostLLMExclusion(out.Analysis, s.Rules); reason != "" {
		out.Stage, out.ExcludeReason, out.ExcludeEvidence = StageExcluded, reason, evidence
	} else {
		out.Stage = StageScored
	}
	return out
}

// charge adds one call's tokens and cost to the outcome and the run's budget.
func (s *Scorer) charge(out *Outcome, c *Completion) {
	cost := callCost(s.Model, c)
	out.Usage.PromptTokens += c.Usage.PromptTokens
	out.Usage.CachedTokens += c.Usage.CachedTokens
	out.Usage.CompletionTokens += c.Usage.CompletionTokens
	out.CostUSD += cost
	s.addCost(cost)
}
