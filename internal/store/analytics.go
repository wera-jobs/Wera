package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WebEvent is one page seen or left on the site (see migration 0035).
type WebEvent struct {
	Kind        string // view or leave
	Visitor     string
	Path        string
	Referrer    string
	UTMSource   string
	UTMMedium   string
	UTMCampaign string
	Device      string
	Browser     string
	OS          string
	Country     string
	EngagedMS   int
}

// RecordWebEvent stores one page event.
func RecordWebEvent(ctx context.Context, pool *pgxpool.Pool, e WebEvent) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO web_events (kind, visitor, path, referrer, utm_source, utm_medium, utm_campaign, device, browser, os, country, engaged_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		e.Kind, e.Visitor, e.Path, e.Referrer, e.UTMSource, e.UTMMedium, e.UTMCampaign, e.Device, e.Browser, e.OS, e.Country, e.EngagedMS)
	return err
}

// VisitorSalt returns the salt of the given UTC day, made on first use,
// and deletes older ones so yesterday's visitor hashes can never be
// recomputed.
func VisitorSalt(ctx context.Context, pool *pgxpool.Pool, day time.Time) ([]byte, error) {
	d := day.UTC().Format("2006-01-02")
	fresh := make([]byte, 32)
	if _, err := rand.Read(fresh); err != nil {
		return nil, err
	}
	var salt []byte
	err := pool.QueryRow(ctx, `
		WITH ins AS (INSERT INTO analytics_salts (day, salt) VALUES ($1, $2) ON CONFLICT (day) DO NOTHING RETURNING salt)
		SELECT salt FROM ins UNION ALL SELECT salt FROM analytics_salts WHERE day = $1 LIMIT 1`, d, fresh).Scan(&salt)
	if err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM analytics_salts WHERE day < $1`, d); err != nil {
		return nil, err
	}
	return salt, nil
}

// RequestCount is how many requests one route answered on one day.
type RequestCount struct {
	Day       time.Time
	Route     string
	Method    string
	Requests  int64
	Errors4xx int64
	Errors5xx int64
	TotalMS   int64
}

// AddRequestCounts adds counts to the daily totals.
func AddRequestCounts(ctx context.Context, pool *pgxpool.Pool, counts []RequestCount) error {
	if len(counts) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, c := range counts {
		batch.Queue(`
			INSERT INTO api_requests_daily (day, route, method, requests, errors_4xx, errors_5xx, total_ms)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (day, route, method) DO UPDATE SET
			  requests = api_requests_daily.requests + EXCLUDED.requests,
			  errors_4xx = api_requests_daily.errors_4xx + EXCLUDED.errors_4xx,
			  errors_5xx = api_requests_daily.errors_5xx + EXCLUDED.errors_5xx,
			  total_ms = api_requests_daily.total_ms + EXCLUDED.total_ms`,
			c.Day.Format("2006-01-02"), c.Route, c.Method, c.Requests, c.Errors4xx, c.Errors5xx, c.TotalMS)
	}
	return pool.SendBatch(ctx, batch).Close()
}

// Sessions: a visit ends after this long without a page seen.
const sessionGap = "30 minutes"

// A visit that saw one page for less than this is a bounce.
const bounceMS = 10000

// TrafficDay is one day of site traffic.
type TrafficDay struct {
	Day       string  `json:"day"`
	Visitors  int64   `json:"visitors"`
	Pageviews int64   `json:"pageviews"`
	Sessions  int64   `json:"sessions"`
	Bounces   int64   `json:"bounces"`
	Signups   int64   `json:"signups"`
	AvgVisitS float64 `json:"avg_visit_seconds"`
}

// TrafficTotals sums a range. Visitors are counted per day (the visitor
// hash changes daily), so a person who came on two days counts twice.
type TrafficTotals struct {
	Visitors      int64   `json:"visitors"`
	Pageviews     int64   `json:"pageviews"`
	Sessions      int64   `json:"sessions"`
	BounceRate    float64 `json:"bounce_rate"`
	AvgVisitS     float64 `json:"avg_visit_seconds"`
	ViewsPerVisit float64 `json:"views_per_visit"`
	Signups       int64   `json:"signups"`
	LiveVisitors  int64   `json:"live_visitors"`
}

// TrafficRow is one line of a breakdown (a page, a referrer, a browser...).
type TrafficRow struct {
	Label     string  `json:"label"`
	Visitors  int64   `json:"visitors"`
	Pageviews int64   `json:"pageviews,omitempty"`
	Sessions  int64   `json:"sessions,omitempty"`
	Bounce    float64 `json:"bounce_rate,omitempty"`
	EngagedS  float64 `json:"avg_engaged_seconds,omitempty"`
}

// RequestDay is one day of API requests.
type RequestDay struct {
	Day       string  `json:"day"`
	Requests  int64   `json:"requests"`
	Errors4xx int64   `json:"errors_4xx"`
	Errors5xx int64   `json:"errors_5xx"`
	AvgMS     float64 `json:"avg_ms"`
}

// RouteRow is one route's requests over the range.
type RouteRow struct {
	Route     string  `json:"route"`
	Method    string  `json:"method"`
	Requests  int64   `json:"requests"`
	Errors5xx int64   `json:"errors_5xx"`
	AvgMS     float64 `json:"avg_ms"`
}

// TrafficView is what the admin app's Traffic page shows.
type TrafficView struct {
	Days      int           `json:"days"`
	TimeZone  string        `json:"time_zone"`
	Totals    TrafficTotals `json:"totals"`
	PerDay    []TrafficDay  `json:"per_day"`
	Pages     []TrafficRow  `json:"pages"`
	Landing   []TrafficRow  `json:"landing"`
	Referrers []TrafficRow  `json:"referrers"`
	Campaigns []TrafficRow  `json:"campaigns"`
	Devices   []TrafficRow  `json:"devices"`
	Browsers  []TrafficRow  `json:"browsers"`
	Systems   []TrafficRow  `json:"systems"`
	Countries []TrafficRow  `json:"countries"`
	Requests  []RequestDay  `json:"requests"`
	TopRoutes []RouteRow    `json:"top_routes"`
}

// sessionsCTE splits each visitor's events into visits ($1: since). A
// visit starts with a page seen after 30 minutes without one.
const sessionsCTE = `
	WITH ev AS (
	  SELECT visitor, at, kind, path, engaged_ms, referrer, utm_source, utm_campaign,
	         lag(at) OVER (PARTITION BY visitor ORDER BY at) AS prev
	  FROM web_events WHERE at >= $1
	), marked AS (
	  SELECT *, CASE WHEN kind = 'view' AND (prev IS NULL OR at - prev > interval '` + sessionGap + `') THEN 1 ELSE 0 END AS starts FROM ev
	), numbered AS (
	  SELECT *, sum(starts) OVER (PARTITION BY visitor ORDER BY at ROWS UNBOUNDED PRECEDING) AS visit FROM marked
	), visits AS (
	  SELECT visitor, visit, min(at) AS started,
	         count(*) FILTER (WHERE kind = 'view') AS views,
	         coalesce(sum(engaged_ms), 0) AS engaged,
	         (array_agg(path ORDER BY at) FILTER (WHERE kind = 'view'))[1] AS landing,
	         (array_agg(referrer ORDER BY at) FILTER (WHERE kind = 'view'))[1] AS referrer,
	         (array_agg(utm_source ORDER BY at) FILTER (WHERE kind = 'view'))[1] AS utm_source,
	         (array_agg(utm_campaign ORDER BY at) FILTER (WHERE kind = 'view'))[1] AS utm_campaign
	  FROM numbered GROUP BY visitor, visit
	  HAVING count(*) FILTER (WHERE kind = 'view') > 0
	)`

// Traffic reads the site's traffic and the API's requests for the last
// days, by day in the given time zone (an IANA name, checked by the caller).
func Traffic(ctx context.Context, pool *pgxpool.Pool, days int, tz string) (*TrafficView, error) {
	if days < 1 || days > 400 {
		return nil, errors.New("days must be 1 to 400")
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("time zone: %w", err)
	}
	now := time.Now().In(loc)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -(days - 1))
	v := &TrafficView{Days: days, TimeZone: tz}
	bounce := fmt.Sprintf("views = 1 AND engaged < %d", bounceMS)

	// Per day: visitors and views from the events, visits and bounces by the day a visit started.
	rows, err := pool.Query(ctx, sessionsCTE+`,
	  days AS (SELECT generate_series(($1 AT TIME ZONE $2)::date, (now() AT TIME ZONE $2)::date, interval '1 day')::date AS day),
	  ev_day AS (
	    SELECT (at AT TIME ZONE $2)::date AS day, count(DISTINCT visitor) AS visitors, count(*) FILTER (WHERE kind = 'view') AS views
	    FROM web_events WHERE at >= $1 GROUP BY 1
	  ), visit_day AS (
	    SELECT (started AT TIME ZONE $2)::date AS day, count(*) AS visits, count(*) FILTER (WHERE `+bounce+`) AS bounces,
	           avg(engaged) / 1000.0 AS avg_s
	    FROM visits GROUP BY 1
	  ), signup_day AS (
	    SELECT (created_at AT TIME ZONE $2)::date AS day, count(*) AS n FROM users WHERE created_at >= $1 GROUP BY 1
	  )
	  SELECT d.day::text, coalesce(e.visitors, 0), coalesce(e.views, 0), coalesce(s.visits, 0), coalesce(s.bounces, 0),
	         coalesce(u.n, 0), coalesce(s.avg_s, 0)
	  FROM days d LEFT JOIN ev_day e USING (day) LEFT JOIN visit_day s USING (day) LEFT JOIN signup_day u USING (day)
	  ORDER BY d.day`, start, tz)
	if err != nil {
		return nil, fmt.Errorf("traffic per day: %w", err)
	}
	v.PerDay, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (TrafficDay, error) {
		var d TrafficDay
		err := r.Scan(&d.Day, &d.Visitors, &d.Pageviews, &d.Sessions, &d.Bounces, &d.Signups, &d.AvgVisitS)
		return d, err
	})
	if err != nil {
		return nil, fmt.Errorf("traffic per day: %w", err)
	}
	var bounces int64
	var engagedSum float64
	for _, d := range v.PerDay {
		v.Totals.Visitors += d.Visitors
		v.Totals.Pageviews += d.Pageviews
		v.Totals.Sessions += d.Sessions
		v.Totals.Signups += d.Signups
		bounces += d.Bounces
		engagedSum += d.AvgVisitS * float64(d.Sessions)
	}
	if v.Totals.Sessions > 0 {
		v.Totals.BounceRate = 100 * float64(bounces) / float64(v.Totals.Sessions)
		v.Totals.AvgVisitS = engagedSum / float64(v.Totals.Sessions)
		v.Totals.ViewsPerVisit = float64(v.Totals.Pageviews) / float64(v.Totals.Sessions)
	}
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT visitor) FROM web_events WHERE at > now() - interval '5 minutes'`).Scan(&v.Totals.LiveVisitors); err != nil {
		return nil, fmt.Errorf("live visitors: %w", err)
	}

	breakdown := func(q string, args ...any) ([]TrafficRow, error) {
		rows, err := pool.Query(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, func(r pgx.CollectableRow) (TrafficRow, error) {
			var t TrafficRow
			err := r.Scan(&t.Label, &t.Visitors, &t.Pageviews, &t.Sessions, &t.Bounce, &t.EngagedS)
			return t, err
		})
	}
	// A column of the events: visitors per value (one visitor counted once a day).
	byColumn := func(col string) ([]TrafficRow, error) {
		return breakdown(`
			SELECT label, sum(visitors)::bigint, sum(views)::bigint, 0::bigint, 0::float8, 0::float8 FROM (
			  SELECT `+col+` AS label, (at AT TIME ZONE $2)::date, count(DISTINCT visitor) AS visitors, count(*) FILTER (WHERE kind = 'view') AS views
			  FROM web_events WHERE at >= $1 AND kind = 'view' GROUP BY 1, 2) x
			GROUP BY label ORDER BY 2 DESC, 1 LIMIT 15`, start, tz)
	}
	if v.Pages, err = breakdown(`
		SELECT label, sum(visitors)::bigint, sum(views)::bigint, 0::bigint, 0::float8, coalesce(avg(engaged) FILTER (WHERE engaged > 0), 0) / 1000.0 FROM (
		  SELECT path AS label, (at AT TIME ZONE $2)::date AS day, count(DISTINCT visitor) AS visitors,
		         count(*) FILTER (WHERE kind = 'view') AS views, avg(engaged_ms) FILTER (WHERE kind = 'leave') AS engaged
		  FROM web_events WHERE at >= $1 GROUP BY 1, 2) x
		GROUP BY label ORDER BY 3 DESC, 1 LIMIT 20`, start, tz); err != nil {
		return nil, fmt.Errorf("pages: %w", err)
	}
	visitBreakdown := func(col string) ([]TrafficRow, error) {
		return breakdown(sessionsCTE+`
			SELECT `+col+`, count(DISTINCT visitor), sum(views)::bigint, count(*),
			       100.0 * count(*) FILTER (WHERE `+bounce+`) / count(*), avg(engaged) / 1000.0
			FROM visits GROUP BY 1 ORDER BY 4 DESC, 1 LIMIT 15`, start)
	}
	if v.Landing, err = visitBreakdown("landing"); err != nil {
		return nil, fmt.Errorf("landing pages: %w", err)
	}
	if v.Referrers, err = visitBreakdown("CASE WHEN referrer = '' THEN 'Direct or unknown' ELSE referrer END"); err != nil {
		return nil, fmt.Errorf("referrers: %w", err)
	}
	if v.Campaigns, err = visitBreakdown("CASE WHEN utm_source = '' THEN '(none)' ELSE utm_source || CASE WHEN utm_campaign <> '' THEN ' / ' || utm_campaign ELSE '' END END"); err != nil {
		return nil, fmt.Errorf("campaigns: %w", err)
	}
	for _, b := range []struct {
		col string
		out *[]TrafficRow
	}{{"device", &v.Devices}, {"browser", &v.Browsers}, {"os", &v.Systems}, {"CASE WHEN country = '' THEN 'Unknown' ELSE country END", &v.Countries}} {
		if *b.out, err = byColumn(b.col); err != nil {
			return nil, fmt.Errorf("breakdown by %s: %w", b.col, err)
		}
	}

	day0 := start.Format("2006-01-02")
	rows, err = pool.Query(ctx, `
		SELECT day::text, sum(requests)::bigint, sum(errors_4xx)::bigint, sum(errors_5xx)::bigint,
		       coalesce(sum(total_ms)::float8 / nullif(sum(requests), 0), 0)
		FROM api_requests_daily WHERE day >= $1 GROUP BY day ORDER BY day`, day0)
	if err != nil {
		return nil, fmt.Errorf("requests: %w", err)
	}
	if v.Requests, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (RequestDay, error) {
		var d RequestDay
		err := r.Scan(&d.Day, &d.Requests, &d.Errors4xx, &d.Errors5xx, &d.AvgMS)
		return d, err
	}); err != nil {
		return nil, fmt.Errorf("requests: %w", err)
	}
	rows, err = pool.Query(ctx, `
		SELECT route, method, sum(requests)::bigint, sum(errors_5xx)::bigint, coalesce(sum(total_ms)::float8 / nullif(sum(requests), 0), 0)
		FROM api_requests_daily WHERE day >= $1 GROUP BY route, method ORDER BY 3 DESC LIMIT 20`, day0)
	if err != nil {
		return nil, fmt.Errorf("top routes: %w", err)
	}
	if v.TopRoutes, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (RouteRow, error) {
		var t RouteRow
		err := r.Scan(&t.Route, &t.Method, &t.Requests, &t.Errors5xx, &t.AvgMS)
		return t, err
	}); err != nil {
		return nil, fmt.Errorf("top routes: %w", err)
	}
	return v, nil
}
