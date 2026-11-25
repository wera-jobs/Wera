package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"wera/internal/store"
)

// PublicStats are the headline numbers for the landing page.
type PublicStats struct {
	Companies    int64 `json:"companies"`
	Industries   int   `json:"industries"`
	OpenJobs     int64 `json:"open_jobs"`
	H1BFilings   int64 `json:"h1b_filings"`
	H1BEmployers int64 `json:"h1b_employers"`
}

// publicStats is GET /api/public/stats (no sign-in): what Wera watches.
func (s *Server) publicStats(w http.ResponseWriter, r *http.Request) {
	v, err := s.sponsorCache.get("public-stats", func() (any, error) {
		st := PublicStats{Industries: len(s.Industries)}
		ctx := context.WithoutCancel(r.Context())
		err := s.Pool.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM companies WHERE enabled),
			       (SELECT count(*) FROM jobs WHERE closed_at IS NULL AND source <> 'manual'),
			       (SELECT count(*) FROM h1b_lca),
			       (SELECT count(DISTINCT employer_key) FROM h1b_lca)`).
			Scan(&st.Companies, &st.OpenJobs, &st.H1BFilings, &st.H1BEmployers)
		return st, err
	})
	if err != nil {
		s.Log.Error("public stats failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=600")
	s.writeJSON(w, http.StatusOK, v)
}

// recentJobsShown is how many postings the landing page's feed lists.
const recentJobsShown = 20

// publicRecentJobs is GET /api/public/recent-jobs (no sign-in): postings
// Wera found in the last few days, one per company, across industries.
func (s *Server) publicRecentJobs(w http.ResponseWriter, r *http.Request) {
	v, err := s.sponsorCache.get("public-recent-jobs", func() (any, error) {
		jobs, err := store.RecentJobs(context.WithoutCancel(r.Context()), s.Pool, recentJobsShown)
		return map[string]any{"jobs": jobs}, err
	})
	if err != nil {
		s.Log.Error("public recent jobs failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=600")
	s.writeJSON(w, http.StatusOK, v)
}

// Signed-out H-1B lookups, per client IP.
const (
	publicH1BPerWindow = 30
	publicH1BWindow    = 15 * time.Minute
)

// publicH1BAllowed counts a signed-out H-1B lookup against the caller's
// IP and answers 429 once it is over the limit.
func (s *Server) publicH1BAllowed(w http.ResponseWriter, r *http.Request) bool {
	if s.publicH1BLimit.Allow(s.clientIP(r)) {
		return true
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(publicH1BWindow.Seconds())))
	s.writeError(w, http.StatusTooManyRequests, "too many lookups; sign in or try again later")
	return false
}

// publicH1BEmployers is GET /api/public/h1b/employers?q= (no sign-in): the
// same employer search as /api/h1b/employers, rate limited.
func (s *Server) publicH1BEmployers(w http.ResponseWriter, r *http.Request) {
	if s.publicH1BAllowed(w, r) {
		s.h1bEmployers(w, r)
	}
}

// PublicH1BSummary is what a signed-out visitor sees of an employer's
// certified H-1B applications: the totals, wage levels and top titles,
// without the places and individual filings an account shows.
type PublicH1BSummary struct {
	Key        string           `json:"key"`
	Name       string           `json:"name"`
	Filings    int64            `json:"filings"`
	NewHires   int64            `json:"new_hires"`
	WageMedian *float64         `json:"wage_median"`
	Levels     []store.H1BCount `json:"levels"` // wage levels I to IV, most filings first
	Titles     []store.H1BCount `json:"titles"` // the five most filed
	Period     store.H1BPeriod  `json:"period"`
}

// publicH1BEmployer is GET /api/public/h1b/employer?key= (no sign-in): one
// filing employer's summary, rate limited.
func (s *Server) publicH1BEmployer(w http.ResponseWriter, r *http.Request) {
	if !s.publicH1BAllowed(w, r) {
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" || len(key) > 200 {
		s.writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	v, err := s.h1bCache.get("detail|"+key, func() (any, error) {
		return store.H1BEmployerDetail(context.WithoutCancel(r.Context()), s.Pool, []string{key})
	})
	if err != nil {
		s.Log.Error("public h1b detail failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	d := v.(*store.H1BDetail)
	if d.Filings == 0 {
		s.writeError(w, http.StatusNotFound, "no H-1B filings for this employer")
		return
	}
	out := PublicH1BSummary{Key: key, Filings: d.Filings, NewHires: d.NewHires, WageMedian: d.WageMedian,
		Levels: d.Levels, Titles: d.Titles[:min(5, len(d.Titles))], Period: d.Period}
	if len(d.Names) > 0 {
		out.Name = d.Names[0]
	}
	w.Header().Set("Cache-Control", "public, max-age=1800")
	s.writeJSON(w, http.StatusOK, out)
}
