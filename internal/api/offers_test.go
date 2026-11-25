package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"wera/internal/auth"
	"wera/internal/h1b"
	"wera/internal/store"
	"wera/internal/testutil"
)

func TestOffers(t *testing.T) {
	ts, pool := testServer(t, nil)
	ctx := context.Background()
	// A filing employer with a job titled like its filings, and one without filings.
	cid, name := testutil.Company(t, pool, "ai_ml")
	filer := testutil.Jobs(t, pool, cid, testutil.Job{Title: "Software Engineer"})[0]
	plain := testJobIDs[0]
	if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage) VALUES ($1, $2, 'h', 'scored')`, testUserID, filer); err != nil {
		t.Fatal(err)
	}
	prefix := testutil.Unique("I-300")
	w := store.NewH1BWriter(pool)
	wage := func(v float64) *float64 { return &v }
	for i, c := range []h1b.Case{
		{JobTitle: "Software Engineer", WageFrom: wage(140000), WageLevel: "II"},
		{JobTitle: "Software Engineer", WageFrom: wage(160000), WageLevel: "II"},
		{JobTitle: "Software Engineer", WageFrom: wage(180000), WageLevel: "III"},
		{JobTitle: "Accountant", WageFrom: wage(80000), WageLevel: "I"},
	} {
		c.CaseNumber = fmt.Sprintf("%s-%d", prefix, i)
		c.EmployerName = name + " LLC"
		c.EmployerKey = h1b.EmployerKey(c.EmployerName)
		c.DecisionDate = time.Date(2026, 4, 1+i, 0, 0, 0, 0, time.UTC)
		c.FiscalYear = h1b.FiscalYear(c.DecisionDate)
		c.WorksiteCity, c.WorksiteState, c.Positions, c.FullTime = "Austin", "TX", 1, true
		if err := w.Add(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Done(ctx, "offers-test.xlsx", 4, 4); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM h1b_lca WHERE case_number LIKE $1 || '%'`, prefix)
		pool.Exec(ctx, `DELETE FROM h1b_imports WHERE source = 'offers-test.xlsx'`)
	})
	if _, err := store.MatchH1BEmployers(ctx, pool, nil); err != nil {
		t.Fatal(err)
	}

	if code, _ := do(t, client, http.MethodGet, jobURL(ts, filer, "/offer"), ""); code != 404 {
		t.Errorf("no offer yet: want 404, got %d", code)
	}
	for _, bad := range []string{
		`{"base_salary":-1}`,
		`{"bonus_pct":5000}`,
		`{"currency":"dollars"}`,
		`{"work_mode":"moon"}`,
		`{"notes":"` + strings.Repeat("x", 5001) + `"}`,
	} {
		if code, body := do(t, client, http.MethodPut, jobURL(ts, filer, "/offer"), bad); code != 400 {
			t.Errorf("PUT %.40s: want 400, got %d %s", bad, code, body)
		}
	}
	code, body := do(t, client, http.MethodPut, jobURL(ts, filer, "/offer"),
		`{"base_salary":150000,"bonus_pct":10,"annual_equity":40000,"sign_on":20000,"work_mode":"hybrid","location":"Austin, TX","deadline_at":"2026-12-01T00:00:00Z"}`)
	var v struct {
		store.Offer
		AnnualTotal    *float64        `json:"annual_total"`
		FirstYearTotal *float64        `json:"first_year_total"`
		Benchmark      *OfferBenchmark `json:"benchmark"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &v) != nil || v.Currency != "USD" || v.AnnualTotal == nil || *v.AnnualTotal != 205000 ||
		*v.FirstYearTotal != 225000 {
		t.Fatalf("put: %d %s", code, body)
	}
	b := v.Benchmark
	if b == nil || b.EmployerFilings != 4 || b.EmployerMedianWage == nil || *b.EmployerMedianWage != 150000 ||
		b.TitleFilings != 3 || b.TitleMedianWage == nil || *b.TitleMedianWage != 160000 {
		t.Fatalf("benchmark: %s", body)
	}
	// A put replaces the offer.
	if code, body := do(t, client, http.MethodPut, jobURL(ts, filer, "/offer"), `{"base_salary":155000}`); code != 200 ||
		!strings.Contains(body, `"bonus_pct":null`) || !strings.Contains(body, `"annual_total":155000`) {
		t.Errorf("replace: %d %s", code, body)
	}
	// Not in dollars: no benchmark. No filings: none either.
	if code, body := do(t, client, http.MethodPut, jobURL(ts, plain, "/offer"), `{"base_salary":90000,"currency":"eur"}`); code != 200 ||
		!strings.Contains(body, `"currency":"EUR"`) || !strings.Contains(body, `"benchmark":null`) {
		t.Errorf("euro offer: %d %s", code, body)
	}

	code, body = do(t, client, http.MethodGet, ts.URL+"/api/offers", "")
	var list struct {
		Offers []json.RawMessage `json:"offers"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &list) != nil || len(list.Offers) != 2 || !strings.Contains(body, `"job_title":"Software Engineer"`) ||
		!strings.Contains(body, `"employer_median_wage":150000`) {
		t.Fatalf("list: %d %s", code, body)
	}

	// Another user: not their job, not their offers.
	other := testutil.User(t, pool, false)
	token, hash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession(ctx, pool, other.ID, hash, time.Hour); err != nil {
		t.Fatal(err)
	}
	them := &http.Client{Transport: cookieTransport{token}}
	if code, _ := do(t, them, http.MethodPut, jobURL(ts, filer, "/offer"), `{"base_salary":1}`); code != 404 {
		t.Errorf("other user's put: want 404, got %d", code)
	}
	if code, _ := do(t, them, http.MethodGet, jobURL(ts, filer, "/offer"), ""); code != 404 {
		t.Errorf("other user's get: want 404, got %d", code)
	}
	if _, body := do(t, them, http.MethodGet, ts.URL+"/api/offers", ""); !strings.Contains(body, `"offers":[]`) {
		t.Errorf("other user's list: %s", body)
	}

	if code, _ := do(t, client, http.MethodDelete, jobURL(ts, filer, "/offer"), ""); code != 204 {
		t.Errorf("delete: %d", code)
	}
	if code, _ := do(t, client, http.MethodDelete, jobURL(ts, filer, "/offer"), ""); code != 404 {
		t.Errorf("delete again: %d", code)
	}
}
