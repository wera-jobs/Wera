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

func TestGoalStreak(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	me := testutil.User(t, pool, false)
	companyID, _ := testutil.Company(t, pool, "other")
	ids := testutil.Jobs(t, pool, companyID, testutil.Job{Title: "S1"}, testutil.Job{Title: "S2"}, testutil.Job{Title: "S3"},
		testutil.Job{Title: "S4"}, testutil.Job{Title: "S5"}, testutil.Job{Title: "S6"}, testutil.Job{Title: "S7"})
	monday := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage) SELECT $1, unnest($2::bigint[]), 'h', 'scored'`, me.ID, ids); err != nil {
		t.Fatal(err)
	}
	sent := func(job int64, at time.Time) {
		t.Helper()
		if err := store.PutApplication(ctx, pool, me.ID, job, store.ApplicationInput{Status: "applied"}); err != nil {
			t.Fatal(err)
		}
		if err := store.SetAppliedAt(ctx, pool, me.ID, job, at); err != nil {
			t.Fatal(err)
		}
	}
	// Two a week for the three weeks before monday, one the week before
	// those, and one so far in monday's week. Moving to interviewing
	// later doesn't count a second time.
	sent(ids[0], monday.AddDate(0, 0, -1))
	sent(ids[1], monday.AddDate(0, 0, -6))
	sent(ids[2], monday.AddDate(0, 0, -8))
	sent(ids[3], monday.AddDate(0, 0, -13))
	sent(ids[4], monday.AddDate(0, 0, -15))
	sent(ids[5], monday.AddDate(0, 0, -20))
	sent(ids[6], monday.Add(2*time.Hour))
	if err := store.PutApplication(ctx, pool, me.ID, ids[0], store.ApplicationInput{Status: "interviewing"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		monday time.Time
		goal   int
		want   int
	}{
		{monday, 0, 0},                   // no goal, no streak
		{monday, 2, 3},                   // this week isn't there yet: the three before it
		{monday, 1, 4},                   // this week counts once it meets the goal
		{monday.AddDate(0, 0, -7), 2, 3}, // reviewing an earlier week
		{monday, 3, 0},
		{monday.AddDate(0, 0, 14), 1, 0}, // a whole week with nothing breaks it
	} {
		got, err := store.GoalStreak(ctx, pool, me.ID, c.monday, c.goal)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("streak for week of %s, goal %d: got %d, want %d", c.monday.Format("2006-01-02"), c.goal, got, c.want)
		}
	}
	if err := store.SaveSettings(ctx, pool, me.ID, store.Settings{Theme: "system", DefaultSort: "score",
		Notifications: store.Notifications{Frequency: "daily", MinScore: 80}, WeeklyApplicationGoal: 2}); err != nil {
		t.Fatal(err)
	}
	r, err := store.GetWeeklyReview(ctx, pool, me.ID, monday)
	if err != nil {
		t.Fatal(err)
	}
	if r.WeeklyGoal != 2 || r.GoalStreakWeeks != 3 {
		t.Errorf("review goal %d, streak %d; want 2 and 3", r.WeeklyGoal, r.GoalStreakWeeks)
	}
}

func TestSettingsGoalRange(t *testing.T) {
	for goal, ok := range map[int]bool{-1: false, 0: true, 100: true, 101: false} {
		s := store.DefaultSettings()
		s.WeeklyApplicationGoal = goal
		if err := s.Validate(); (err == nil) != ok {
			t.Errorf("goal %d: err %v", goal, err)
		}
	}
}
