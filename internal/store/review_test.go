package store_test

import (
	"context"
	"testing"
	"time"

	"wera/internal/store"
	"wera/internal/testutil"
)

func TestWeeklyReview(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	me := testutil.User(t, pool, false)
	companyID, _ := testutil.Company(t, pool, "other")
	ids := testutil.Jobs(t, pool, companyID, testutil.Job{Title: "Rev One"}, testutil.Job{Title: "Rev Two"}, testutil.Job{Title: "Rev Three"}, testutil.Job{Title: "Rev Strong"})
	now := time.Now().UTC()
	monday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -((int(now.Weekday()) + 6) % 7))
	score := func(job int64, fit int, missing []string) {
		t.Helper()
		var aid int64
		if err := pool.QueryRow(ctx, `INSERT INTO analyses (job_id, kind, model, profile_hash, fit_score, raw, user_id, skills_missing, created_at)
			VALUES ($1, 'score', 'm', $2, $3, '{}', $4, $5, $6) RETURNING id`, job, testutil.Unique("h"), fit, me.ID, missing, monday.Add(time.Hour)).Scan(&aid); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage, analysis_id) VALUES ($1, $2, 'h', 'scored', $3)`, me.ID, job, aid); err != nil {
			t.Fatal(err)
		}
	}
	score(ids[0], 70, []string{"Kubernetes"})
	score(ids[1], 75, []string{"Kubernetes", "Go"})
	score(ids[2], 60, nil)
	score(ids[3], 92, nil)
	put := func(job int64, status string) {
		t.Helper()
		if err := store.PutApplication(ctx, pool, me.ID, job, store.ApplicationInput{Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	// Two applied this week; one of them heard back. One applied three weeks ago, still waiting.
	put(ids[0], "applied")
	put(ids[0], "interviewing")
	put(ids[1], "applied")
	put(ids[2], "applied")
	if err := store.SetAppliedAt(ctx, pool, me.ID, ids[2], monday.AddDate(0, 0, -20)); err != nil {
		t.Fatal(err)
	}
	r, err := store.GetWeeklyReview(ctx, pool, me.ID, monday)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Applied) != 2 || len(r.Replies) != 1 || r.Replies[0].JobID != ids[0] || r.Replies[0].Status != "interviewing" {
		t.Errorf("applied and replies: %+v %+v", r.Applied, r.Replies)
	}
	if r.StrongNew != 1 || r.StrongActed != 0 || r.StrongBest == nil || r.StrongBest.JobID != ids[3] {
		t.Errorf("strong: %d %d %+v", r.StrongNew, r.StrongActed, r.StrongBest)
	}
	if r.Waiting != 1 {
		t.Errorf("waiting: %d", r.Waiting)
	}
}
