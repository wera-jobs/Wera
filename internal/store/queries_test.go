package store_test

import (
	"context"
	"testing"

	"wera/internal/store"
	"wera/internal/testutil"
)

// "Newest" and "posted within" go by when the posting went up, not when
// Wera first saw it: a newly watched company's old postings are not new.
func TestListJobsByPostingDate(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	user := testutil.User(t, pool, false)
	companyID, _ := testutil.Company(t, pool, "other")
	ids := testutil.Jobs(t, pool, companyID,
		testutil.Job{Title: "Old Posting, Just Found"},
		testutil.Job{Title: "Fresh Posting"},
		testutil.Job{Title: "No Posting Date"})
	pool.Exec(ctx, `UPDATE jobs SET posted_at = now() - interval '120 days' WHERE id = $1`, ids[0])
	pool.Exec(ctx, `UPDATE jobs SET posted_at = now() - interval '2 days', first_seen_at = now() - interval '1 day' WHERE id = $1`, ids[1])
	pool.Exec(ctx, `UPDATE jobs SET first_seen_at = now() - interval '10 days' WHERE id = $1`, ids[2])
	for _, id := range ids {
		if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage) VALUES ($1, $2, 'h', 'pending_score')`, user.ID, id); err != nil {
			t.Fatal(err)
		}
	}

	titles := func(q store.JobQuery) []string {
		t.Helper()
		q.Q = "posting"
		jobs, err := store.ListJobs(ctx, pool, user.ID, q)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(jobs))
		for _, j := range jobs {
			out = append(out, j.Title)
		}
		return out
	}
	got := titles(store.JobQuery{Sort: "newest"})
	want := []string{"Fresh Posting", "No Posting Date", "Old Posting, Just Found"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("newest: got %v, want %v", got, want)
	}
	if got := titles(store.JobQuery{PostedWithin: 30}); len(got) != 2 {
		t.Errorf("posted within 30 days: got %v, want the fresh and the undated one", got)
	}
	if got := titles(store.JobQuery{PostedWithin: 7}); len(got) != 1 || got[0] != "Fresh Posting" {
		t.Errorf("posted within 7 days: got %v", got)
	}
}
