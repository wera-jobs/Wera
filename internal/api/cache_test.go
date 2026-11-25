package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"wera/internal/store"
	"wera/internal/testutil"
)

func TestTTLCache(t *testing.T) {
	c := newTTLCache(50*time.Millisecond, 2)
	calls := 0
	get := func(k string) any {
		v, _ := c.get(k, func() (any, error) { calls++; return k + "!", nil })
		return v
	}
	first, second := get("a"), get("a")
	if first != "a!" || second != "a!" || calls != 1 {
		t.Fatalf("not cached: %d calls", calls)
	}
	if _, err := c.get("e", func() (any, error) { return nil, errors.New("boom") }); err == nil {
		t.Fatal("error lost")
	}
	if _, err := c.get("e", func() (any, error) { calls++; return 1, nil }); err != nil || calls != 2 {
		t.Fatal("errors must not be cached")
	}
	get("b") // full: a and e fill it, so it resets
	time.Sleep(60 * time.Millisecond)
	get("a")
	if calls != 4 {
		t.Errorf("expired entry served: %d calls", calls)
	}
}

func TestPublicStats(t *testing.T) {
	ts, _ := testServer(t, nil)
	resp, err := http.Get(ts.URL + "/api/public/stats") // no session
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st PublicStats
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil || resp.StatusCode != 200 || st.Industries == 0 {
		t.Fatalf("public stats: %d %+v %v", resp.StatusCode, st, err)
	}
}

func TestPublicRecentJobs(t *testing.T) {
	ts, pool := testServer(t, nil)
	ctx := context.Background()
	// Two postings at one company (only the newer is listed), one at a
	// second company in another industry, and one closed.
	aID, _ := testutil.Company(t, pool, "fintech")
	bID, _ := testutil.Company(t, pool, "healthcare")
	a := testutil.Jobs(t, pool, aID, testutil.Job{Title: "Recent Older"}, testutil.Job{Title: "Recent Newer", Location: "Austin, TX"})
	b := testutil.Jobs(t, pool, bID, testutil.Job{Title: "Recent Other"}, testutil.Job{Title: "Recent Closed"})
	// Ours are the newest postings, so whatever else the database holds,
	// their industries lead the first round.
	if _, err := pool.Exec(ctx, `UPDATE jobs SET first_seen_at = now() + interval '1 minute' WHERE id = ANY($1)`, []int64{a[1], b[0]}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE jobs SET first_seen_at = now() - interval '1 hour' WHERE id = $1`, a[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE jobs SET closed_at = now(), first_seen_at = now() + interval '2 minutes' WHERE id = $1`, b[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE jobs SET pay_min = 120000, pay_max = 150000, pay_currency = 'USD', pay_period = 'year' WHERE id = $1`, a[1]); err != nil {
		t.Fatal(err)
	}
	read := func() (http.Header, []store.RecentJob) {
		t.Helper()
		resp, err := http.Get(ts.URL + "/api/public/recent-jobs") // no session
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body struct {
			Jobs []store.RecentJob `json:"jobs"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || resp.StatusCode != 200 {
			t.Fatalf("recent jobs: %d %v", resp.StatusCode, err)
		}
		return resp.Header, body.Jobs
	}
	header, jobs := read()
	if header.Get("Cache-Control") != "public, max-age=600" {
		t.Errorf("cache header: %q", header.Get("Cache-Control"))
	}
	if len(jobs) > recentJobsShown {
		t.Errorf("%d jobs listed, want at most %d", len(jobs), recentJobsShown)
	}
	seen := map[int64]bool{}
	got := map[int64]store.RecentJob{}
	for _, j := range jobs {
		if seen[j.CompanyID] {
			t.Errorf("company %d listed twice", j.CompanyID)
		}
		seen[j.CompanyID] = true
		got[j.ID] = j
	}
	{
		if _, ok := got[a[0]]; ok {
			t.Error("the older posting of a company was listed")
		}
		if _, ok := got[b[1]]; ok {
			t.Error("a closed posting was listed")
		}
		n, ok := got[a[1]]
		if !ok || n.Company == "" || n.Location != "Austin, TX" || n.PayMin == nil || *n.PayMin != 120000 || n.Source != "greenhouse" {
			t.Errorf("newer posting: %+v (listed %v)", n, ok)
		}
		if _, ok := got[b[0]]; !ok {
			t.Error("the second company's posting is missing")
		}
	}
	// Cached: a posting added now doesn't show for ten minutes.
	c, _ := testutil.Company(t, pool, "other")
	fresh := testutil.Jobs(t, pool, c, testutil.Job{Title: "Recent Fresh"})
	if _, again := read(); len(again) != len(jobs) {
		t.Errorf("cache not used: %d then %d jobs", len(jobs), len(again))
	} else {
		for _, j := range again {
			if j.ID == fresh[0] {
				t.Error("cached feed changed")
			}
		}
	}
}
