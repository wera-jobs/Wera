package store

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// IndustryCounts holds the per-industry numbers behind GET /api/industries.
type IndustryCounts struct {
	Companies int64 `json:"companies"`
	OpenJobs  int64 `json:"open_jobs"`
	Matches   int64 `json:"matches"`   // the user's open, scored jobs
	TopScore  *int  `json:"top_score"` // best fit score among matches
}

// CountByIndustry returns company, job, and the user's match counts keyed
// by industry id.
func CountByIndustry(ctx context.Context, pool *pgxpool.Pool, userID int64) (map[string]IndustryCounts, error) {
	rows, err := pool.Query(ctx, `
		WITH boards AS (
		  SELECT industry, count(*) FILTER (WHERE enabled) AS companies FROM companies GROUP BY industry
		), open AS (
		  SELECT c.industry, count(*) AS jobs
		  FROM jobs j JOIN companies c ON c.id = j.company_id
		  WHERE j.closed_at IS NULL
		  GROUP BY c.industry
		), mine AS (
		  SELECT c.industry, count(*) AS matches, max(a.fit_score) AS top
		  FROM user_jobs uj
		  JOIN jobs j ON j.id = uj.job_id AND j.closed_at IS NULL
		  JOIN companies c ON c.id = j.company_id
		  LEFT JOIN analyses a ON a.id = uj.analysis_id
		  WHERE uj.user_id = $1 AND uj.stage = 'scored'
		  GROUP BY c.industry
		)
		SELECT b.industry, b.companies, COALESCE(o.jobs, 0), COALESCE(m.matches, 0), m.top
		FROM boards b
		LEFT JOIN open o USING (industry)
		LEFT JOIN mine m USING (industry)`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]IndustryCounts{}
	for rows.Next() {
		var id string
		var c IndustryCounts
		if err := rows.Scan(&id, &c.Companies, &c.OpenJobs, &c.Matches, &c.TopScore); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, rows.Err()
}
