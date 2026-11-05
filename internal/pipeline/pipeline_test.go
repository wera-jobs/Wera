package pipeline

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/config"
	"wera/internal/filter"
	"wera/internal/scoring"
	"wera/internal/store"
	"wera/internal/testutil"
)

// fakeCoral answers facts and fit calls like the inference API would and
// counts each kind. A posting mentioning "8+ years" gets 8 required years
// in its facts; every fit's ratings score 75.
type fakeCoral struct {
	facts, fits, quick atomic.Int64
	// quickBest, when set, turns on quick grades: high for the job with
	// this title, middling for the rest. Unset, quick calls fail and jobs
	// keep their local estimates.
	quickBest string
}

func (f *fakeCoral) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages []struct{ Role, Content string } `json:"messages"`
	}
	body, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(body, &req); err != nil || len(req.Messages) == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	system, last := req.Messages[0].Content, req.Messages[len(req.Messages)-1].Content
	var content string
	switch {
	case strings.HasPrefix(system, "You extract facts"):
		f.facts.Add(1)
		years := 1
		if strings.Contains(last, "8+ years") {
			years = 8
		}
		content = `{"seniority":"junior","years_required":` + strconv.Itoa(years) + `,"sponsorship":"unknown",` +
			`"sponsorship_quote":null,"us_eligible":true,"work_mode":"remote","location_summary":"Remote, US",` +
			`"skills_required":["Go","Kubernetes"],"digest":"Runs production infrastructure."}`
	case strings.HasPrefix(system, "You grade how well each") && f.quickBest != "":
		// Quick grades: 9s for the job titled quickBest, 5s for the rest.
		f.quick.Add(1)
		var grades []string
		for _, line := range strings.Split(last, "\n") {
			switch {
			case strings.Contains(line, f.quickBest):
				grades = append(grades, "[9,9,9,9,9,0]")
			case strings.Contains(line, " at "):
				grades = append(grades, "[5,5,5,5,5,0]")
			}
		}
		content = `{"grades":[` + strings.Join(grades, ",") + `]}`
	case strings.HasPrefix(system, "You score how well"):
		f.fits.Add(1)
		content = `{"ratings":{"role":8,"seniority":8,"skills":7,"domain":6,"location":7,"blocker":false},"skills_matched":["Go"],"skills_missing":[],"reason":"Matches the target role."}`
	default:
		http.Error(w, "unexpected prompt", http.StatusBadRequest)
		return
	}
	reply, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": content}}},
		"usage": map[string]any{"prompt_tokens": 900, "completion_tokens": 80,
			"prompt_tokens_details": map[string]int{"cached_tokens": 600}},
	})
	w.Header().Set("Content-Type", "application/json")
	w.Write(reply)
}

func testEngine(t *testing.T) *filter.Engine {
	t.Helper()
	roles, err := config.LoadRoles("../../config/roles.yaml")
	if err != nil {
		t.Fatal(err)
	}
	eng, err := filter.New(roles)
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

func stageOf(t *testing.T, pool *pgxpool.Pool, userID, jobID int64) (stage, reason string) {
	t.Helper()
	var r *string
	if err := pool.QueryRow(context.Background(),
		`SELECT stage, exclude_reason FROM user_jobs WHERE user_id = $1 AND job_id = $2`, userID, jobID).Scan(&stage, &r); err != nil {
		t.Fatalf("user_jobs row for job %d: %v", jobID, err)
	}
	if r != nil {
		reason = *r
	}
	return stage, reason
}

func testEnv(url string) *config.Env {
	return &config.Env{
		AI:                 config.AI{APIKey: "cb_test", BaseURL: url, Model: "deepseek-v4.1-flash-fast"},
		ScoringConcurrency: 2,
	}
}

var srePrefs = config.Preferences{
	RoleFamilies: []string{"sre", "platform"}, Levels: []string{"entry", "mid"},
	MaxYearsRequired: 3, USOnly: true, NeedsSponsorship: true,
}

const sreProfile = "# Candidate Profile\n## Target roles\nSite reliability and platform engineering, entry level.\n## Technical skills\nGo, Kubernetes, Terraform, Prometheus."

// TestMatchingEndToEnd runs the rule filter, local ranking, shared facts,
// fact-based exclusions and fit scoring for two users with a fake LLM, and
// checks that the second user reuses the first user's facts.
func TestMatchingEndToEnd(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	coral := &fakeCoral{}
	srv := httptest.NewServer(coral)
	t.Cleanup(srv.Close)
	env := testEnv(srv.URL)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := testEngine(t)

	companyID, _ := testutil.Company(t, pool, "ai_ml")
	ids := testutil.Jobs(t, pool, companyID,
		testutil.Job{Title: "Site Reliability Engineer", Location: "Remote, US", Description: "Keep our Go services healthy with Kubernetes."},
		testutil.Job{Title: "Senior Site Reliability Engineer", Location: "Remote, US", Description: "Lead reliability."},
		testutil.Job{Title: "Site Reliability Engineer", Location: "London, United Kingdom", Description: "Join our London team."},
		testutil.Job{Title: "Platform Engineer", Location: "Austin, TX", Description: "We are unable to sponsor visas for this position."},
		testutil.Job{Title: "Platform Engineer, Infrastructure", Location: "Seattle, WA", Description: "You have 8+ years of experience running Kubernetes."},
		testutil.Job{Title: "Accountant", Location: "Remote, US", Description: "Close the books."},
	)
	good, senior, uk, noSponsor, tooSenior, offTopic := ids[0], ids[1], ids[2], ids[3], ids[4], ids[5]

	u1 := testutil.User(t, pool, false)
	p1 := testutil.Profile(t, pool, u1.ID, sreProfile, srePrefs)
	if _, err := FilterUser(ctx, pool, eng, nil, p1, nil); err != nil {
		t.Fatalf("filter: %v", err)
	}
	for id, want := range map[int64]string{senior: "title:", uk: "location:non_us", noSponsor: "sponsorship:explicit_no", offTopic: "title:no_category"} {
		if stage, reason := stageOf(t, pool, u1.ID, id); stage != "excluded" || !strings.HasPrefix(reason, want) {
			t.Errorf("job %d: got %s/%s, want excluded/%s", id, stage, reason, want)
		}
	}
	for _, id := range []int64{good, tooSenior} {
		if stage, _ := stageOf(t, pool, u1.ID, id); stage != "pending_score" {
			t.Errorf("job %d: got %s, want pending_score", id, stage)
		}
	}
	var est *int
	pool.QueryRow(ctx, `SELECT estimated_score FROM user_jobs WHERE user_id = $1 AND job_id = $2`, u1.ID, good).Scan(&est)
	if est == nil {
		t.Error("pending match has no local estimate")
	}

	st, err := ScoreUser(ctx, pool, env, log, p1, 0, []int64{good, tooSenior}, 1, nil)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if st.FactsExtracted != 2 || st.ExcludedByFacts != 1 || st.Scored != 1 {
		t.Errorf("stats: %+v", st)
	}
	if stage, reason := stageOf(t, pool, u1.ID, tooSenior); stage != "excluded" || !strings.Contains(reason, "years") {
		t.Errorf("8+ years job: %s/%s", stage, reason)
	}
	if stage, _ := stageOf(t, pool, u1.ID, good); stage != "scored" {
		t.Errorf("good job: %s", stage)
	}
	jobs, _, err := store.TodayJobs(ctx, pool, u1.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, j := range jobs {
		if j.ID == good && j.FitScore != nil && *j.FitScore == 75 {
			found = true
		}
	}
	if !found {
		t.Error("scored job missing from Today with its score")
	}
	if coral.facts.Load() != 2 || coral.fits.Load() != 1 {
		t.Errorf("LLM calls: %d facts, %d fits", coral.facts.Load(), coral.fits.Load())
	}

	// A refilter puts scored jobs back in the queue; filtering again links
	// the analysis this profile text already has, with no LLM call.
	if _, err := pool.Exec(ctx, `UPDATE user_jobs SET stage = 'pending_score', analysis_id = NULL
		WHERE user_id = $1 AND job_id = $2`, u1.ID, good); err != nil {
		t.Fatal(err)
	}
	callsBefore := coral.facts.Load() + coral.fits.Load()
	if _, err := FilterUser(ctx, pool, eng, nil, p1, nil); err != nil {
		t.Fatalf("refilter: %v", err)
	}
	if stage, _ := stageOf(t, pool, u1.ID, good); stage != "scored" {
		t.Errorf("cached analysis not relinked at filter time: %s", stage)
	}
	if coral.facts.Load()+coral.fits.Load() != callsBefore {
		t.Error("relinking a cached analysis must not call the LLM")
	}

	// A second user with the same preferences: facts are shared, so the
	// too-senior job drops out at filter time and only one fit call is made.
	u2 := testutil.User(t, pool, false)
	p2 := testutil.Profile(t, pool, u2.ID, sreProfile+"\nPrefers remote work.", srePrefs)
	if _, err := FilterUser(ctx, pool, eng, nil, p2, nil); err != nil {
		t.Fatalf("filter user 2: %v", err)
	}
	if stage, _ := stageOf(t, pool, u2.ID, tooSenior); stage != "excluded" {
		t.Errorf("known facts not applied at filter time: %s", stage)
	}
	if _, err := ScoreUser(ctx, pool, env, log, p2, 0, []int64{good}, 1, nil); err != nil {
		t.Fatalf("score user 2: %v", err)
	}
	if coral.facts.Load() != 2 || coral.fits.Load() != 2 {
		t.Errorf("user 2 should reuse facts: %d facts, %d fits", coral.facts.Load(), coral.fits.Load())
	}

	// A score from the old scoring method (stored under the bare profile
	// hash) keeps showing until the next run scores the job again, and the
	// rescore does not use up the user's daily score limit.
	if _, err := pool.Exec(ctx, `UPDATE analyses SET profile_hash = $1, fit_score = 88, verdict = 'strong'
		WHERE id = (SELECT analysis_id FROM user_jobs WHERE user_id = $2 AND job_id = $3)`, p1.ProfileHash, u1.ID, good); err != nil {
		t.Fatal(err)
	}
	if fit := todayScore(t, pool, u1.ID, good); fit != 88 {
		t.Errorf("old score hidden before the rescore: %d", fit)
	}
	before, err := store.UserScoreActivity(ctx, pool, u1.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	st, err = ScoreUser(ctx, pool, env, log, p1, 0, nil, 1, nil)
	if err != nil {
		t.Fatalf("rescore: %v", err)
	}
	if stage, _ := stageOf(t, pool, u1.ID, good); stage != "scored" {
		t.Errorf("rescored job: %s", stage)
	}
	if fit := todayScore(t, pool, u1.ID, good); fit != 75 {
		t.Errorf("rescored job shows %d, want 75", fit)
	}
	// The run also scored the user's other pending jobs: only those count.
	after, err := store.UserScoreActivity(ctx, pool, u1.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if after.ScoredLastDay != before.ScoredLastDay+st.Scored-1 {
		t.Errorf("a rescore counted against the daily limit: %d scores today before, %d after %d scored", before.ScoredLastDay, after.ScoredLastDay, st.Scored)
	}
	// Nothing older is left: the next run calls nothing.
	fitsBefore := coral.fits.Load()
	if _, err := ScoreUser(ctx, pool, env, log, p1, 0, nil, 1, nil); err != nil {
		t.Fatal(err)
	}
	if coral.fits.Load() != fitsBefore {
		t.Error("a current score was scored again")
	}
}

// TestAddingARoleFamilyKeepsScores checks that a preference change keeps
// the user's scores: adding a role family queues only the jobs it lets in,
// scored jobs keep their scores (one from an older scoring method too), the
// new rules still rule out a scored job they forbid, and jobs whose outcome
// did not change are not written.
func TestAddingARoleFamilyKeepsScores(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	coral := &fakeCoral{}
	srv := httptest.NewServer(coral)
	t.Cleanup(srv.Close)
	env := testEnv(srv.URL)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := testEngine(t)

	companyID, _ := testutil.Company(t, pool, "ai_ml")
	ids := testutil.Jobs(t, pool, companyID,
		testutil.Job{Title: "Site Reliability Engineer", Location: "Remote, US", Description: "Keep our Go services healthy."},
		testutil.Job{Title: "Platform Engineer", Location: "Remote, US", Description: "Run Kubernetes."},
		testutil.Job{Title: "Site Reliability Engineer II", Location: "Remote, US", Description: "Own on-call."},
		testutil.Job{Title: "Software Engineer", Location: "Remote, US", Description: "Build our Go backend."},
		testutil.Job{Title: "Accountant", Location: "Remote, US", Description: "Close the books."},
	)
	keep, stale, tooMany, swe, offTopic := ids[0], ids[1], ids[2], ids[3], ids[4]

	u := testutil.User(t, pool, false)
	prefs := srePrefs
	prefs.RoleFamilies = []string{"sre", "platform"}
	p := testutil.Profile(t, pool, u.ID, sreProfile, prefs)
	if _, err := FilterUser(ctx, pool, eng, nil, p, nil); err != nil {
		t.Fatalf("filter: %v", err)
	}
	if stage, _ := stageOf(t, pool, u.ID, swe); stage != "excluded" {
		t.Fatalf("software engineer before the family is added: %s", stage)
	}
	if _, err := ScoreUser(ctx, pool, env, log, p, 0, []int64{keep, stale, tooMany}, 1, nil); err != nil {
		t.Fatalf("score: %v", err)
	}
	// One score from the old scoring method, and one for a job asking 3 years.
	if _, err := pool.Exec(ctx, `UPDATE analyses SET profile_hash = $1, fit_score = 88
		WHERE id = (SELECT analysis_id FROM user_jobs WHERE user_id = $2 AND job_id = $3)`, p.ProfileHash, u.ID, stale); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE analyses SET years_required = 3
		WHERE id = (SELECT analysis_id FROM user_jobs WHERE user_id = $1 AND job_id = $2)`, u.ID, tooMany); err != nil {
		t.Fatal(err)
	}
	written := func(id int64) time.Time {
		t.Helper()
		var at time.Time
		if err := pool.QueryRow(ctx, `SELECT updated_at FROM user_jobs WHERE user_id = $1 AND job_id = $2`, u.ID, id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	before := written(offTopic)

	// Add software engineering and accept at most 2 years.
	prefs.RoleFamilies = []string{"sre", "platform", "software_engineering"}
	prefs.MaxYearsRequired = 2
	p = testutil.Profile(t, pool, u.ID, sreProfile, prefs)
	calls := coral.fits.Load()
	// The rule pass alone leaves scored jobs scored: none waits for a later
	// step (one a request deadline can cut short) to get its score back.
	out, err := store.FilterForUser(ctx, pool, eng, u.ID, &p.Preferences)
	if err != nil || !out.Refiltered {
		t.Fatalf("rule pass: %+v, %v", out, err)
	}
	for _, id := range []int64{keep, stale} {
		if stage, _ := stageOf(t, pool, u.ID, id); stage != "scored" {
			t.Errorf("job %d left the scored list during the rule pass: %s", id, stage)
		}
	}
	if _, err := FilterUser(ctx, pool, eng, nil, p, nil); err != nil {
		t.Fatalf("refilter: %v", err)
	}
	if coral.fits.Load() != calls {
		t.Error("a preference change must not score anything again")
	}
	if fit := todayScore(t, pool, u.ID, keep); fit != 75 {
		t.Errorf("scored job after adding a family shows %d, want 75", fit)
	}
	if fit := todayScore(t, pool, u.ID, stale); fit != 88 {
		t.Errorf("job scored by the old method shows %d, want its old 88 until rescored", fit)
	}
	if stage, reason := stageOf(t, pool, u.ID, tooMany); stage != "excluded" || !strings.Contains(reason, "years") {
		t.Errorf("scored job asking more years than now accepted: %s/%s", stage, reason)
	}
	if stage, _ := stageOf(t, pool, u.ID, swe); stage != "pending_score" {
		t.Errorf("job the new family lets in: %s, want pending_score", stage)
	}
	if !written(offTopic).Equal(before) {
		t.Error("a job whose outcome did not change was written again")
	}

	// The next pass knows the jobs were filtered with these preferences.
	keptAt := written(keep)
	if _, err := FilterUser(ctx, pool, eng, nil, p, nil); err != nil {
		t.Fatal(err)
	}
	if !written(keep).Equal(keptAt) {
		t.Error("an unchanged profile filtered every job again")
	}

	// A job lost to an interrupted refilter, back in the queue with only an
	// old-method score, gets that score back at the next pass.
	if _, err := pool.Exec(ctx, `UPDATE user_jobs SET stage = 'pending_score' WHERE user_id = $1 AND job_id = $2`, u.ID, stale); err != nil {
		t.Fatal(err)
	}
	if _, err := FilterUser(ctx, pool, eng, nil, p, nil); err != nil {
		t.Fatal(err)
	}
	if fit := todayScore(t, pool, u.ID, stale); fit != 88 {
		t.Errorf("queued job with an old-method score shows %d, want 88", fit)
	}
}

// TestQuickGradesRankTheWholeQueue checks that a scoring pass grades every
// queued job in batches, that the grades order the queue (the daily limit's
// one full score goes to the best graded job) and show as AI estimates, and
// that grading costs the user but not their daily score limit.
func TestQuickGradesRankTheWholeQueue(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	coral := &fakeCoral{quickBest: testutil.Unique("Quick Platform Engineer")}
	srv := httptest.NewServer(coral)
	t.Cleanup(srv.Close)
	env := testEnv(srv.URL)
	env.ScoresPerDay = 1
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	companyID, _ := testutil.Company(t, pool, "ai_ml")
	ids := testutil.Jobs(t, pool, companyID,
		testutil.Job{Title: "Site Reliability Engineer", Location: "Remote, US", Description: "Keep Go services healthy."},
		testutil.Job{Title: "Site Reliability Engineer II", Location: "Remote, US", Description: "Own on-call."},
		testutil.Job{Title: coral.quickBest, Location: "Remote, US", Description: "Run Kubernetes."},
	)
	best := ids[2]
	u := testutil.User(t, pool, false)
	// A profile text of its own: no score from another test is reused.
	p := testutil.Profile(t, pool, u.ID, sreProfile+"\n"+testutil.Unique("note"), srePrefs)
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM analyses WHERE profile_hash = $1`, scoring.ScoreKey(p.ProfileHash))
	})
	if _, err := FilterUser(ctx, pool, testEngine(t), nil, p, nil); err != nil {
		t.Fatal(err)
	}
	// The test database may hold other open jobs this profile matches.
	var pending int
	pool.QueryRow(ctx, `SELECT count(*) FROM user_jobs WHERE user_id = $1 AND stage = 'pending_score'`, u.ID).Scan(&pending)

	st, err := ScoreUser(ctx, pool, env, log, p, 0, nil, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.QuickGraded != pending || st.Scored != 1 || st.Deferred != pending-1 {
		t.Fatalf("stats %+v; want all %d graded, 1 scored, the rest deferred", st, pending)
	}
	if want := (pending + scoring.QuickBatch - 1) / scoring.QuickBatch; coral.quick.Load() != int64(want) {
		t.Errorf("%d quick calls for %d jobs, want %d", coral.quick.Load(), pending, want)
	}
	if stage, _ := stageOf(t, pool, u.ID, best); stage != "scored" {
		t.Errorf("the one full score should go to the best graded job: it is %s", stage)
	}
	mid := scoring.Ratings{Role: 5, Seniority: 5, Skills: 5, Domain: 5, Location: 5}
	for _, id := range ids[:2] {
		var est, grade *int
		pool.QueryRow(ctx, `SELECT uj.estimated_score, q.score FROM user_jobs uj
			LEFT JOIN quick_grades q ON q.user_id = uj.user_id AND q.job_id = uj.job_id
			WHERE uj.user_id = $1 AND uj.job_id = $2`, u.ID, id).Scan(&est, &grade)
		if grade == nil || est == nil || *est != *grade || *grade < mid.Score(-1)-15 || *grade > mid.Score(-1)+15 {
			t.Errorf("job %d: estimate %v, quick grade %v; want the grade, about %d", id, est, grade, mid.Score(-1))
		}
	}
	jobs, _, err := store.TodayJobs(ctx, pool, u.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if j.FitScore == nil && !j.EstimateAI {
			t.Errorf("Today shows job %d with an estimate not marked as the AI's", j.ID)
		}
	}
	var quickCost float64
	pool.QueryRow(ctx, `SELECT COALESCE(sum(cost_usd), 0) FROM llm_usage WHERE user_id = $1 AND kind = 'quick'`, u.ID).Scan(&quickCost)
	if quickCost <= 0 {
		t.Error("quick grading was not charged to the user")
	}
	act, err := store.UserScoreActivity(ctx, pool, u.ID, scoring.ScoreKey(p.ProfileHash))
	if err != nil || act.ScoredLastDay != 1 {
		t.Errorf("quick grades counted against the daily limit: %d scores today (%v), want 1", act.ScoredLastDay, err)
	}

	// The next pass grades nothing again.
	if st, err = ScoreUser(ctx, pool, env, log, p, 0, nil, 1, nil); err != nil || st.QuickGraded != 0 {
		t.Errorf("second pass graded %d (%v), want 0", st.QuickGraded, err)
	}
}

// TestPrefilterDuringSetup checks that the rule filter run while a new
// user writes their profile leaves the profile save nothing to re-filter.
func TestPrefilterDuringSetup(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	eng := testEngine(t)
	companyID, _ := testutil.Company(t, pool, "ai_ml")
	ids := testutil.Jobs(t, pool, companyID,
		testutil.Job{Title: "Site Reliability Engineer", Location: "Remote, US", Description: "Keep Go services healthy."},
		testutil.Job{Title: "Accountant", Location: "Remote, US", Description: "Close the books."},
	)
	u := testutil.User(t, pool, false)
	if err := store.SavePreferences(ctx, pool, u.ID, srePrefs, nil); err != nil {
		t.Fatal(err)
	}
	if err := Prefilter(ctx, pool, eng, u.ID); err != nil {
		t.Fatal(err)
	}
	if stage, _ := stageOf(t, pool, u.ID, ids[0]); stage != "pending_score" {
		t.Errorf("matching job after the prefilter: %s", stage)
	}
	if stage, _ := stageOf(t, pool, u.ID, ids[1]); stage != "excluded" {
		t.Errorf("off-topic job after the prefilter: %s", stage)
	}
	p := testutil.Profile(t, pool, u.ID, sreProfile, srePrefs)
	out, err := store.FilterForUser(ctx, pool, eng, u.ID, &p.Preferences)
	if err != nil || out.Refiltered {
		t.Errorf("the save filtered every job again after the prefilter: %+v, %v", out, err)
	}
	// Once the text exists the prefilter leaves the user to the matcher.
	pool.Exec(ctx, `UPDATE profiles SET filtered_preferences = NULL WHERE user_id = $1`, u.ID)
	if err := Prefilter(ctx, pool, eng, u.ID); err != nil {
		t.Fatal(err)
	}
	var marked bool
	pool.QueryRow(ctx, `SELECT filtered_preferences IS NOT NULL FROM profiles WHERE user_id = $1`, u.ID).Scan(&marked)
	if marked {
		t.Error("the prefilter ran for a profile that has its text")
	}
}

func todayScore(t *testing.T, pool *pgxpool.Pool, userID, jobID int64) int {
	t.Helper()
	jobs, _, err := store.TodayJobs(context.Background(), pool, userID, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if j.ID == jobID && j.FitScore != nil {
			return *j.FitScore
		}
	}
	return -1
}

// TestShareJob checks that a job one user adds reaches the others it
// suits, scored, and is filtered out for the rest.
func TestShareJob(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	coral := &fakeCoral{}
	srv := httptest.NewServer(coral)
	t.Cleanup(srv.Close)
	env := testEnv(srv.URL)
	env.MaxCostPerRunUSD, env.MaxMonthlyCostUSD = 1, 1e6
	p := &Pipeline{Pool: pool, Env: env, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Engine: testEngine(t)}

	adder := testutil.User(t, pool, false)
	other := testutil.User(t, pool, false)
	testutil.Profile(t, pool, other.ID, sreProfile, srePrefs)
	add := func(title string) int64 {
		t.Helper()
		id, created, err := store.AddManualJob(ctx, pool, adder.ID, store.ManualJob{
			URL: "https://careers.example.com/" + testutil.Unique("job"), Company: "Shared Co " + testutil.Unique("c"),
			Title: title, Location: "Remote, US", Description: "Keep our Go services healthy with Kubernetes.",
		})
		if err != nil || !created {
			t.Fatalf("add %q: %v (created %v)", title, err, created)
		}
		t.Cleanup(func() {
			pool.Exec(ctx, `DELETE FROM companies WHERE id = (SELECT company_id FROM jobs WHERE id = $1)`, id)
		})
		return id
	}
	sre, accountant := add("Site Reliability Engineer"), add("Accountant")

	if _, scored, err := p.ShareJob(ctx, sre, adder.ID); err != nil || scored != 1 {
		t.Fatalf("share: scored %d, %v", scored, err)
	}
	if stage, _ := stageOf(t, pool, other.ID, sre); stage != "scored" {
		t.Errorf("a suitable added job should be scored for other users: %s", stage)
	}
	if _, scored, err := p.ShareJob(ctx, accountant, adder.ID); err != nil || scored != 0 {
		t.Fatalf("share off-topic: scored %d, %v", scored, err)
	}
	if stage, _ := stageOf(t, pool, other.ID, accountant); stage != "excluded" {
		t.Errorf("an off-topic added job should be rule-filtered: %s", stage)
	}

	// The other user adding the posting themselves gets the shared job,
	// even though their rules excluded it.
	var url string
	pool.QueryRow(ctx, `SELECT url FROM jobs WHERE id = $1`, accountant).Scan(&url)
	id, created, err := store.AddManualJob(ctx, pool, other.ID, store.ManualJob{URL: url, Company: "X", Title: "Accountant"})
	if err != nil || id != accountant || !created {
		t.Fatalf("re-add: id %d (want %d), created %v, %v", id, accountant, created, err)
	}
	if stage, _ := stageOf(t, pool, other.ID, accountant); stage != "pending_score" {
		t.Errorf("adding a job on purpose lifts its rule exclusion: %s", stage)
	}

	// Added postings close after their lifetime.
	pool.Exec(ctx, `UPDATE jobs SET last_seen_at = now() - interval '60 days' WHERE id = $1`, sre)
	if n, err := store.CloseStaleManualJobs(ctx, pool); err != nil || n < 1 {
		t.Errorf("close stale: %d, %v", n, err)
	}

	// Long-closed postings drop the matches nobody acted on; scored and
	// tracked ones stay.
	pool.Exec(ctx, `UPDATE jobs SET closed_at = now() - interval '40 days' WHERE id = ANY($1)`, []int64{sre, accountant})
	pool.Exec(ctx, `UPDATE user_jobs SET stage = 'excluded' WHERE user_id = $1 AND job_id = $2`, other.ID, accountant)
	pool.Exec(ctx, `DELETE FROM applications WHERE user_id = $1 AND job_id = $2`, other.ID, accountant)
	if _, err := store.PruneClosedMatches(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if stage, _ := store.UserJobStage(ctx, pool, other.ID, accountant); stage != "" {
		t.Errorf("an untouched closed match should be pruned: %q", stage)
	}
	if stage, _ := store.UserJobStage(ctx, pool, other.ID, sre); stage != "scored" {
		t.Errorf("a scored closed match must stay: %q", stage)
	}
	if stage, _ := store.UserJobStage(ctx, pool, adder.ID, accountant); stage == "" {
		t.Error("a tracked closed job must stay")
	}

	// The job outlives the account that added it: others still have it.
	if err := store.DeleteUser(ctx, pool, adder.ID); err != nil {
		t.Fatal(err)
	}
	if stage, _ := store.UserJobStage(ctx, pool, other.ID, sre); stage != "scored" {
		t.Errorf("a shared job must survive its adder's account: %q", stage)
	}
}

func TestScoreUserWithoutKeySkips(t *testing.T) {
	st, err := ScoreUser(context.Background(), nil, &config.Env{}, slog.New(slog.NewTextHandler(io.Discard, nil)), &store.Profile{}, 0, nil, 1, nil)
	if st != nil || err != nil {
		t.Errorf("want (nil, nil) without an API key, got %v, %v", st, err)
	}
}

func TestExclusionsFor(t *testing.T) {
	r := ExclusionsFor(&config.Preferences{MaxYearsRequired: 3, NeedsSponsorship: true, USOnly: true, Levels: []string{"entry"}})
	if r.MaxYears != 3 || !r.RequireSponsorship || !r.USOnly || r.AllowSenior {
		t.Errorf("%+v", r)
	}
	if !ExclusionsFor(&config.Preferences{Levels: []string{"senior"}}).AllowSenior {
		t.Error("senior level should allow senior roles")
	}
}

func TestScoreStatsAdd(t *testing.T) {
	a := &ScoreStats{Scored: 1, CostUSD: 0.5}
	a.Add(&ScoreStats{Scored: 2, Failed: 1, CostUSD: 0.25})
	a.Add(nil)
	if a.Scored != 3 || a.Failed != 1 || a.CostUSD != 0.75 {
		t.Errorf("%+v", a)
	}
}
