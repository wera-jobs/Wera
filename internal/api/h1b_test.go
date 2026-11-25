package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"wera/internal/h1b"
	"wera/internal/store"
	"wera/internal/testutil"
)

func TestH1BEndpoints(t *testing.T) {
	ts, pool := testServer(t, nil)
	ctx := context.Background()
	cid, name := testutil.Company(t, pool, "ai_ml")
	jobs := testutil.Jobs(t, pool, cid, testutil.Job{Title: "Senior Software Engineer, Payments", Location: "Seattle, WA"})

	prefix := testutil.Unique("I-200")
	w := store.NewH1BWriter(pool)
	wage := func(v float64) *float64 { return &v }
	for i, c := range []h1b.Case{
		{JobTitle: "Software Engineer", WageFrom: wage(150000), WageLevel: "II", WorksiteCity: "Seattle", WorksiteState: "WA", NewEmployment: true},
		{JobTitle: "Senior Software Engineer", WageFrom: wage(190000), WageTo: wage(240000), WageLevel: "III", WorksiteCity: "Seattle", WorksiteState: "WA"},
		{JobTitle: "Data Analyst", WageFrom: wage(110000), WageLevel: "I", WorksiteCity: "Austin", WorksiteState: "TX"},
	} {
		c.CaseNumber = fmt.Sprintf("%s-%d", prefix, i)
		c.EmployerName = name + ", Inc."
		c.EmployerKey = h1b.EmployerKey(c.EmployerName)
		c.DecisionDate = time.Date(2026, 3, 1+i, 0, 0, 0, 0, time.UTC)
		c.FiscalYear = h1b.FiscalYear(c.DecisionDate)
		c.Positions = 1
		c.FullTime = true
		if err := w.Add(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Done(ctx, "test.xlsx", 3, 3); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM h1b_lca WHERE case_number LIKE $1 || '%'`, prefix)
		pool.Exec(ctx, `DELETE FROM h1b_imports WHERE source = 'test.xlsx'`)
	})
	if _, err := store.MatchH1BEmployers(ctx, pool, nil); err != nil {
		t.Fatal(err)
	}

	// The sponsorship list carries the filings, even with no posting signal.
	code, body := getBody(t, ts.URL+"/api/sponsorship?q="+url.QueryEscape(name))
	var list struct {
		Companies []store.SponsorshipStat `json:"companies"`
	}
	json.Unmarshal([]byte(body), &list)
	if code != 200 || len(list.Companies) != 1 || list.Companies[0].H1BFilings == nil || *list.Companies[0].H1BFilings != 3 ||
		*list.Companies[0].H1BNewHires != 1 {
		t.Fatalf("sponsorship: %d %s", code, body)
	}
	// Pages: only filers, one row at a time, with the total that match.
	code, body = getBody(t, ts.URL+"/api/sponsorship?filers=1&limit=1&offset=0&q="+url.QueryEscape(name))
	var page struct {
		Companies []store.SponsorshipStat `json:"companies"`
		Total     int                     `json:"total"`
	}
	json.Unmarshal([]byte(body), &page)
	if code != 200 || page.Total != 1 || len(page.Companies) != 1 {
		t.Errorf("filers page: %d %s", code, body)
	}
	if _, body = getBody(t, ts.URL+"/api/sponsorship?limit=1&offset=5&q="+url.QueryEscape(name)); !strings.Contains(body, `"companies":[]`) {
		t.Errorf("past the end: %s", body)
	}

	code, body = getBody(t, fmt.Sprintf("%s/api/sponsorship/%d/h1b", ts.URL, cid))
	var d store.H1BDetail
	json.Unmarshal([]byte(body), &d)
	if code != 200 || d.Filings != 3 || len(d.Titles) != 3 || d.Places[0].Label != "Seattle, WA" || *d.WageMedian != 150000 {
		t.Fatalf("detail: %d %s", code, body)
	}

	code, body = getBody(t, fmt.Sprintf("%s/api/jobs/%d/h1b?level=II", ts.URL, jobs[0]))
	var j store.JobH1B
	json.Unmarshal([]byte(body), &j)
	// Level II first: the one level II filing leads, though less similar
	// (too few at that level to set the range on their own).
	if code != 200 || !j.Matched || len(j.Similar) != 2 || j.Similar[0].Title != "Software Engineer" || j.Level != "" {
		t.Fatalf("job: %d %s", code, body)
	}
	for _, s := range j.Similar {
		if s.Title == "Data Analyst" {
			t.Errorf("an unrelated title counted as similar: %s", body)
		}
	}

	code, body = getBody(t, ts.URL+"/api/h1b/employers?q="+url.QueryEscape(name))
	var found struct {
		Employers []store.H1BEmployer `json:"employers"`
	}
	json.Unmarshal([]byte(body), &found)
	if code != 200 || len(found.Employers) != 1 || found.Employers[0].CompanyID == nil || *found.Employers[0].CompanyID != cid {
		t.Fatalf("search: %d %s", code, body)
	}
	code, _ = getBody(t, ts.URL+"/api/h1b/employer?key="+url.QueryEscape(found.Employers[0].Key))
	if code != 200 {
		t.Errorf("employer detail: %d", code)
	}
	if code, _ = getBody(t, ts.URL+"/api/h1b/employer?key=nobody+at+all"); code != 404 {
		t.Errorf("unknown employer: %d", code)
	}

	// Signed out: the search and a summary (no places), per IP limited.
	anon := func(ip, path string) (int, string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.Header.Set("X-Real-IP", ip)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	ip := "198.51.100.23"
	if code, body, _ := anon(ip, "/api/h1b/employers?q="+url.QueryEscape(name)); code != 401 {
		t.Errorf("signed-out search on the account route: %d %s", code, body)
	}
	code, body, _ = anon(ip, "/api/public/h1b/employers?q="+url.QueryEscape(name))
	if code != 200 || !strings.Contains(body, found.Employers[0].Key) {
		t.Fatalf("public search: %d %s", code, body)
	}
	code, body, header := anon(ip, "/api/public/h1b/employer?key="+url.QueryEscape(found.Employers[0].Key))
	var sum PublicH1BSummary
	json.Unmarshal([]byte(body), &sum)
	if code != 200 || sum.Filings != 3 || sum.NewHires != 1 || sum.WageMedian == nil || *sum.WageMedian != 150000 ||
		len(sum.Levels) != 3 || len(sum.Titles) != 3 || sum.Name != name+", Inc." || strings.Contains(body, "Seattle") {
		t.Fatalf("public summary: %d %s", code, body)
	}
	if header.Get("Cache-Control") != "public, max-age=1800" {
		t.Errorf("public summary cache header: %q", header.Get("Cache-Control"))
	}
	if code, _, _ := anon(ip, "/api/public/h1b/employer?key=nobody+at+all"); code != 404 {
		t.Errorf("public unknown employer: %d", code)
	}
	// 3 lookups so far from this IP; the limit is per IP and counts cached answers.
	for i := 3; i < publicH1BPerWindow; i++ {
		if code, _, _ := anon(ip, "/api/public/h1b/employers?q="+url.QueryEscape(name)); code != 200 {
			t.Fatalf("lookup %d: %d", i+1, code)
		}
	}
	code, _, header = anon(ip, "/api/public/h1b/employer?key="+url.QueryEscape(found.Employers[0].Key))
	if code != 429 || header.Get("Retry-After") == "" {
		t.Errorf("over the limit: %d, Retry-After %q", code, header.Get("Retry-After"))
	}
	if code, _, _ := anon("198.51.100.24", "/api/public/h1b/employers?q="+url.QueryEscape(name)); code != 200 {
		t.Errorf("another IP was limited: %d", code)
	}
}
