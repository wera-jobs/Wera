package store_test

import (
	"context"
	"testing"

	"wera/internal/store"
	"wera/internal/testutil"
)

// Early: posted in the last three days and found within a day of it.
func TestEarlyApplicantFlag(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	user := testutil.User(t, pool, false)
	companyID, _ := testutil.Company(t, pool, "other")
	ids := testutil.Jobs(t, pool, companyID,
		testutil.Job{Title: "Early Fresh"}, testutil.Job{Title: "Early Found Late"}, testutil.Job{Title: "Early Old"}, testutil.Job{Title: "Early Undated"})
	set := `UPDATE jobs SET posted_at = now() - $2::interval, first_seen_at = now() - $3::interval WHERE id = $1`
	for i, iv := range [][2]string{{"5 hours", "4 hours"}, {"2 days", "4 hours"}, {"10 days", "10 days"}} {
		if _, err := pool.Exec(ctx, set, ids[i], iv[0], iv[1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE jobs SET posted_at = NULL WHERE id = $1`, ids[3]); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage) VALUES ($1, $2, 'h', 'pending_score')`, user.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := store.ListJobs(ctx, pool, user.ID, store.JobQuery{Q: "early"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, j := range jobs {
		got[j.Title] = j.Early
	}
	want := map[string]bool{"Early Fresh": true, "Early Found Late": false, "Early Old": false, "Early Undated": false}
	for title, w := range want {
		if got[title] != w {
			t.Errorf("%s: early = %v, want %v", title, got[title], w)
		}
	}
}
