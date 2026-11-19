package store_test

import (
	"context"
	"testing"

	"wera/internal/store"
	"wera/internal/testutil"
)

func TestApplicationAnalytics(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	empty := testutil.User(t, pool, false)
	a, err := store.ApplicationAnalytics(ctx, pool, empty.ID, 12)
	if err != nil {
		t.Fatal(err)
	}
	if a.Reached.Applied != 0 || len(a.Weeks) != 12 || len(a.Sources) != 0 || a.DaysToInterview != nil || a.Now["applied"] != 0 {
		t.Errorf("empty account: %+v", a)
	}

	me := testutil.User(t, pool, false)
	companyID, _ := testutil.Company(t, pool, "other")
	ids := testutil.Jobs(t, pool, companyID, testutil.Job{Title: "A1"}, testutil.Job{Title: "A2"}, testutil.Job{Title: "A3"}, testutil.Job{Title: "A4"})
	for _, id := range ids {
		if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage) VALUES ($1, $2, 'h', 'scored')`, me.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	put := func(job int64, status string) {
		t.Helper()
		if err := store.PutApplication(ctx, pool, me.ID, job, store.ApplicationInput{Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	// The trigger records each change; age them so the stages are days apart.
	age := func(job int64, status string, daysAgo int) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE application_changes SET at = now() - make_interval(days => $3) WHERE user_id = $1 AND job_id = $2 AND status = $4`, me.ID, job, daysAgo, status); err != nil {
			t.Fatal(err)
		}
	}
	put(ids[0], "applied")
	put(ids[0], "interviewing")
	put(ids[0], "offer")
	age(ids[0], "applied", 20)
	age(ids[0], "interviewing", 10)
	age(ids[0], "offer", 4)
	put(ids[1], "applied")
	put(ids[1], "rejected")
	put(ids[2], "applied")
	put(ids[3], "saved")
	put(ids[3], "saved") // not a change

	a, err = store.ApplicationAnalytics(ctx, pool, me.ID, 12)
	if err != nil {
		t.Fatal(err)
	}
	if a.Now["offer"] != 1 || a.Now["rejected"] != 1 || a.Now["applied"] != 1 || a.Now["saved"] != 1 {
		t.Errorf("now: %v", a.Now)
	}
	if a.Reached.Applied != 3 || a.Reached.Interviewing != 1 || a.Reached.Offer != 1 {
		t.Errorf("reached: %+v", a.Reached)
	}
	if a.DaysToInterview == nil || *a.DaysToInterview < 9.9 || *a.DaysToInterview > 10.1 || a.DaysToOffer == nil || *a.DaysToOffer < 5.9 || *a.DaysToOffer > 6.1 {
		t.Errorf("days: %v %v", a.DaysToInterview, a.DaysToOffer)
	}
	applied, responded := 0, 0
	for _, w := range a.Weeks {
		applied += w.Applied
		responded += w.Responded
	}
	if applied != 3 || responded != 2 {
		t.Errorf("weeks: %d applied, %d responded", applied, responded)
	}
	if len(a.Sources) != 1 || a.Sources[0].Applied != 3 || a.Sources[0].Responded != 2 || a.Sources[0].Interview != 1 {
		t.Errorf("sources: %+v", a.Sources)
	}
	var changes int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM application_changes WHERE user_id = $1 AND job_id = $2`, me.ID, ids[3]).Scan(&changes)
	if changes != 1 {
		t.Errorf("saving twice recorded %d changes", changes)
	}
}
