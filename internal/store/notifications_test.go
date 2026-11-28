package store_test

import (
	"context"
	"testing"
	"time"

	"wera/internal/store"
	"wera/internal/testutil"
)

func TestMakeNotifications(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	me, quiet := testutil.User(t, pool, false), testutil.User(t, pool, false)
	companyID, company := testutil.Company(t, pool, "other")
	ids := testutil.Jobs(t, pool, companyID,
		testutil.Job{Title: "Strong A"}, testutil.Job{Title: "Strong B"}, testutil.Job{Title: "Weak C"},
		testutil.Job{Title: "Applied D"}, testutil.Job{Title: "Tracked E"})
	now := time.Now()
	// The quiet user turned strong-match alerts off.
	if _, err := pool.Exec(ctx, `UPDATE users SET settings = '{"notifications":{"strong_matches":false}}' WHERE id = $1`, quiet.ID); err != nil {
		t.Fatal(err)
	}
	score := func(user, job int64, fit int) {
		t.Helper()
		var aid int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO analyses (job_id, kind, model, profile_hash, fit_score, raw, user_id, created_at)
			VALUES ($1, 'score', 'test', $2, $3, '{}', $4, $5) RETURNING id`,
			// Scored at now itself: a minute earlier falls on yesterday in the first minute of a UTC day.
			job, testutil.Unique("h"), fit, user, now).Scan(&aid); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage, analysis_id) VALUES ($1, $2, 'h', 'scored', $3)`, user, job, aid); err != nil {
			t.Fatal(err)
		}
	}
	score(me.ID, ids[0], 85)
	score(me.ID, ids[1], 91)
	score(me.ID, ids[2], 60) // under the alert score
	score(me.ID, ids[3], 95) // applied already: dealt with
	score(quiet.ID, ids[0], 90)
	put := func(job int64, status string) {
		t.Helper()
		if err := store.PutApplication(ctx, pool, me.ID, job, store.ApplicationInput{Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	put(ids[3], "applied")
	if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage) VALUES ($1, $2, 'h', 'scored')`, me.ID, ids[4]); err != nil {
		t.Fatal(err)
	}
	put(ids[4], "saved")
	if _, err := pool.Exec(ctx, `UPDATE jobs SET closed_at = $2 WHERE id = $1`, ids[4], now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	// An interview tomorrow on the applied job.
	if _, err := store.AddEvent(ctx, pool, me.ID, store.Event{JobID: ids[3], At: now.Add(20 * time.Hour), Kind: "onsite"}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.MakeNotifications(ctx, pool, now); err != nil {
		t.Fatal(err)
	}
	list, unread, err := store.ListNotifications(ctx, pool, me.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[string]store.Notification{}
	for _, n := range list {
		byKind[n.Kind] = n
	}
	if len(list) != 3 || unread != 3 {
		t.Fatalf("got %d notices (%d unread): %+v", len(list), unread, list)
	}
	if m := byKind["strong_matches"]; m.Title != "2 new strong matches" || m.JobID == nil || *m.JobID != ids[1] || m.Body != "Best: Strong B at "+company {
		t.Errorf("strong matches: %+v", m)
	}
	if c := byKind["posting_closed"]; c.JobID == nil || *c.JobID != ids[4] {
		t.Errorf("closed: %+v", c)
	}
	if r := byKind["reminder"]; r.Title != "Onsite" || r.At == nil || r.JobID == nil || *r.JobID != ids[3] {
		t.Errorf("reminder: %+v", r)
	}
	if q, _, _ := store.ListNotifications(ctx, pool, quiet.ID, 0); len(q) != 0 {
		t.Errorf("alerts off still notified: %+v", q)
	}

	// Again: nothing new. Read them all; a new strong match brings the day's notice back, updated.
	if n, err := store.MakeNotifications(ctx, pool, now); err != nil || n != 0 {
		t.Errorf("second pass made %d (%v)", n, err)
	}
	if err := store.MarkNotificationsRead(ctx, pool, me.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, unread, _ = store.ListNotifications(ctx, pool, me.ID, 0); unread != 0 {
		t.Errorf("unread after reading all: %d", unread)
	}
	more := testutil.Jobs(t, pool, companyID, testutil.Job{Title: "Strong F"})
	score(me.ID, more[0], 88)
	if _, err := store.MakeNotifications(ctx, pool, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	list, unread, _ = store.ListNotifications(ctx, pool, me.ID, 0)
	if unread != 1 || list[0].Title != "3 new strong matches" {
		t.Errorf("after a new match: %d unread, %+v", unread, list[0])
	}
	// Reading one by id leaves others alone.
	if err := store.MarkNotificationsRead(ctx, pool, me.ID, []int64{list[0].ID}); err != nil {
		t.Fatal(err)
	}
	if _, unread, _ = store.ListNotifications(ctx, pool, me.ID, 0); unread != 0 {
		t.Errorf("unread: %d", unread)
	}
}
