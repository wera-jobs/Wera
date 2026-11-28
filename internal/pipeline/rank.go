package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/config"
	"wera/internal/embed"
	"wera/internal/relevance"
	"wera/internal/scoring"
	"wera/internal/store"
)

// embedChunk is how many postings are embedded and saved at a time, so a
// cut-short pass (a request deadline) keeps the work it finished.
const embedChunk = 256

// Embedder ranks jobs by embedding similarity. A nil *Embedder means no
// embedding endpoint is configured and ranking uses TF-IDF alone.
type Embedder struct {
	Client *embed.Client
	Model  string
	// QueryPrefix goes before the profile text and DocPrefix before each
	// posting (see embed.Prefixes).
	QueryPrefix, DocPrefix string
	Log                    *slog.Logger
}

// NewEmbedder returns the configured embedder, or nil.
func NewEmbedder(env *config.Env, log *slog.Logger) *Embedder {
	c := embed.New(env.EmbedBaseURL, env.EmbedModel, env.EmbedAPIKey)
	if c == nil {
		return nil
	}
	q, d := embed.Prefixes(env.EmbedModel)
	if env.EmbedQueryPrefix != nil {
		q = *env.EmbedQueryPrefix
	}
	if env.EmbedDocPrefix != nil {
		d = *env.EmbedDocPrefix
	}
	return &Embedder{Client: c, Model: env.EmbedModel, QueryPrefix: q, DocPrefix: d, Log: log}
}

// profileKey and jobKey name stored vectors: the model, plus a mark of
// the prefix when there is one, so changing a prefix re-embeds instead of
// comparing vectors made two ways.
func (e *Embedder) profileKey() string { return keyed(e.Model, "q", e.QueryPrefix) }
func (e *Embedder) jobKey() string     { return keyed(e.Model, "d", e.DocPrefix) }

func keyed(model, kind, prefix string) string {
	if prefix == "" {
		return model
	}
	sum := sha256.Sum256([]byte(prefix))
	return model + "#" + kind + hex.EncodeToString(sum[:4])
}

// RankPending re-ranks the user's unscored matches against their profile
// without an AI call and stores the estimates (35..85, or the job's quick
// grade when it has one). The estimates order
// the AI scoring queue, decide which jobs get an AI score under the daily
// limit, and preview jobs on the dashboard. It returns them unrounded, so
// the best jobs keep their order: many share each whole number.
//
// Jobs with shared facts are ranked by Blend (see there) and by what the AI's
// scores so far say about this user (see Learned); the rest (new postings
// whose facts are not extracted yet) are ranked on the posting itself (see
// bareOrder), and that order decides which postings get facts first.
func RankPending(ctx context.Context, pool *pgxpool.Pool, e *Embedder, prof *store.Profile) (map[int64]float64, error) {
	docs, hasFacts, err := store.RankDocs(ctx, pool, prof.UserID)
	if err != nil {
		return nil, err
	}
	// Jobs without facts are ranked on the posting itself (see bareOrder);
	// the blend below ranks the rest.
	bare := make([]relevance.Doc, 0, len(docs)-len(hasFacts))
	for _, d := range docs {
		if !hasFacts[d.ID] {
			bare = append(bare, d)
		}
	}
	order := bareOrder(ctx, pool, e, prof, bare)
	est := relevance.Scale(order)
	exact := relevance.ScaleExact(order)
	if len(docs) > len(bare) {
		ids := make([]int64, len(docs))
		titles := make(map[int64]string, len(docs))
		for i, d := range docs {
			ids[i] = d.ID
			titles[d.ID] = d.Title
		}
		facts, err := store.FactsFor(ctx, pool, ids)
		if err != nil {
			return nil, err
		}
		var sims map[int64]float64
		var vecs map[int64][]float32
		if e != nil {
			if sims, vecs, err = e.Similarities(ctx, pool, prof, ids); err != nil {
				e.Log.Warn("embedding ranking unavailable; ranking by words and skills", "user_id", prof.UserID, "err", err)
			}
		}
		blend := Blend(prof.Markdown, prof.Preferences, titles, facts, sims)
		if e != nil && len(vecs) > 0 {
			learned, n, err := e.Learned(ctx, pool, prof, vecs)
			if err != nil {
				e.Log.Warn("ranking without the user's scores", "user_id", prof.UserID, "err", err)
			}
			blend = withLearned(blend, learned, n)
		}
		for id, v := range relevance.ScaleExact(blend) {
			exact[id] = v
		}
		for id, v := range relevance.Scale(blend) {
			est[id] = v
		}
	}
	// The AI's quick grades beat every local signal (rank correlation with
	// full scores 0.80 to 0.88 on four users, against 0.05 to 0.73), so a
	// graded job is estimated by its grade.
	quick, err := store.QuickGrades(ctx, pool, prof.UserID, scoring.ScoreKey(prof.ProfileHash))
	if err != nil {
		return nil, err
	}
	for id, g := range quick {
		if _, ok := est[id]; ok {
			est[id], exact[id] = g, float64(g)
		}
	}
	return exact, store.SetEstimates(ctx, pool, prof.UserID, est)
}

// Ranking postings before their facts exist (#173). Measured on five
// users' newest 250 AI scores each (scratch copy of the live data, Nov 27):
// TF-IDF on the posting alone, the previous order, agreed with the AI at
// Spearman 0.41 on average and put 64% of the good (65+) jobs in its top
// third; adding the embedding of the posting's opening and what the
// user's own scores say about it (the same ridge as Learned, which carries
// over from facts vectors) gave 0.54 and 72%. With facts: 0.72 and 79%.
const (
	bareWeightWords   = 1.0
	bareWeightExcerpt = 2.0
	// excerptRunes is how much of the posting is embedded after its title.
	excerptRunes = 1500
	// maxExcerptsPerRank caps the postings embedded in one ranking, best by
	// words first, so a queue of thousands without facts never stalls a
	// run; later rankings embed the next ones. Each posting is embedded once
	// for everyone.
	maxExcerptsPerRank = embedChunk
)

// excerptKey names excerpt vectors apart from facts vectors: a posting's
// row is replaced by its facts vector once the facts exist.
func (e *Embedder) excerptKey() string { return e.jobKey() + "#x" }

// ExcerptText is what a posting without facts is embedded as: its title
// and the opening of its text.
func ExcerptText(title, body string) string {
	b := strings.Join(strings.Fields(body), " ")
	if r := []rune(b); len(r) > excerptRunes {
		b = string(r[:excerptRunes])
	}
	return title + ". " + b
}

// bareOrder orders the jobs without facts (higher is better): TF-IDF on
// the posting, plus, with an embedder, the profile's similarity to the
// posting's opening and the learned estimate from the user's scores. A job
// not embedded yet counts as average on those.
func bareOrder(ctx context.Context, pool *pgxpool.Pool, e *Embedder, prof *store.Profile, bare []relevance.Doc) map[int64]float64 {
	if len(bare) == 0 {
		return map[int64]float64{}
	}
	words := relevance.Standardize(relevance.Similarities(embed.ProfileText(prof.Markdown), bare))
	out := make(map[int64]float64, len(bare))
	for id, v := range words {
		out[id] = bareWeightWords * v
	}
	if e == nil {
		return out
	}
	vecs, pv, err := e.excerptVectors(ctx, pool, prof, bare, words)
	if err != nil {
		e.Log.Warn("embedding postings without facts failed; ranking them by words", "user_id", prof.UserID, "err", err)
	}
	if len(vecs) == 0 || pv == nil {
		return out
	}
	sims := make(map[int64]float64, len(vecs))
	for id, v := range vecs {
		sims[id] = embed.Cosine(pv, v)
	}
	for id, v := range relevance.Standardize(sims) {
		out[id] += bareWeightExcerpt * v
	}
	learned, n, err := e.Learned(ctx, pool, prof, vecs)
	if err != nil {
		e.Log.Warn("ranking postings without facts without the user's scores", "user_id", prof.UserID, "err", err)
	}
	if n > 0 {
		w := learnedWeight(n)
		for id, v := range relevance.Standardize(learned) {
			out[id] += w * v
		}
	}
	return out
}

// excerptVectors returns the excerpt embeddings of the bare jobs and the
// profile's, embedding up to maxExcerptsPerRank missing ones (best by
// words first).
func (e *Embedder) excerptVectors(ctx context.Context, pool *pgxpool.Pool, prof *store.Profile, bare []relevance.Doc, words map[int64]float64) (map[int64][]float32, []float32, error) {
	pv, err := e.profileVector(ctx, pool, prof)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]int64, len(bare))
	for i, d := range bare {
		ids[i] = d.ID
	}
	vecs, err := store.JobEmbeddings(ctx, pool, ids, e.excerptKey())
	if err != nil {
		return nil, pv, err
	}
	var todo []relevance.Doc
	for _, d := range bare {
		if vecs[d.ID] == nil {
			todo = append(todo, d)
		}
	}
	sort.SliceStable(todo, func(a, b int) bool { return words[todo[a].ID] > words[todo[b].ID] })
	todo = todo[:min(len(todo), maxExcerptsPerRank)]
	if len(todo) == 0 {
		return vecs, pv, nil
	}
	texts := make([]string, len(todo))
	for i, d := range todo {
		texts[i] = e.DocPrefix + ExcerptText(d.Title, d.Body)
	}
	got, err := e.Client.Embed(ctx, texts)
	if err != nil {
		return vecs, pv, fmt.Errorf("embed postings: %w", err)
	}
	batch := make(map[int64][]float32, len(todo))
	for i, d := range todo {
		batch[d.ID] = got[i]
		vecs[d.ID] = got[i]
	}
	return vecs, pv, store.SaveJobEmbeddings(ctx, pool, e.excerptKey(), batch)
}

// Blend weights. Each signal is standardized over the user's candidates
// first, so the weights compare like with like. They follow what the fit score
// weighs, and were fit to 27,000 AI fit scores of seven users, holding each
// user out in turn, then rounded. Against those scores this order agrees at
// Spearman 0.77 (the embedding alone: 0.66).
const (
	weightEmbedding = 2.0
	weightWords     = 1.0
	weightSkills    = 1.0 // every word of a required skill is in the profile
	weightCoverage  = 1.0 // any specific word is, which is what the fit score measures
	weightNearLimit = 1.0 // asks close to the most years the user accepts
	weightLevelGap  = 0.5 // levels away from those the user accepts
	weightRemote    = 0.5
	// penaltyBlocker is subtracted when a posting needs a clearance or
	// citizenship the user does not show: the fit score cuts those by 60%.
	penaltyBlocker = 1.5
	// penaltyTooManyYears is subtracted when a posting asks for more
	// years than the user accepts: the AI scores those far lower.
	penaltyTooManyYears = 4.0
)

// needsClearance finds a posting's demand for a clearance or citizenship in
// its digest and skills. "Clearance" alone also means medical clearance.
var needsClearance = regexp.MustCompile(`(?i)\b(security|secret|government|dod|sci|public trust)[- ]+clearance\b|` +
	`\bclearable\b|\bts/sci\b|\bu\.?s\.? citizen(ship)?\b|must be a citizen|\bitar\b`)

// blocked reports whether a posting demands a clearance or citizenship.
func blocked(f *scoring.Facts) bool {
	return needsClearance.MatchString(f.Digest + " " + strings.Join(f.SkillsRequired, " "))
}

// showsClearance finds a profile that could meet such a demand. A user who
// needs sponsorship cannot, whatever their profile says about it.
var showsClearance = regexp.MustCompile(`(?i)clearance|citizen`)

// levelOrder places the levels of postings and preferences on one line.
var levelOrder = map[string]int{"internship": 0, "entry": 1, "junior": 2, "mid": 3, "senior": 4, "management": 5}

// levelGap is how many levels a posting is from the nearest one the user
// accepts. An unknown level counts as one step: neither a match nor far off.
func levelGap(prefs config.Preferences, seniority string) float64 {
	at, ok := levelOrder[seniority]
	if !ok {
		return 1
	}
	gap := -1
	for _, l := range prefs.Levels {
		if o, ok := levelOrder[l]; ok && (gap < 0 || abs(o-at) < gap) {
			gap = abs(o - at)
		}
	}
	if gap < 0 {
		return 1
	}
	return float64(gap)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// FactsText is what a posting's facts read as for word matching and
// embedding: seniority, required skills and the digest.
func FactsText(f *scoring.Facts) string {
	return fmt.Sprintf("Seniority: %s. Skills: %s. %s", f.Seniority, strings.Join(f.SkillsRequired, ", "), f.Digest)
}

// Blend scores the jobs that have facts on what the fit score weighs:
// embedding similarity (sims; a job missing from it counts as average),
// TF-IDF similarity of the facts to the profile's roles and skills, how much
// of the required skills the profile shows, closeness to the years limit, the
// level, remote work, and penalties for a blocker or too many years. Higher
// is better; the values only order jobs.
func Blend(markdown string, prefs config.Preferences, titles map[int64]string, facts map[int64]*scoring.Facts, sims map[int64]float64) map[int64]float64 {
	if len(facts) == 0 {
		return nil
	}
	profile := embed.ProfileText(markdown)
	have, skillSet := relevance.TokenSet(profile), scoring.NewSkillSet(markdown)
	canClear := !prefs.NeedsSponsorship && showsClearance.MatchString(markdown)
	n := len(facts)
	docs := make([]relevance.Doc, 0, n)
	skills, coverage := make(map[int64]float64, n), make(map[int64]float64, n)
	near, gaps, remote := make(map[int64]float64, n), make(map[int64]float64, n), make(map[int64]float64, n)
	for id, f := range facts {
		docs = append(docs, relevance.Doc{ID: id, Title: titles[id], Body: FactsText(f)})
		skills[id] = relevance.SkillOverlap(have, f.SkillsRequired)
		if c := scoring.Coverage(scoring.MatchSkills(skillSet, f.SkillsRequired)); c >= 0 {
			coverage[id] = c
		}
		near[id] = 0
		if prefs.MaxYearsRequired > 0 && f.YearsRequired != nil {
			near[id] = float64(max(0, *f.YearsRequired-prefs.MaxYearsRequired+3))
		}
		gaps[id] = levelGap(prefs, f.Seniority)
		remote[id] = 0
		if f.WorkMode == "remote" {
			remote[id] = 1
		}
	}
	words := relevance.Standardize(relevance.Similarities(profile, docs))
	skills = relevance.Standardize(skills)
	coverage = relevance.Standardize(coverage)
	near, gaps, remote = relevance.Standardize(near), relevance.Standardize(gaps), relevance.Standardize(remote)
	emb := map[int64]float64{}
	if len(sims) > 0 {
		known := make(map[int64]float64, len(sims))
		for id := range facts {
			if v, ok := sims[id]; ok {
				known[id] = v
			}
		}
		emb = relevance.Standardize(known)
	}
	out := make(map[int64]float64, n)
	for id, f := range facts {
		v := weightEmbedding*emb[id] + weightWords*words[id] + weightSkills*skills[id] + weightCoverage*coverage[id] -
			weightNearLimit*near[id] - weightLevelGap*gaps[id] + weightRemote*remote[id]
		if !canClear && blocked(f) {
			v -= penaltyBlocker
		}
		if prefs.MaxYearsRequired > 0 && f.YearsRequired != nil && *f.YearsRequired > prefs.MaxYearsRequired {
			v -= penaltyTooManyYears
		}
		out[id] = v
	}
	return out
}

// Similarities returns the profile's similarity to each job that has
// facts, and those jobs' embeddings, embedding the profile and any postings
// not embedded yet. On an error it still returns what it could compute.
func (e *Embedder) Similarities(ctx context.Context, pool *pgxpool.Pool, prof *store.Profile, jobIDs []int64) (map[int64]float64, map[int64][]float32, error) {
	pv, err := e.profileVector(ctx, pool, prof)
	if err != nil {
		return nil, nil, err
	}
	_, embedErr := e.EmbedJobs(ctx, pool, jobIDs)
	vecs, err := store.JobEmbeddings(ctx, pool, jobIDs, e.jobKey())
	if err != nil {
		return nil, nil, err
	}
	sims := make(map[int64]float64, len(vecs))
	for id, v := range vecs {
		sims[id] = embed.Cosine(pv, v)
	}
	return sims, vecs, embedErr
}

// profileVector returns the profile's embedding, embedding it the first
// time for this text.
func (e *Embedder) profileVector(ctx context.Context, pool *pgxpool.Pool, prof *store.Profile) ([]float32, error) {
	pv, err := store.ProfileEmbedding(ctx, pool, prof.UserID, prof.ProfileHash, e.profileKey())
	if err != nil || pv != nil {
		return pv, err
	}
	vecs, err := e.Client.Embed(ctx, []string{e.QueryPrefix + embed.ProfileText(prof.Markdown)})
	if err != nil {
		return nil, fmt.Errorf("embed profile: %w", err)
	}
	pv = vecs[0]
	return pv, store.SaveProfileEmbedding(ctx, pool, prof.UserID, prof.ProfileHash, e.profileKey(), pv)
}

// Learning from a user's scores. Measured on seven users' real queues: of the
// 40 best pending jobs per user, the AI rated 63% good going by the blend
// alone and 69% with this.
const (
	minLearnedScores = 10   // fewer say nothing yet
	maxLearnedScores = 3000 // more add nothing and take longer to fit
	learnedLambda    = 1.0  // results barely change from 0.3 to 3
)

// Learned estimates each job from the AI's own scores for this user so far:
// a ridge regression over the embeddings of the jobs it scored. Every score
// says what this user's fit looks like (a domain, a kind of team, a pattern
// of blockers) in ways a profile does not spell out, and the AI's call is
// what the ranking is trying to foretell. It returns the estimates and how
// many scores they rest on: nil and 0 while there are too few.
func (e *Embedder) Learned(ctx context.Context, pool *pgxpool.Pool, prof *store.Profile, vecs map[int64][]float32) (map[int64]float64, int, error) {
	scored, err := store.ScoredVectors(ctx, pool, prof.UserID, scoring.ScoreKeys(prof.ProfileHash), e.jobKey(), maxLearnedScores)
	if err != nil {
		return nil, 0, err
	}
	if len(scored) < minLearnedScores {
		return nil, 0, nil
	}
	x, y := make([][]float32, len(scored)), make([]float64, len(scored))
	for i, s := range scored {
		x[i], y[i] = s.Vector, s.Score
	}
	model := embed.FitRidge(x, y, learnedLambda)
	if model == nil {
		return nil, 0, nil
	}
	out := make(map[int64]float64, len(vecs))
	for id, v := range vecs {
		out[id] = model.Predict(v)
	}
	return out, len(scored), nil
}

// learnedWeight is how much estimates resting on n scores count against the
// blend. It tracks the best weight measured from 10 scores (0.1) to 3,000
// (1.9): early scores add a little, and later the user's own scores outweigh
// everything the profile says.
func learnedWeight(n int) float64 {
	return 2 * float64(n) / float64(n+200)
}

// withLearned mixes learned estimates into a blend, both standardized over
// the blend's jobs. A job with no estimate counts as average.
func withLearned(blend, learned map[int64]float64, n int) map[int64]float64 {
	if len(learned) == 0 {
		return blend
	}
	known := make(map[int64]float64, len(blend))
	for id := range blend {
		if v, ok := learned[id]; ok {
			known[id] = v
		}
	}
	out := relevance.Standardize(blend)
	z := relevance.Standardize(known)
	w := learnedWeight(n)
	for id := range out {
		out[id] += w * z[id]
	}
	return out
}

// EmbedJobs embeds those of the jobs that have facts but no current
// embedding, and returns how many it embedded.
func (e *Embedder) EmbedJobs(ctx context.Context, pool *pgxpool.Pool, jobIDs []int64) (int, error) {
	todo, err := store.JobsToEmbed(ctx, pool, jobIDs, e.jobKey())
	if err != nil {
		return 0, err
	}
	done := 0
	for start := 0; start < len(todo); start += embedChunk {
		part := todo[start:min(start+embedChunk, len(todo))]
		texts := make([]string, len(part))
		for i, c := range part {
			texts[i] = e.DocPrefix + embed.JobText(c.Title, c.Facts.Seniority, c.Facts.SkillsRequired, c.Facts.Digest)
		}
		vecs, err := e.Client.Embed(ctx, texts)
		if err != nil {
			return done, fmt.Errorf("embed jobs: %w", err)
		}
		batch := make(map[int64][]float32, len(part))
		for i, c := range part {
			batch[c.ID] = vecs[i]
		}
		if err := store.SaveJobEmbeddings(ctx, pool, e.jobKey(), batch); err != nil {
			return done, err
		}
		done += len(part)
	}
	return done, nil
}

// Backfill embeds open postings that have facts but no embedding, for at
// most maxTime, so ranking a user rarely waits on embeddings (after a
// deploy, or when the model changes).
func (e *Embedder) Backfill(ctx context.Context, pool *pgxpool.Pool, maxTime time.Duration) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, maxTime)
	defer cancel()
	ids, err := store.OpenJobsWithFacts(ctx, pool)
	if err != nil {
		return 0, err
	}
	n, err := e.EmbedJobs(ctx, pool, ids)
	if ctx.Err() != nil {
		return n, nil // out of time: the next run continues
	}
	return n, err
}
