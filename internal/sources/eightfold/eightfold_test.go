package eightfold

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"wera/internal/sources"
)

// 23 positions over pages of 10; position 5 appears twice.
func newTestAdapter(t *testing.T) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("domain") != "acme.com" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/api/apply/v2/jobs/7" {
			w.Write([]byte(`{"id":7,"job_description":"<p>Stream <b>video</b>.</p>"}`))
			return
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		var items []string
		for id := start + 1; id <= min(start+10, 23); id++ {
			items = append(items, fmt.Sprintf(`{"id":%d,"name":"Engineer %d","locations":["Los Gatos,California,United States of America"],`+
				`"department":"Eng","t_create":1791417600,"work_location_option":"onsite","canonicalPositionUrl":"https://jobs.acme.com/careers/job/%d"}`, id, id, id))
		}
		if start == 10 {
			items = append(items, `{"id":5,"name":"Engineer 5"}`)
		}
		fmt.Fprintf(w, `{"count":23,"positions":[%s]}`, join(items))
	}))
	t.Cleanup(srv.Close)
	return NewWithBaseURL(sources.NewHTTP("Wera/test"), srv.URL)
}

func join(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

func TestListAndDetail(t *testing.T) {
	a := newTestAdapter(t)
	jobs, err := a.List(context.Background(), "jobs.acme.com/acme.com")
	if err != nil || len(jobs) != 23 {
		t.Fatalf("List: %d jobs, %v", len(jobs), err)
	}
	j := jobs[6]
	if j.ExtID != "7" || j.Title != "Engineer 7" || j.URL != "https://jobs.acme.com/careers/job/7" ||
		j.LocationRaw != "Los Gatos,California,United States of America" || j.PostedAt == nil || *j.IsRemote {
		t.Errorf("job 7: %+v", j)
	}
	if err := a.Detail(context.Background(), "jobs.acme.com/acme.com", &j); err != nil {
		t.Fatal(err)
	}
	if j.DescriptionText != "Stream video." {
		t.Errorf("description: %q", j.DescriptionText)
	}
	if _, err := a.List(context.Background(), "no-domain"); err == nil {
		t.Error("bad token accepted")
	}
}

// A deep page that fails makes the listing partial: the postings it did
// get come back, flagged, so a full sweep closes nothing.
func TestListReportsAFailedPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		if start == 10 {
			http.Error(w, "too many requests", http.StatusServiceUnavailable)
			return
		}
		var items []string
		for id := start + 1; id <= min(start+10, 23); id++ {
			items = append(items, fmt.Sprintf(`{"id":%d,"name":"Engineer %d"}`, id, id))
		}
		fmt.Fprintf(w, `{"count":23,"positions":[%s]}`, join(items))
	}))
	t.Cleanup(srv.Close)
	jobs, err := NewWithBaseURL(sources.NewHTTP("Wera/test"), srv.URL).List(context.Background(), "jobs.acme.com/acme.com")
	if !errors.Is(err, sources.ErrPartialList) || len(jobs) != 13 {
		t.Fatalf("got %d jobs, %v; want the 13 from the pages that worked and ErrPartialList", len(jobs), err)
	}
}

// pagedBoard serves count positions, newest first, and counts requests.
func pagedBoard(t *testing.T, count int, requests *atomic.Int32) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		var items []string
		for id := start + 1; id <= min(start+pageSize, count); id++ {
			items = append(items, fmt.Sprintf(`{"id":%d,"name":"Engineer %d"}`, id, id))
		}
		fmt.Fprintf(w, `{"count":%d,"positions":[%s]}`, count, join(items))
	}))
	t.Cleanup(srv.Close)
	return NewWithBaseURL(sources.NewHTTP("Wera/test"), srv.URL)
}

func TestListNewStopsAtKnownPostings(t *testing.T) {
	known := map[string]bool{}
	for id := 4; id <= 45; id++ {
		known[strconv.Itoa(id)] = true // only 1-3 are new
	}
	var requests atomic.Int32
	jobs, complete, err := pagedBoard(t, 45, &requests).ListNew(context.Background(), "jobs.acme.com/acme.com", known)
	if err != nil || complete || requests.Load() != 2 || len(jobs) != 20 {
		t.Fatalf("got %d jobs, complete=%v, %d requests, %v; want 20 jobs from 2 pages, incomplete", len(jobs), complete, requests.Load(), err)
	}

	for id := 1; id <= 3; id++ {
		known[strconv.Itoa(id)] = true
	}
	requests.Store(0)
	if _, complete, err := pagedBoard(t, 45, &requests).ListNew(context.Background(), "jobs.acme.com/acme.com", known); err != nil || complete || requests.Load() != 1 {
		t.Fatalf("quiet board: complete=%v, %d requests, %v; want one request", complete, requests.Load(), err)
	}

	requests.Store(0)
	jobs, complete, err = pagedBoard(t, 45, &requests).ListNew(context.Background(), "jobs.acme.com/acme.com", map[string]bool{})
	if err != nil || !complete || requests.Load() != 5 || len(jobs) != 45 {
		t.Fatalf("all new: got %d jobs, complete=%v, %d requests, %v; want the whole board", len(jobs), complete, requests.Load(), err)
	}
}

func TestListReportsATruncatedBoard(t *testing.T) {
	var requests atomic.Int32
	jobs, err := pagedBoard(t, maxListed+25, &requests).List(context.Background(), "jobs.acme.com/acme.com")
	if !errors.Is(err, sources.ErrTruncatedList) || len(jobs) != maxListed {
		t.Fatalf("got %d jobs, %v; want %d and ErrTruncatedList", len(jobs), err, maxListed)
	}
}
