package store_test

import (
	"context"
	"testing"

	"wera/internal/config"
	"wera/internal/store"
	"wera/internal/testutil"
)

func TestScoredVectors(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	model, other := testutil.Unique("embed"), testutil.Unique("embed")
	companyID, _ := testutil.Company(t, pool, "ai_ml")
	jobs := make([]testutil.Job, 5)
	for i := range jobs {
		jobs[i] = testutil.Job{Title: testutil.Unique("Engineer"), Location: "Remote, US", Description: "Build."}
	}
	ids := testutil.Jobs(t, pool, companyID, jobs...)
	me, them := testutil.User(t, pool, false), testutil.User(t, pool, false)
	prefs := config.Preferences{Levels: []string{"mid"}}
	testutil.Profile(t, pool, me.ID, "# Profile", prefs)
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM analyses WHERE user_id = ANY($1)`, []int64{me.ID, them.ID})
		pool.Exec(context.Background(), `DELETE FROM job_embeddings WHERE model = ANY($1)`, []string{model, other})
	})
	if err := store.SaveJobEmbeddings(ctx, pool, model, map[int64][]float32{
		ids[0]: {1, 0}, ids[1]: {0, 1}, ids[2]: {1, 1}, ids[3]: {2, 2},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveJobEmbeddings(ctx, pool, other, map[int64][]float32{ids[4]: {3, 3}}); err != nil {
		t.Fatal(err)
	}
	score := func(user, job int64, key string, fit any, age string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO analyses (job_id, kind, model, profile_hash, fit_score, raw, user_id, created_at)
			VALUES ($1, 'score', 'test', $2, $3, '{}', $4, now() - $5::interval)`, job, key, fit, user, age); err != nil {
			t.Fatal(err)
		}
	}
	score(me.ID, ids[0], "old", 10, "3 days") // scored again below: the newest counts
	score(me.ID, ids[0], "new", 80, "1 hour")
	score(me.ID, ids[1], "old", 30, "2 days")
	score(me.ID, ids[2], "another-profile", 99, "1 hour")
	score(me.ID, ids[3], "new", nil, "1 hour")  // a failed score has no number
	score(me.ID, ids[4], "new", 70, "1 hour")   // embedded by another model
	score(them.ID, ids[1], "new", 55, "1 hour") // someone else's

	got, err := store.ScoredVectors(ctx, pool, me.ID, []string{"old", "new"}, model, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Score != 80 || got[1].Score != 30 {
		t.Fatalf("got %+v, want the newest score of the two usable jobs, newest first", got)
	}
	if v := got[0].Vector; len(v) != 2 || v[0] != 1 {
		t.Errorf("vector: %v", v)
	}
	if got, err = store.ScoredVectors(ctx, pool, me.ID, []string{"old", "new"}, model, 1); err != nil || len(got) != 1 || got[0].Score != 80 {
		t.Errorf("the limit keeps the most recent: %+v, %v", got, err)
	}
	// A score linked from the cache (another account, same profile text)
	// is the user's score too.
	var shared int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO analyses (job_id, kind, model, profile_hash, fit_score, raw, user_id, created_at)
		VALUES ($1, 'score', 'test', 'new', 64, '{}', $2, now() - interval '30 minutes') RETURNING id`, ids[2], them.ID).Scan(&shared); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage, analysis_id) VALUES ($1, $2, 'h', 'scored', $3)`, me.ID, ids[2], shared); err != nil {
		t.Fatal(err)
	}
	if got, err = store.ScoredVectors(ctx, pool, me.ID, []string{"old", "new"}, model, 10); err != nil || len(got) != 3 || got[0].Score != 64 {
		t.Fatalf("with a linked score: %+v, %v; want 64 (newest), 80, 30", got, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM user_jobs WHERE user_id = $1 AND job_id = $2`, me.ID, ids[2]); err != nil {
		t.Fatal(err)
	}
	// Passing on a job for a reason about the job teaches a clear no, scored
	// or not; pay or no reason teaches nothing.
	dismiss := func(job int64, reason *string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage) VALUES ($1, $2, 'h', 'scored') ON CONFLICT DO NOTHING`, me.ID, job); err != nil {
			t.Fatal(err)
		}
		if err := store.PutApplication(ctx, pool, me.ID, job, store.ApplicationInput{Status: "not_interested", Reason: reason}); err != nil {
			t.Fatal(err)
		}
	}
	field, senior, pay := "field", "too_senior", "pay"
	dismiss(ids[1], &field)  // scored 30: now a no
	dismiss(ids[2], &senior) // scored under another profile only: still a no
	dismiss(ids[3], &pay)
	got, err = store.ScoredVectors(ctx, pool, me.ID, []string{"old", "new"}, model, 10)
	if err != nil {
		t.Fatal(err)
	}
	scores := map[float32]float64{}
	for _, g := range got {
		scores[g.Vector[0]+10*g.Vector[1]] = g.Score
	}
	want := map[float32]float64{1: 80, 10: store.DismissedScore, 11: store.DismissedScore}
	if len(scores) != len(want) {
		t.Fatalf("with dismissals: %+v", got)
	}
	for k, w := range want {
		if scores[k] != w {
			t.Errorf("job with vector key %v: score %v, want %v", k, scores[k], w)
		}
	}
	// Another status clears the reason.
	if err := store.PutApplication(ctx, pool, me.ID, ids[1], store.ApplicationInput{Status: "saved"}); err != nil {
		t.Fatal(err)
	}
	var reason *string
	if err := pool.QueryRow(ctx, `SELECT dismiss_reason FROM applications WHERE user_id = $1 AND job_id = $2`, me.ID, ids[1]).Scan(&reason); err != nil || reason != nil {
		t.Errorf("reason after saving: %v, %v", reason, err)
	}
}
