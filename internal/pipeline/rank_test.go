package pipeline

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/config"
	"wera/internal/scoring"
	"wera/internal/store"
	"wera/internal/testutil"
)

// fakeEmbed is an embeddings endpoint: the profile points one way and
// each job title gets a fixed angle to it, so the ranking is known.
type fakeEmbed struct {
	calls atomic.Int64
	texts atomic.Int64
}

func (f *fakeEmbed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Input []string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	f.calls.Add(1)
	f.texts.Add(int64(len(req.Input)))
	type item struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	}
	data := make([]item, len(req.Input))
	for i, t := range req.Input {
		v := []float32{0, 1}
		switch {
		case strings.HasPrefix(t, "## Target roles"):
			v = []float32{1, 0}
		case strings.HasPrefix(t, "Platform Engineer, Zeta Cloud."):
			v = []float32{0.9, 0.44}
		case strings.HasPrefix(t, "Site Reliability Engineer, Zeta Cloud."):
			v = []float32{0.5, 0.87}
		}
		data[i] = item{Index: i, Embedding: v}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// TestScoreLimitScoresBestRankedFirst checks the cost controls end to end:
// every pending job gets shared facts and an embedding, but only the
// best-ranked job within the daily limit gets an AI fit score; the rest
// stay pending with an estimate.
func TestScoreLimitScoresBestRankedFirst(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	coral, embedder := &fakeCoral{}, &fakeEmbed{}
	csrv, esrv := httptest.NewServer(coral), httptest.NewServer(embedder)
	t.Cleanup(csrv.Close)
	t.Cleanup(esrv.Close)
	env := testEnv(csrv.URL)
	// A model name and profile text of its own keep embeddings and cached
	// scores from earlier runs (shared rows on seed jobs) out of this one.
	env.EmbedBaseURL, env.EmbedModel = esrv.URL, testutil.Unique("embed")
	env.ScoresPerDay, env.ScoresPerRun, env.InactiveAfterDays = 1, 0, 7
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	companyID, _ := testutil.Company(t, pool, "ai_ml")
	ids := testutil.Jobs(t, pool, companyID,
		testutil.Job{Title: "Site Reliability Engineer, Zeta Cloud", Location: "Remote, US", Description: "Keep Go services healthy."},
		testutil.Job{Title: "Platform Engineer, Zeta Cloud", Location: "Remote, US", Description: "Build the platform."},
	)
	sre, platform := ids[0], ids[1]

	u := testutil.User(t, pool, false)
	prof := testutil.Profile(t, pool, u.ID, sreProfile+"\n"+testutil.Unique("note"), srePrefs)
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM analyses WHERE profile_hash = $1`, prof.ProfileHash)
		pool.Exec(context.Background(), `DELETE FROM job_embeddings WHERE model = $1`, env.EmbedModel)
	})
	if _, err := FilterUser(ctx, pool, testEngine(t), NewEmbedder(env, log), prof, nil); err != nil {
		t.Fatalf("filter: %v", err)
	}
	for _, id := range ids {
		if stage, reason := stageOf(t, pool, u.ID, id); stage != "pending_score" {
			t.Fatalf("job %d should be pending, got %s/%s", id, stage, reason)
		}
	}
	// The test database may hold other open jobs this profile matches.
	var pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_jobs WHERE user_id = $1 AND stage = 'pending_score'`, u.ID).Scan(&pending); err != nil {
		t.Fatal(err)
	}

	st, err := ScoreUser(ctx, pool, env, log, prof, 0, nil, 1, nil)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if coral.fits.Load() != 1 || st.Scored != 1 || st.Deferred != pending-1 {
		t.Fatalf("want 1 fit call and %d deferred, got %d fits, stats %+v", pending-1, coral.fits.Load(), st)
	}
	if stage, _ := stageOf(t, pool, u.ID, platform); stage != "scored" {
		t.Errorf("the best-ranked job (Platform Engineer, Zeta Cloud) should be scored, got %s", stage)
	}
	if stage, _ := stageOf(t, pool, u.ID, sre); stage != "pending_score" {
		t.Errorf("a lower-ranked job should stay pending, got %s", stage)
	}
	var estPlatform, estSRE int
	if err := pool.QueryRow(ctx, `SELECT estimated_score FROM user_jobs WHERE user_id = $1 AND job_id = $2`, u.ID, sre).Scan(&estSRE); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT estimated_score FROM user_jobs WHERE user_id = $1 AND job_id = $2`, u.ID, platform).Scan(&estPlatform); err != nil {
		t.Fatal(err)
	}
	if estPlatform <= estSRE {
		t.Errorf("embedding estimates: platform %d should beat SRE %d", estPlatform, estSRE)
	}
	var embedded int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM user_jobs uj JOIN job_embeddings e ON e.job_id = uj.job_id
		WHERE uj.user_id = $1 AND uj.stage = 'pending_score'`, u.ID).Scan(&embedded); err != nil {
		t.Fatal(err)
	}
	if embedded != pending-1 {
		t.Errorf("want every pending job embedded, got %d of %d", embedded, pending-1)
	}

	// The daily limit is used up: another run scores nothing, and embeds
	// nothing new (postings and the profile are embedded once).
	texts := embedder.texts.Load()
	st, err = ScoreUser(ctx, pool, env, log, prof, 0, nil, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if coral.fits.Load() != 1 || st.Scored != 0 {
		t.Errorf("limit reached, yet %d fit calls (stats %+v)", coral.fits.Load(), st)
	}
	if embedder.texts.Load() != texts {
		t.Errorf("re-embedded %d texts on the second run", embedder.texts.Load()-texts)
	}

	// The run's backfill finds nothing new to embed for these jobs.
	before := embedder.texts.Load()
	if _, err := NewEmbedder(env, log).Backfill(ctx, pool, time.Minute); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	var left int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs j JOIN job_facts f ON f.job_id = j.id AND f.content_hash = j.content_hash
		LEFT JOIN job_embeddings e ON e.job_id = j.id AND e.model = $1
		WHERE j.closed_at IS NULL AND e.job_id IS NULL`, env.EmbedModel).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("backfill left %d open jobs with facts unembedded (embedded %d texts)", left, embedder.texts.Load()-before)
	}

	// Opening a job still scores it right away, whatever the limit.
	if _, err := ScoreUser(ctx, pool, env, log, prof, 1, []int64{sre}, 1, nil); err != nil {
		t.Fatal(err)
	}
	if stage, _ := stageOf(t, pool, u.ID, sre); stage != "scored" {
		t.Errorf("opened job should be scored despite the limit, got %s", stage)
	}
}

func TestScoreLimit(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	u := testutil.User(t, pool, false)
	prof := testutil.Profile(t, pool, u.ID, sreProfile, srePrefs)
	env := &config.Env{ScoresPerDay: 100, ScoresPerRun: 30, InactiveAfterDays: 7, InactiveScoresPerDay: 10}

	check := func(name string, want int) {
		t.Helper()
		got, why, err := ScoreLimit(ctx, pool, env, prof)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: limit %d (%s), want %d", name, got, why, want)
		}
	}
	// New profile, no scores yet: the whole day's allowance at once.
	check("new profile", 100)

	// Once the profile has scores, at most the per-run limit.
	companyID, _ := testutil.Company(t, pool, "ai_ml")
	jobs := testutil.Jobs(t, pool, companyID, testutil.Job{Title: "Platform Engineer", Location: "Remote, US"})
	if _, err := pool.Exec(ctx, `
		INSERT INTO analyses (job_id, kind, model, profile_hash, raw, user_id, created_at)
		SELECT $1, 'score', 'm', $2, 'null', $3, now() - g * interval '1 minute' FROM generate_series(1, 1) g`,
		jobs[0], scoring.ScoreKey(prof.ProfileHash), u.ID); err != nil {
		t.Fatal(err)
	}
	check("scored profile", 30)

	// Close to the daily limit: what is left of it.
	env.ScoresPerDay = 20
	check("near the daily limit", 19)
	env.ScoresPerDay = 100

	// Inactive users: none without a digest to fill, digest-sized with one.
	if _, err := pool.Exec(ctx, `UPDATE users SET last_seen_at = now() - interval '30 days' WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	check("inactive, email off", 0)
	env.Mail.Provider = "log"
	check("inactive, address not confirmed", 0)
	if ok, err := store.MarkEmailVerified(ctx, pool, u.ID, u.Email); err != nil || !ok {
		t.Fatalf("confirm: %v %v", ok, err)
	}
	check("inactive, digest on", 9)
	if err := store.SaveSettings(ctx, pool, u.ID, func() store.Settings {
		s := store.DefaultSettings()
		s.Notifications.EmailDigest = false
		return s
	}()); err != nil {
		t.Fatal(err)
	}
	check("inactive, digest off", 0)

	// No limits configured: unlimited.
	if _, err := pool.Exec(ctx, `UPDATE users SET last_seen_at = now() WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	env.ScoresPerDay, env.ScoresPerRun = 0, 0
	check("no limits", -1)
}

// TestLearnedFollowsTheUsersScores checks the learning from a user's scores
// end to end: the AI liked jobs pointing one way and not the other, so a new
// job pointing the good way is estimated higher, once there are enough scores.
func TestLearnedFollowsTheUsersScores(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	model := testutil.Unique("embed")
	e := &Embedder{Model: model, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	companyID, _ := testutil.Company(t, pool, "ai_ml")
	const count = 12
	jobs := make([]testutil.Job, count)
	for i := range jobs {
		jobs[i] = testutil.Job{Title: testutil.Unique("Engineer"), Location: "Remote, US", Description: "Build."}
	}
	ids := testutil.Jobs(t, pool, companyID, jobs...)
	u := testutil.User(t, pool, false)
	prof := testutil.Profile(t, pool, u.ID, sreProfile+"\n"+testutil.Unique("note"), srePrefs)
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM analyses WHERE user_id = $1`, u.ID)
		pool.Exec(context.Background(), `DELETE FROM job_embeddings WHERE model = $1`, model)
	})

	good, bad := []float32{1, 0}, []float32{0, 1}
	vecs := map[int64][]float32{}
	for i, id := range ids {
		v, score := good, 90
		if i%2 == 1 {
			v, score = bad, 20
		}
		vecs[id] = v
		if i < minLearnedScores-1 {
			mustScore(t, pool, u.ID, id, scoring.ScoreKey(prof.ProfileHash), score)
		}
	}
	if err := store.SaveJobEmbeddings(ctx, pool, e.jobKey(), vecs); err != nil {
		t.Fatal(err)
	}
	fresh := map[int64][]float32{1: good, 2: bad}
	if got, n, err := e.Learned(ctx, pool, prof, fresh); err != nil || got != nil || n != 0 {
		t.Fatalf("too few scores: %v, %d, %v", got, n, err)
	}
	for i, id := range ids[minLearnedScores-1:] {
		score := 90
		if (i+minLearnedScores-1)%2 == 1 {
			score = 20
		}
		mustScore(t, pool, u.ID, id, prof.ProfileHash, score) // an older method's key counts too
	}
	got, n, err := e.Learned(ctx, pool, prof, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if n != count || !(got[1] > got[2]+40) {
		t.Errorf("learned %v from %d scores", got, n)
	}
}

func mustScore(t *testing.T, pool *pgxpool.Pool, userID, jobID int64, key string, score int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO analyses (job_id, kind, model, profile_hash, fit_score, raw, user_id)
		VALUES ($1, 'score', 'test', $2, $3, '{}', $4)`, jobID, key, score, userID); err != nil {
		t.Fatal(err)
	}
}
