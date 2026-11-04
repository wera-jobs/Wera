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

// The AI scores fresh postings before months-old ones of a similar
// estimate, but a much better old match still goes first.
func TestScoringQueuePrefersFreshPostings(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	user := testutil.User(t, pool, false)
	companyID, _ := testutil.Company(t, pool, "other")
	ids := testutil.Jobs(t, pool, companyID,
		testutil.Job{Title: "Old, Good"}, testutil.Job{Title: "Fresh, Fair"}, testutil.Job{Title: "Old, Great"})
	for i, v := range []struct {
		days     int
		estimate int
	}{{200, 78}, {5, 72}, {200, 90}} {
		pool.Exec(ctx, `UPDATE jobs SET posted_at = now() - make_interval(days => $2) WHERE id = $1`, ids[i], v.days)
		if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage, estimated_score)
			VALUES ($1, $2, 'h', 'pending_score', $3)`, user.ID, ids[i], v.estimate); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := store.PendingScoreJobs(ctx, pool, user.ID, "none", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(jobs))
	for _, j := range jobs {
		got = append(got, j.Job.Title)
	}
	if len(got) != 3 || got[0] != "Old, Great" || got[1] != "Fresh, Fair" || got[2] != "Old, Good" {
		t.Errorf("queue order: %v", got)
	}
}

// "Applied this week" counts applications sent in the last 7 days, not
// every job the user saved or dismissed, nor one moved back after applying.
func TestStatsAppliedThisWeek(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	user := testutil.User(t, pool, false)
	companyID, _ := testutil.Company(t, pool, "other")
	ids := testutil.Jobs(t, pool, companyID,
		testutil.Job{Title: "Applied"}, testutil.Job{Title: "Interviewing"}, testutil.Job{Title: "Saved"},
		testutil.Job{Title: "Undone"}, testutil.Job{Title: "Long ago"})
	for _, id := range ids {
		if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage) VALUES ($1, $2, 'h', 'scored')`, user.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	put := func(id int64, status string) {
		t.Helper()
		if err := store.PutApplication(ctx, pool, user.ID, id, store.ApplicationInput{Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	put(ids[0], "applied")
	put(ids[1], "applied")
	put(ids[1], "interviewing")
	put(ids[2], "saved")
	put(ids[3], "applied")
	put(ids[3], "saved")
	put(ids[4], "applied")
	pool.Exec(ctx, `UPDATE applications SET applied_at = now() - interval '9 days' WHERE job_id = $1`, ids[4])

	st, err := store.Stats(ctx, pool, user.ID, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if st.ApplicationsWeek != 2 {
		t.Errorf("applied this week: got %d, want 2 (applied, interviewing)", st.ApplicationsWeek)
	}
	if st.ByStatus["applied"] != 2 || st.ByStatus["interviewing"] != 1 || st.ByStatus["saved"] != 2 {
		t.Errorf("by status: %v", st.ByStatus)
	}
}
