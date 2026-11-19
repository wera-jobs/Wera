package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"wera/internal/store"
	"wera/internal/testutil"
)

// A scored job carries the ratings its score came from; a version 1 score
// (one number, no ratings) and an unreadable reply carry none.
func TestJobFitParts(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	user := testutil.User(t, pool, false)
	companyID, _ := testutil.Company(t, pool, "other")
	ids := testutil.Jobs(t, pool, companyID, testutil.Job{Title: "Parts Rated"}, testutil.Job{Title: "Parts Old"}, testutil.Job{Title: "Parts Garbled"})
	reply := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	raws := []string{
		reply("```json\n{\"ratings\": {\"role\": 8, \"seniority\": 4, \"skills\": 6, \"domain\": 3, \"location\": 1, \"blocker\": false}, \"reason\": \"fits\"}\n```"),
		`{"fit_score": 72}`,
		reply(`{"ratings": {"role": 80, "seniority": 4}, "reason": "x"}`),
	}
	aids := make([]int64, 0, len(ids))
	for i, id := range ids {
		var aid int64
		if err := pool.QueryRow(ctx, `INSERT INTO analyses (job_id, kind, model, profile_hash, fit_score, reason, raw, user_id)
			VALUES ($1, 'score', 'm', $2, 70, 'fits', $3::jsonb, $4) RETURNING id`, id, testutil.Unique("hash"), raws[i], user.ID).Scan(&aid); err != nil {
			t.Fatal(err)
		}
		aids = append(aids, aid)
		if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, stage, analysis_id, content_hash) VALUES ($1, $2, 'scored', $3, 'h')`, user.ID, id, aid); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM analyses WHERE id = ANY($1)`, aids) })

	got, err := store.GetJob(ctx, pool, user.ID, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	want := store.FitParts{Role: 8, Seniority: 4, Skills: 6, Domain: 3, Location: 1}
	if got.FitParts == nil || *got.FitParts != want {
		t.Errorf("parts = %+v, want %+v", got.FitParts, want)
	}
	jobs, err := store.ListJobs(ctx, pool, user.ID, store.JobQuery{Q: "parts"})
	if err != nil || len(jobs) != 3 {
		t.Fatalf("list: %d, %v", len(jobs), err)
	}
	for _, j := range jobs {
		if (j.Title == "Parts Rated") != (j.FitParts != nil) {
			t.Errorf("%s: parts %+v", j.Title, j.FitParts)
		}
	}
}
