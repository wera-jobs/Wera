package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RunView is one row of GET /api/runs.
type RunView struct {
	ID               int64      `json:"id"`
	StartedAt        time.Time  `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at"`
	Status           string     `json:"status"`
	CompaniesOK      int        `json:"companies_ok"`
	CompaniesFailed  int        `json:"companies_failed"`
	JobsSeen         int        `json:"jobs_seen"`
	JobsNew          int        `json:"jobs_new"`
	JobsExcluded     int        `json:"jobs_excluded"`
	JobsScored       int        `json:"jobs_scored"`
	PromptTokens     int64      `json:"prompt_tokens"`
	CachedTokens     int64      `json:"cached_tokens"`
	CompletionTokens int64      `json:"completion_tokens"`
	CostUSD          float64    `json:"cost_usd"`
	Error            *string    `json:"error"`
}

// RecentRuns returns the latest runs, newest first.
func RecentRuns(ctx context.Context, pool *pgxpool.Pool, limit int) ([]RunView, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, started_at, finished_at, status, companies_ok, companies_failed,
		       jobs_seen, jobs_new, jobs_excluded, jobs_scored,
		       prompt_tokens, cached_tokens, completion_tokens, cost_usd, error
		FROM runs ORDER BY id DESC LIMIT $1`, normalizeLimit(limit, 20, 100))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunView
	for rows.Next() {
		var v RunView
		if err := rows.Scan(&v.ID, &v.StartedAt, &v.FinishedAt, &v.Status,
			&v.CompaniesOK, &v.CompaniesFailed, &v.JobsSeen, &v.JobsNew,
			&v.JobsExcluded, &v.JobsScored, &v.PromptTokens, &v.CachedTokens,
			&v.CompletionTokens, &v.CostUSD, &v.Error); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CatalogSize counts the companies Wera watches and their industries.
func CatalogSize(ctx context.Context, pool *pgxpool.Pool) (companies, industries int, err error) {
	err = pool.QueryRow(ctx, `SELECT count(*), count(DISTINCT industry) FROM companies WHERE enabled AND ats <> 'manual'`).
		Scan(&companies, &industries)
	return companies, industries, err
}

// CompanyView is one row of GET /api/companies.
type CompanyView struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	ATS            string     `json:"ats"`
	Token          string     `json:"token"`
	Industry       string     `json:"industry"`
	Enabled        bool       `json:"enabled"`
	LastFetchAt    *time.Time `json:"last_fetch_at"`
	LastFetchOK    *bool      `json:"last_fetch_ok"`
	LastFetchError *string    `json:"last_fetch_error"`
	JobsOpen       int64      `json:"jobs_open"`
	JobsScored     int64      `json:"jobs_scored"` // the user's open, scored matches
}

// ListCompanies returns companies with fetch status, open job counts, and
// the user's match counts.
func ListCompanies(ctx context.Context, pool *pgxpool.Pool, userID int64) ([]CompanyView, error) {
	rows, err := pool.Query(ctx, `
		WITH open AS (
		  SELECT company_id, count(*) AS n FROM jobs
		  WHERE closed_at IS NULL AND source <> 'manual' GROUP BY company_id
		), mine AS (
		  SELECT j.company_id, count(*) AS n
		  FROM user_jobs uj JOIN jobs j ON j.id = uj.job_id AND j.closed_at IS NULL AND j.source <> 'manual'
		  WHERE uj.user_id = $1 AND uj.stage = 'scored'
		  GROUP BY j.company_id
		)
		SELECT c.id, c.name, c.ats, c.token, c.industry, c.enabled,
		       c.last_fetch_at, c.last_fetch_ok, c.last_fetch_error,
		       COALESCE(o.n, 0), COALESCE(m.n, 0)
		FROM companies c
		LEFT JOIN open o ON o.company_id = c.id
		LEFT JOIN mine m ON m.company_id = c.id
		WHERE c.ats <> 'manual' -- companies users typed in stay private
		ORDER BY c.industry, c.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CompanyView
	for rows.Next() {
		var v CompanyView
		if err := rows.Scan(&v.ID, &v.Name, &v.ATS, &v.Token, &v.Industry, &v.Enabled,
			&v.LastFetchAt, &v.LastFetchOK, &v.LastFetchError,
			&v.JobsOpen, &v.JobsScored); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// UsageView is the token/cost summary for GET /api/usage.
type UsageView struct {
	PerDay []UsageDay  `json:"per_day"`
	Totals UsageTotals `json:"totals"`
}

// UsageDay is one day of token usage.
type UsageDay struct {
	Day              string  `json:"day"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CachedTokens     int64   `json:"cached_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

// UsageTotals aggregates all analyses.
type UsageTotals struct {
	Analyses         int64   `json:"analyses"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CachedTokens     int64   `json:"cached_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	CachedPercent    float64 `json:"cached_percent"`
}

// Usage summarizes token usage and cost per day (last 30 days) and total.
func Usage(ctx context.Context, pool *pgxpool.Pool) (*UsageView, error) {
	out := &UsageView{PerDay: []UsageDay{}}

	rows, err := pool.Query(ctx, `
		SELECT to_char(created_at, 'YYYY-MM-DD'),
		       COALESCE(sum(prompt_tokens), 0), COALESCE(sum(cached_tokens), 0),
		       COALESCE(sum(completion_tokens), 0), COALESCE(sum(cost_usd), 0)
		FROM analyses
		GROUP BY 1 ORDER BY 1 DESC LIMIT 30`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d UsageDay
		if err := rows.Scan(&d.Day, &d.PromptTokens, &d.CachedTokens, &d.CompletionTokens, &d.CostUSD); err != nil {
			return nil, err
		}
		out.PerDay = append(out.PerDay, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var t UsageTotals
	if err := pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(sum(prompt_tokens), 0),
		       COALESCE(sum(cached_tokens), 0), COALESCE(sum(completion_tokens), 0),
		       COALESCE(sum(cost_usd), 0),
		       COALESCE(sum(cached_tokens)::float / NULLIF(sum(prompt_tokens), 0) * 100, 0)
		FROM analyses`).Scan(&t.Analyses, &t.PromptTokens, &t.CachedTokens,
		&t.CompletionTokens, &t.CostUSD, &t.CachedPercent); err != nil {
		return nil, err
	}
	out.Totals = t
	return out, nil
}

// StatsView is the dashboard summary for GET /api/stats.
type StatsView struct {
	ByStage          map[string]int64 `json:"by_stage"`
	ByIndustry       map[string]int64 `json:"by_industry"`
	ByCategory       map[string]int64 `json:"by_category"`
	ByStatus         map[string]int64 `json:"by_status"`
	NewPerDay        []DayCount       `json:"new_per_day"`
	ApplicationsWeek int64            `json:"applications_per_week"`
}

// DayCount is a per-day count series point.
type DayCount struct {
	Day   string `json:"day"`
	Count int64  `json:"count"`
}

// Stats builds the /api/stats summary for one user. new_per_day counts the
// user's matches (scored or still estimated) by the day the job was first
// seen, in the user's time zone (tz, an IANA name; UTC when empty), for
// the last 14 days, starting no earlier than the first job Wera saw.
func Stats(ctx context.Context, pool *pgxpool.Pool, userID int64, tz string) (*StatsView, error) {
	if tz == "" {
		tz = "UTC"
	}
	out := &StatsView{
		ByStage:    map[string]int64{},
		ByIndustry: map[string]int64{},
		ByCategory: map[string]int64{},
		ByStatus:   map[string]int64{},
		NewPerDay:  []DayCount{},
	}

	countTo := func(q string, m map[string]int64) error {
		rows, err := pool.Query(ctx, q, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			var n int64
			if err := rows.Scan(&k, &n); err != nil {
				return err
			}
			m[k] = n
		}
		return rows.Err()
	}

	if err := countTo(`SELECT stage, count(*) FROM user_jobs WHERE user_id = $1 GROUP BY 1`, out.ByStage); err != nil {
		return nil, err
	}
	// The user's live matches, read once for all three breakdowns.
	rows, err := pool.Query(ctx, `
		WITH m AS MATERIALIZED (
		  SELECT c.industry, uj.matched_categories, COALESCE(a.status, 'none') AS status
		  FROM user_jobs uj
		  JOIN jobs j ON j.id = uj.job_id AND j.closed_at IS NULL
		  JOIN companies c ON c.id = j.company_id
		  LEFT JOIN applications a ON a.job_id = uj.job_id AND a.user_id = uj.user_id
		  WHERE uj.user_id = $1 AND uj.stage IN ('scored', 'pending_score')
		)
		SELECT 'industry', industry, count(*) FROM m GROUP BY industry
		UNION ALL SELECT 'category', cat, count(*) FROM m, unnest(m.matched_categories) AS cat GROUP BY cat
		UNION ALL SELECT 'status', status, count(*) FROM m GROUP BY status`, userID)
	if err != nil {
		return nil, err
	}
	by := map[string]map[string]int64{"industry": out.ByIndustry, "category": out.ByCategory, "status": out.ByStatus}
	for rows.Next() {
		var kind, k string
		var n int64
		if err := rows.Scan(&kind, &k, &n); err != nil {
			rows.Close()
			return nil, err
		}
		by[kind][k] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = pool.Query(ctx, `
		WITH today AS (SELECT (now() AT TIME ZONE $2)::date AS d),
		     first AS (SELECT (min(first_seen_at) AT TIME ZONE $2)::date AS d FROM jobs)
		SELECT to_char(s.day, 'YYYY-MM-DD'), COALESCE(c.n, 0)
		FROM generate_series(
		       GREATEST((SELECT d FROM today) - 13, COALESCE((SELECT d FROM first), (SELECT d FROM today))),
		       (SELECT d FROM today), interval '1 day') AS s(day)
		LEFT JOIN (
			SELECT (j.first_seen_at AT TIME ZONE $2)::date AS day, count(*) AS n
			FROM user_jobs uj JOIN jobs j ON j.id = uj.job_id
			WHERE uj.user_id = $1 AND uj.stage IN ('scored', 'pending_score')
			  AND j.first_seen_at >= now() - interval '15 days'
			GROUP BY 1
		) c ON c.day = s.day::date
		ORDER BY s.day`, userID, tz)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p DayCount
		if err := rows.Scan(&p.Day, &p.Count); err != nil {
			return nil, err
		}
		out.NewPerDay = append(out.NewPerDay, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM applications
		WHERE user_id = $1 AND applied_at >= now() - interval '7 days'
		  AND status IN ('applied', 'interviewing', 'offer', 'rejected')`, userID).Scan(&out.ApplicationsWeek); err != nil {
		return nil, err
	}
	return out, nil
}
