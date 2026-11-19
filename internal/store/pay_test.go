package store_test

import (
	"context"
	"testing"

	"wera/internal/store"
	"wera/internal/testutil"
)

// Pay is read when a posting is saved, read in the background for jobs
// saved before, served with every job, and filters the list by a year's pay.
func TestJobPay(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	user := testutil.User(t, pool, false)
	companyID, _ := testutil.Company(t, pool, "other")

	if _, _, err := store.SaveJobs(ctx, pool, []store.JobInput{
		{CompanyID: companyID, Source: "greenhouse", ExtID: testutil.Unique("pay"), Title: "Paid Yearly", URL: "https://example.com/1",
			Description: "The base salary range for this role is $120,000 - $150,000.", ContentHash: testutil.Unique("h")},
		{CompanyID: companyID, Source: "greenhouse", ExtID: testutil.Unique("pay"), Title: "Paid Hourly", URL: "https://example.com/2",
			Description: "Pay range: $25.50 to $31 per hour", ContentHash: testutil.Unique("h")},
		{CompanyID: companyID, Source: "greenhouse", ExtID: testutil.Unique("pay"), Title: "Paid Unstated", URL: "https://example.com/3",
			Description: "We raised $40 million in our Series B.", ContentHash: testutil.Unique("h")},
	}); err != nil {
		t.Fatal(err)
	}
	// One saved before pay was read: the background fill reads it.
	old := testutil.Jobs(t, pool, companyID, testutil.Job{Title: "Paid Before", Description: "Annual salary: $80,000"})[0]
	if _, err := pool.Exec(ctx, `UPDATE jobs SET pay_checked = false WHERE id = $1`, old); err != nil {
		t.Fatal(err)
	}
	if n, err := store.FillPay(ctx, pool, 1000); err != nil || n < 1 {
		t.Fatalf("fill pay: %d, %v", n, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage)
		SELECT $1, id, 'h', 'pending_score' FROM jobs WHERE company_id = $2`, user.ID, companyID); err != nil {
		t.Fatal(err)
	}

	list := func(q store.JobQuery) map[string]store.JobView {
		t.Helper()
		q.Q = "paid"
		jobs, err := store.ListJobs(ctx, pool, user.ID, q)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]store.JobView{}
		for _, j := range jobs {
			out[j.Title] = j
		}
		return out
	}
	all := list(store.JobQuery{})
	if j := all["Paid Yearly"]; j.PayMin == nil || *j.PayMin != 120000 || *j.PayMax != 150000 || *j.PayCurrency != "USD" || *j.PayPeriod != "year" {
		t.Errorf("yearly: %+v", j)
	}
	if j := all["Paid Hourly"]; j.PayMin == nil || *j.PayMin != 25.5 || *j.PayPeriod != "hour" {
		t.Errorf("hourly: %+v", j)
	}
	if j := all["Paid Unstated"]; j.PayMin != nil {
		t.Errorf("funding read as pay: %+v", j)
	}
	if j := all["Paid Before"]; j.PayMin == nil || *j.PayMin != 80000 {
		t.Errorf("backfilled: %+v", j)
	}
	// $31 an hour is about $64,480 a year: under 100k, over 60k.
	if got := list(store.JobQuery{MinPay: 100000}); len(got) != 1 || got["Paid Yearly"].ID == 0 {
		t.Errorf("min pay 100k: %v", got)
	}
	if got := list(store.JobQuery{MinPay: 60000}); len(got) != 3 {
		t.Errorf("min pay 60k: %d jobs", len(got))
	}
}
