package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"wera/internal/testutil"
)

func TestUSState(t *testing.T) {
	for in, want := range map[string]string{
		"Austin, TX": "TX", "Seattle, Washington": "WA", "Charleston, West Virginia": "WV", "Remote, US": "",
		"London, UK": "", "San Francisco, CA or Remote": "CA", "New York City": "NY",
	} {
		if got := usState(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	if wageLevelFor("Senior") != "III" || wageLevelFor("entry") != "I" || wageLevelFor("") != "" {
		t.Error("wage levels")
	}
	if got := compare(150000, 180000, 120000, "x"); !strings.Contains(got, "above") {
		t.Errorf("above: %s", got)
	}
	if got := compare(90000, 100000, 150000, "x"); !strings.Contains(got, "below") {
		t.Errorf("below: %s", got)
	}
	if got := compare(100000, 160000, 130000, "x"); !strings.Contains(got, "takes in") {
		t.Errorf("within: %s", got)
	}
}

func TestPayInsight(t *testing.T) {
	ts, pool := testServer(t, nil)
	ctx := context.Background()
	companyID, _ := testutil.Company(t, pool, "other")
	ids := testutil.Jobs(t, pool, companyID, testutil.Job{Title: "Payscope Data Engineer", Location: "Austin, TX"}, testutil.Job{Title: "Payscope Data Engineer Unpaid", Location: "Remote"})
	for _, id := range ids {
		if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage) VALUES ($1, $2, 'h', 'scored') ON CONFLICT DO NOTHING`, testUserID, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE jobs SET pay_min = 140000, pay_max = 170000, pay_currency = 'USD', pay_period = 'year' WHERE id = $1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	key := testutil.Unique("payscope")
	if _, err := pool.Exec(ctx, `INSERT INTO company_h1b_employers (company_id, employer_key) VALUES ($1, $2)`, companyID, key); err != nil {
		t.Fatal(err)
	}
	for i, w := range []int{110000, 115000, 120000, 125000, 130000, 135000} {
		if _, err := pool.Exec(ctx, `INSERT INTO h1b_lca (case_number, fiscal_year, decision_date, employer_name, employer_key, job_title, wage_from, worksite_state)
			VALUES ($1, 2026, '2026-03-01', 'Payscope', $2, 'Payscope Data Engineer', $3, 'TX')`, fmt.Sprintf("%s-%d", key, i), key, w); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM h1b_lca WHERE employer_key = $1`, key)
		pool.Exec(context.Background(), `DELETE FROM company_h1b_employers WHERE employer_key = $1`, key)
	})
	get := func(id int64) (out struct {
		Posted  *struct{ Low, High float64 }
		Company *struct {
			Median float64
			Count  int
		}
		Place *struct {
			State string
			Count int
		}
		Says []string
	}) {
		t.Helper()
		code, body := do(t, client, http.MethodGet, fmt.Sprintf("%s/api/jobs/%d/pay-insight", ts.URL, id), "")
		if code != 200 {
			t.Fatalf("pay insight: %d %s", code, body)
		}
		_ = json.Unmarshal([]byte(body), &out)
		return out
	}
	got := get(ids[0])
	if got.Posted == nil || got.Posted.Low != 140000 || got.Company == nil || got.Company.Count != 6 || got.Place == nil || got.Place.State != "TX" || len(got.Says) != 2 || !strings.Contains(got.Says[0], "above") {
		t.Errorf("insight: %+v", got)
	}
	// No posted pay: the wages, but nothing compared.
	if other := get(ids[1]); other.Posted != nil || len(other.Says) != 0 || other.Company == nil {
		t.Errorf("no posted pay: %+v", other)
	}
}
