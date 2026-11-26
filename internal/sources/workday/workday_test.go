package workday

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"wera/internal/sources"
)

func newTestAdapter(t *testing.T) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const base = "/wday/cxs/acme/Careers"
		switch {
		case r.Method == http.MethodPost && r.URL.Path == base+"/jobs":
			var req searchRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Limit != pageSize {
				t.Errorf("bad search request: %+v %v", req, err)
			}
			if req.Offset > 0 {
				w.Write([]byte(`{"total":0,"jobPostings":[]}`))
				return
			}
			serve(t, w, "testdata/page0.json")
		case r.Method == http.MethodGet && r.URL.Path == base+"/job/US-TX-Dallas/Data-Scientist_JR2":
			serve(t, w, "testdata/detail.json")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return NewWithBaseURL(sources.NewHTTP("Wera/test"), srv.URL)
}

func serve(t *testing.T, w http.ResponseWriter, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func TestListAndDetail(t *testing.T) {
	a := newTestAdapter(t)
	jobs, err := a.List(context.Background(), "acme.wd5/Careers")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("want 2 deduplicated jobs, got %d", len(jobs))
	}
	j := jobs[1]
	if j.ExtID != "/job/US-TX-Dallas/Data-Scientist_JR2" ||
		j.URL != "https://acme.wd5.myworkdayjobs.com/Careers/job/US-TX-Dallas/Data-Scientist_JR2" {
		t.Errorf("listed job: %+v", j)
	}
	if err := a.Detail(context.Background(), "acme.wd5/Careers", &j); err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if j.Title != "Data Scientist, Flight Operations" {
		t.Errorf("title: %q", j.Title)
	}
	if j.LocationRaw != "US, TX, Dallas; US, Remote (United States of America)" {
		t.Errorf("location: %q", j.LocationRaw)
	}
	if !strings.Contains(j.DescriptionText, "Build models for flight delays") {
		t.Errorf("description: %q", j.DescriptionText)
	}
	if j.PostedAt == nil || !j.PostedAt.Equal(time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("posted: %v", j.PostedAt)
	}
	if j.IsRemote == nil || !*j.IsRemote {
		t.Error("want remote")
	}
}

func TestParseToken(t *testing.T) {
	s, err := parseToken("nvidia.wd5/NVIDIAExternalCareerSite")
	if err != nil || s.tenant != "nvidia" || s.host != "https://nvidia.wd5.myworkdayjobs.com" || s.name != "NVIDIAExternalCareerSite" {
		t.Errorf("parse: %+v %v", s, err)
	}
	for _, bad := range []string{"nvidia", "nvidia/site", "nvidia.wd5/", ".wd5/x"} {
		if _, err := parseToken(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestMaintenanceWindow(t *testing.T) {
	a := New(sources.NewHTTP("Wera/test"))
	pt := func(day, h, minute int) time.Time { return time.Date(2026, 10, day, h, minute, 0, 0, pacific) }
	end := pt(10, 3, 0) // Saturday 3 a.m.
	tests := []struct {
		now     time.Time
		inside  bool
		wantEnd time.Time
	}{
		{pt(9, 22, 59), false, time.Time{}}, // Friday before 11 p.m.
		{pt(9, 23, 0), true, end},           // Friday 11 p.m.
		{pt(10, 0, 30), true, end},          // Saturday 12:30 a.m.
		{pt(10, 2, 59), true, end},
		{pt(10, 3, 0), false, time.Time{}},  // Saturday 3 a.m.: open again
		{pt(8, 23, 30), false, time.Time{}}, // Thursday night
	}
	for _, tc := range tests {
		got, inside := a.MaintenanceUntil(tc.now.UTC())
		if inside != tc.inside || !got.Equal(tc.wantEnd) {
			t.Errorf("%s: got %v %v, want %v %v", tc.now, got, inside, tc.wantEnd, tc.inside)
		}
	}
}

func TestMaintenanceRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/maintenance-page" {
			w.Write([]byte("<html>down</html>"))
			return
		}
		http.Redirect(w, r, "/maintenance-page", http.StatusSeeOther)
	}))
	t.Cleanup(srv.Close)
	a := NewWithBaseURL(sources.NewHTTP("Wera/test"), srv.URL)
	if _, err := a.List(context.Background(), "acme.wd5/Careers"); !errors.Is(err, sources.ErrUnavailable) {
		t.Errorf("want ErrUnavailable, got %v", err)
	}
}

// A board of 45 postings, newest first; pages of 20.
func boardServer(t *testing.T, pages *int) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req searchRequest
		json.NewDecoder(r.Body).Decode(&req)
		*pages++
		var items []string
		for id := req.Offset + 1; id <= min(req.Offset+req.Limit, 45); id++ {
			items = append(items, fmt.Sprintf(`{"title":"Job %d","externalPath":"/job/%d"}`, id, id))
		}
		fmt.Fprintf(w, `{"total":45,"jobPostings":[%s]}`, strings.Join(items, ","))
	}))
	t.Cleanup(srv.Close)
	return NewWithBaseURL(sources.NewHTTP("Wera/test"), srv.URL)
}

func TestListNewStopsAtKnownPostings(t *testing.T) {
	known := map[string]bool{}
	for id := 4; id <= 45; id++ {
		known[fmt.Sprintf("/job/%d", id)] = true // only 1-3 are new
	}
	pages := 0
	jobs, complete, err := boardServer(t, &pages).ListNew(context.Background(), "acme.wd5/Careers", known)
	if err != nil || complete || pages != 2 || len(jobs) != 40 {
		t.Fatalf("got %d jobs, complete=%v, %d pages, %v; want 40 jobs from 2 pages, incomplete", len(jobs), complete, pages, err)
	}

	pages = 0
	jobs, complete, err = boardServer(t, &pages).ListNew(context.Background(), "acme.wd5/Careers", map[string]bool{})
	if err != nil || !complete || pages != 3 || len(jobs) != 45 {
		t.Fatalf("all new: got %d jobs, complete=%v, %d pages, %v; want the whole board", len(jobs), complete, pages, err)
	}
}

// A deep page that fails makes the listing partial: the postings it did
// get come back, flagged, so a full sweep closes nothing.
func TestListReportsAFailedPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req searchRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Offset == 20 {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		var items []string
		for id := req.Offset + 1; id <= min(req.Offset+req.Limit, 45); id++ {
			items = append(items, fmt.Sprintf(`{"title":"Job %d","externalPath":"/job/%d"}`, id, id))
		}
		fmt.Fprintf(w, `{"total":45,"jobPostings":[%s]}`, strings.Join(items, ","))
	}))
	t.Cleanup(srv.Close)
	jobs, err := NewWithBaseURL(sources.NewHTTP("Wera/test"), srv.URL).List(context.Background(), "acme.wd5/Careers")
	if !errors.Is(err, sources.ErrPartialList) || len(jobs) != 25 {
		t.Fatalf("got %d jobs, %v; want the 25 from the pages that worked and ErrPartialList", len(jobs), err)
	}
	pages := 0
	if jobs, err := boardServer(t, &pages).List(context.Background(), "acme.wd5/Careers"); err != nil || len(jobs) != 45 {
		t.Fatalf("whole board: %d jobs, %v", len(jobs), err)
	}
}

// A board larger than a full sweep lists comes back truncated: the
// sweep reads past the incremental cap, then flags the rest so nothing
// is closed for being missing.
func TestListReportsATruncatedBoard(t *testing.T) {
	const total = maxSwept + 300
	old := throttle
	throttle = sources.NewThrottle(4, 0) // a hundred pages at the real pace take 15 s
	t.Cleanup(func() { throttle = old })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req searchRequest
		json.NewDecoder(r.Body).Decode(&req)
		var items []string
		for id := req.Offset + 1; id <= min(req.Offset+req.Limit, total); id++ {
			items = append(items, fmt.Sprintf(`{"title":"Job %d","externalPath":"/job/%d"}`, id, id))
		}
		fmt.Fprintf(w, `{"total":%d,"jobPostings":[%s]}`, total, strings.Join(items, ","))
	}))
	t.Cleanup(srv.Close)
	jobs, err := NewWithBaseURL(sources.NewHTTP("Wera/test"), srv.URL).List(context.Background(), "acme.wd5/Careers")
	if !errors.Is(err, sources.ErrTruncatedList) || errors.Is(err, sources.ErrPartialList) || len(jobs) != maxSwept {
		t.Fatalf("got %d jobs, %v; want %d and ErrTruncatedList", len(jobs), err, maxSwept)
	}
}
