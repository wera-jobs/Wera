package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"wera/internal/store"
)

// Site analytics without cookies or consent (see migration 0035): the
// dashboard sends a small beacon when a page is seen and when it is left.
// Nothing is stored on the visitor's device, and the server keeps no
// address, browser string or account: a visitor is a hash of the day's
// salt, the address and the browser, which no one can recompute once the
// salt is deleted the next day.

// pageSeen is POST /api/page-seen (no sign-in). Always 204: a beacon has
// no one to read an error.
func (s *Server) pageSeen(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusNoContent)
	ua := r.UserAgent()
	if ua == "" || botUA.MatchString(ua) || r.Header.Get("Sec-GPC") == "1" || r.Header.Get("DNT") == "1" {
		return
	}
	ip := s.visitorIP(r)
	if !s.beaconLimit.Allow(ip) {
		return
	}
	var in struct {
		Kind     string `json:"k"`
		Path     string `json:"p"`
		Referrer string `json:"r"`
		Source   string `json:"us"`
		Medium   string `json:"um"`
		Campaign string `json:"uc"`
		Engaged  int    `json:"e"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 2048))
	if err != nil || json.Unmarshal(body, &in) != nil {
		return
	}
	if in.Kind != "view" && in.Kind != "leave" {
		return
	}
	path := cleanPath(in.Path)
	if path == "" {
		return
	}
	salt, err := s.visitorSalt(r.Context())
	if err != nil {
		s.Log.Warn("analytics salt failed", "err", err)
		return
	}
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(ip + "\x00" + ua))
	device, browser, os := parseUA(ua)
	e := store.WebEvent{
		Kind: in.Kind, Visitor: hex.EncodeToString(h.Sum(nil))[:24], Path: path,
		Referrer: referrerHost(in.Referrer), UTMSource: clip(in.Source, 60), UTMMedium: clip(in.Medium, 60), UTMCampaign: clip(in.Campaign, 80),
		Device: device, Browser: browser, OS: os, Country: country(r),
	}
	if in.Kind == "leave" {
		// Time the page was in view, capped at an hour (a tab left open overnight says nothing).
		e.EngagedMS = min(max(in.Engaged, 0), 3600_000)
	}
	if err := store.RecordWebEvent(context.WithoutCancel(r.Context()), s.Pool, e); err != nil {
		s.Log.Warn("recording a page event failed", "err", err)
	}
}

var botUA = regexp.MustCompile(`(?i)bot|crawl|spider|slurp|headless|preview|facebookexternalhit|lighthouse|pingdom|uptime|curl|wget|python|go-http|axios|node-fetch`)

// cleanPath keeps a page's path and nothing that could name someone: no
// query, no fragment, and ids in the path replaced by ":id".
func cleanPath(p string) string {
	p = strings.TrimSpace(p)
	if !strings.HasPrefix(p, "/") || len(p) > 300 {
		return ""
	}
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	parts := strings.Split(p, "/")
	for i, part := range parts {
		if idPart.MatchString(part) {
			parts[i] = ":id"
		}
	}
	if p = strings.Join(parts, "/"); len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// A path part that is an id: digits, a UUID, or a long token.
var idPart = regexp.MustCompile(`^(\d+|[0-9a-fA-F-]{16,}|[A-Za-z0-9_-]{24,})$`)

// referrerHost is the site a visitor came from, without "www." (only its host).
func referrerHost(ref string) string {
	u, err := url.Parse(strings.TrimSpace(ref))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return clip(strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."), 100)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// parseUA reads the device kind, browser and system from a user agent.
func parseUA(ua string) (device, browser, os string) {
	switch {
	case strings.Contains(ua, "iPad") || strings.Contains(ua, "Tablet") || (strings.Contains(ua, "Android") && !strings.Contains(ua, "Mobile")):
		device = "Tablet"
	case strings.Contains(ua, "Mobi") || strings.Contains(ua, "iPhone"):
		device = "Mobile"
	default:
		device = "Desktop"
	}
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "OPR/") || strings.Contains(ua, "Opera"):
		browser = "Opera"
	case strings.Contains(ua, "SamsungBrowser"):
		browser = "Samsung Internet"
	case strings.Contains(ua, "Firefox/") || strings.Contains(ua, "FxiOS"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/") || strings.Contains(ua, "CriOS"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	default:
		browser = "Other"
	}
	switch {
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad"):
		os = "iOS"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "Mac OS X") || strings.Contains(ua, "Macintosh"):
		os = "macOS"
	case strings.Contains(ua, "CrOS"):
		os = "ChromeOS"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	default:
		os = "Other"
	}
	return device, browser, os
}

// visitorIP is the visitor's address for counting (not security): the
// first address a proxy saw. Vercel, in front of the dashboard, puts the
// visitor's own address in X-Vercel-Forwarded-For and X-Real-IP;
// Cloudflare's CF-Connecting-IP is Vercel's then.
func (s *Server) visitorIP(r *http.Request) string {
	if s.TrustProxy {
		for _, h := range []string{"X-Vercel-Forwarded-For", "X-Real-IP", "X-Forwarded-For", "CF-Connecting-IP"} {
			if v := strings.TrimSpace(strings.Split(r.Header.Get(h), ",")[0]); v != "" {
				return v
			}
		}
	}
	return s.clientIP(r)
}

// country is the visitor's country as the edge saw it (Vercel's first: Cloudflare sees Vercel).
func country(r *http.Request) string {
	for _, h := range []string{"X-Vercel-IP-Country", "CF-IPCountry"} {
		if v := strings.ToUpper(strings.TrimSpace(r.Header.Get(h))); len(v) == 2 && v != "XX" && v != "T1" {
			return v
		}
	}
	return ""
}

// visitorSalt is today's salt, kept in memory for the day.
func (s *Server) visitorSalt(ctx context.Context) ([]byte, error) {
	day := time.Now().UTC().Format("2006-01-02")
	s.saltMu.Lock()
	defer s.saltMu.Unlock()
	if s.saltDay == day {
		return s.salt, nil
	}
	salt, err := store.VisitorSalt(ctx, s.Pool, time.Now())
	if err != nil {
		return nil, err
	}
	s.salt, s.saltDay = salt, day
	return salt, nil
}

// adminTraffic is GET /api/admin/traffic?days=30&tz=America/Chicago.
func (s *Server) adminTraffic(w http.ResponseWriter, r *http.Request) {
	days := 30
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && d > 0 {
		days = min(d, 400)
	}
	tz := r.URL.Query().Get("tz")
	if _, err := time.LoadLocation(tz); tz == "" || err != nil {
		tz = "UTC"
	}
	v, err := store.Traffic(r.Context(), s.Pool, days, tz)
	if err != nil {
		s.Log.Error("traffic failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, v)
}

// requestCounter adds up the requests the API answers, per day, route and
// method, and writes them to the database about once a minute.
type requestCounter struct {
	mu      sync.Mutex
	counts  map[[3]string]*store.RequestCount
	flushed time.Time
	saving  bool
}

const flushEvery = time.Minute

// countRequests is the middleware that feeds the request counter.
func (s *Server) countRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		route := "(no route)"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			route = rc.RoutePattern()
		}
		s.requests.add(route, r.Method, ww.Status(), time.Since(start))
		s.requests.maybeFlush(s)
	})
}

func (c *requestCounter) add(route, method string, status int, took time.Duration) {
	day := time.Now().UTC().Truncate(24 * time.Hour)
	key := [3]string{day.Format("2006-01-02"), route, method}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts == nil {
		c.counts = map[[3]string]*store.RequestCount{}
		c.flushed = time.Now()
	}
	rc := c.counts[key]
	if rc == nil {
		rc = &store.RequestCount{Day: day, Route: route, Method: method}
		c.counts[key] = rc
	}
	rc.Requests++
	rc.TotalMS += took.Milliseconds()
	switch {
	case status >= 500:
		rc.Errors5xx++
	case status >= 400:
		rc.Errors4xx++
	}
}

// maybeFlush writes the counts in the background once a minute has passed.
func (c *requestCounter) maybeFlush(s *Server) {
	c.mu.Lock()
	if c.saving || time.Since(c.flushed) < flushEvery || len(c.counts) == 0 || s.Pool == nil {
		c.mu.Unlock()
		return
	}
	batch := make([]store.RequestCount, 0, len(c.counts))
	for _, rc := range c.counts {
		batch = append(batch, *rc)
	}
	c.counts, c.flushed, c.saving = map[[3]string]*store.RequestCount{}, time.Now(), true
	c.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := store.AddRequestCounts(ctx, s.Pool, batch); err != nil {
			s.Log.Warn("saving request counts failed", "err", err)
		}
		c.mu.Lock()
		c.saving = false
		c.mu.Unlock()
	}()
}
