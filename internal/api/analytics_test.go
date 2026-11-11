package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"wera/internal/store"
)

const (
	chromeDesktop = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36"
	safariPhone   = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"
)

func TestPageSeenAndTraffic(t *testing.T) {
	var srv *Server
	ts, pool := testServerWith(t, nil, func(s *Server) { srv = s })
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DELETE FROM web_events; DELETE FROM api_requests_daily`); err != nil {
		t.Fatal(err)
	}
	beacon := func(ip, ua, body string, extra ...string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/page-seen", strings.NewReader(body))
		req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		req.Header.Set("Origin", ts.URL)
		req.Header.Set("User-Agent", ua)
		req.Header.Set("X-Real-IP", ip)
		req.Header.Set("X-Vercel-IP-Country", "KE")
		for i := 0; i+1 < len(extra); i += 2 {
			req.Header.Set(extra[i], extra[i+1])
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("beacon: %d", res.StatusCode)
		}
	}
	// One visitor reads the landing page for 3 s and leaves: a bounce.
	beacon("198.51.100.1", chromeDesktop, `{"k":"view","p":"/","r":"https://www.linkedin.com/feed/","us":"linkedin","uc":"launch"}`)
	beacon("198.51.100.1", chromeDesktop, `{"k":"leave","p":"/","e":3000}`)
	// Another signs in and opens a job: two pages, no bounce. The job's id never reaches the database.
	beacon("198.51.100.2", safariPhone, `{"k":"view","p":"/"}`)
	beacon("198.51.100.2", safariPhone, `{"k":"view","p":"/jobs/12345?tab=letter"}`)
	beacon("198.51.100.2", safariPhone, `{"k":"leave","p":"/jobs/12345","e":20000}`)
	// Not counted: a crawler, a browser asking not to be tracked, nonsense.
	beacon("198.51.100.3", "Googlebot/2.1 (+http://www.google.com/bot.html)", `{"k":"view","p":"/"}`)
	beacon("198.51.100.4", chromeDesktop, `{"k":"view","p":"/"}`, "Sec-GPC", "1")
	beacon("198.51.100.5", chromeDesktop, `{"k":"click","p":"/"}`)
	beacon("198.51.100.5", chromeDesktop, `{"k":"view","p":"https://evil.example/"}`)

	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM web_events WHERE path LIKE '%12345%' OR visitor LIKE '198.%'`).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("an id or address was stored: %d %v", stored, err)
	}

	code, body := do(t, client, http.MethodGet, ts.URL+"/api/admin/traffic?days=7&tz=America/Chicago", "")
	if code != 200 {
		t.Fatalf("traffic: %d %s", code, body)
	}
	var v store.TrafficView
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	tot := v.Totals
	if tot.Visitors != 2 || tot.Pageviews != 3 || tot.Sessions != 2 || tot.BounceRate != 50 || tot.LiveVisitors != 2 {
		t.Errorf("totals: %+v", tot)
	}
	if len(v.PerDay) != 7 || v.PerDay[6].Pageviews != 3 {
		t.Errorf("per day: %+v", v.PerDay)
	}
	has := func(rows []store.TrafficRow, label string) *store.TrafficRow {
		for i := range rows {
			if rows[i].Label == label {
				return &rows[i]
			}
		}
		t.Errorf("no %q in %+v", label, rows)
		return nil
	}
	if p := has(v.Pages, "/jobs/:id"); p != nil && p.EngagedS != 20 {
		t.Errorf("job page engagement: %+v", p)
	}
	if l := has(v.Landing, "/"); l != nil && (l.Sessions != 2 || l.Bounce != 50) {
		t.Errorf("landing: %+v", l)
	}
	has(v.Referrers, "linkedin.com")
	has(v.Campaigns, "linkedin / launch")
	has(v.Devices, "Mobile")
	has(v.Browsers, "Safari")
	has(v.Systems, "iOS")
	has(v.Countries, "KE")

	// Requests are counted per route and written about once a minute.
	srv.requests.mu.Lock()
	srv.requests.flushed = time.Now().Add(-2 * flushEvery)
	srv.requests.mu.Unlock()
	do(t, client, http.MethodGet, ts.URL+"/api/auth/me", "")
	deadline := time.Now().Add(5 * time.Second)
	var n int64
	for time.Now().Before(deadline) {
		_ = pool.QueryRow(ctx, `SELECT coalesce(sum(requests), 0) FROM api_requests_daily WHERE route = '/api/page-seen'`).Scan(&n)
		if n > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n != 9 {
		t.Errorf("page-seen requests counted: %d, want 9", n)
	}
}

func TestCleanPath(t *testing.T) {
	for in, want := range map[string]string{
		"/":         "/",
		"/jobs/123": "/jobs/:id",
		"/reset-password/0f8fad5b-d9cb-469f-a165": "/reset-password/:id",
		"/jobs/?q=secret":                         "/jobs",
		"https://x.example/":                      "",
	} {
		if got := cleanPath(in); got != want {
			t.Errorf("cleanPath(%q) = %q, want %q", in, got, want)
		}
	}
}
