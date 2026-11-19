package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CompanyPage is everything the dashboard's company page shows, but the
// H-1B history (see CompanyH1BKeys).
type CompanyPage struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Industry string `json:"industry"`
	ATS      string `json:"ats"`
	LogoURL  string `json:"logo_url,omitempty"`
	Hidden   bool   `json:"hidden"`
	Followed bool   `json:"followed"`
	OpenJobs int    `json:"open_jobs"`
	// MatchCount is how many open matches the user has there; Matches
	// lists the best of them (up to 24).
	MatchCount int        `json:"match_count"`
	Matches    []JobView  `json:"matches"`
	Roles      []OpenRole `json:"roles"` // every open role, newest first (up to 200)
	// Sponsorship: what the user's scored postings there say about visa
	// sponsorship (yes, no, unknown), as counts.
	Sponsorship map[string]int `json:"sponsorship"`
}

// OpenRole is one open posting at a company.
type OpenRole struct {
	ID       int64      `json:"id"`
	Title    string     `json:"title"`
	Location string     `json:"location"`
	URL      string     `json:"url"`
	PostedAt *time.Time `json:"posted_at"`
	SeenAt   time.Time  `json:"first_seen_at"`
	Match    bool       `json:"match"` // one of the user's matches
}

// GetCompanyPage builds the page for one user. A company someone typed in
// themselves (ats manual) is theirs alone; to anyone else it is
// pgx.ErrNoRows, like a company that doesn't exist.
func GetCompanyPage(ctx context.Context, pool *pgxpool.Pool, userID, companyID int64) (*CompanyPage, error) {
	p := &CompanyPage{Matches: []JobView{}, Roles: []OpenRole{}, Sponsorship: map[string]int{}}
	var logoAt *time.Time
	err := pool.QueryRow(ctx, `
		SELECT c.id, c.name, c.industry, c.ats,
		       EXISTS (SELECT 1 FROM user_hidden_companies h WHERE h.user_id = $1 AND h.company_id = c.id),
		       EXISTS (SELECT 1 FROM company_follows f WHERE f.user_id = $1 AND f.company_id = c.id),
		       (SELECT count(*) FROM jobs j WHERE j.company_id = c.id AND j.closed_at IS NULL),
		       (SELECT l.checked_at FROM company_logos l WHERE l.company_id = c.id AND l.found)
		FROM companies c
		WHERE c.id = $2 AND (c.ats <> 'manual' OR EXISTS (
		  SELECT 1 FROM user_jobs uj JOIN jobs j ON j.id = uj.job_id WHERE uj.user_id = $1 AND j.company_id = c.id))`,
		userID, companyID).Scan(&p.ID, &p.Name, &p.Industry, &p.ATS, &p.Hidden, &p.Followed, &p.OpenJobs, &logoAt)
	if err != nil {
		return nil, err
	}
	if logoAt != nil {
		p.LogoURL = LogoURL(p.ID, *logoAt)
	}
	if p.Matches, err = queryJobViews(ctx, pool, jobViewSelect+`
	  AND j.company_id = $2 AND j.closed_at IS NULL AND uj.stage IN ('scored', 'pending_score')
	ORDER BY coalesce(a.fit_score, uj.estimated_score) DESC NULLS LAST, j.first_seen_at DESC
	LIMIT 24`, userID, companyID); err != nil {
		return nil, fmt.Errorf("company matches: %w", err)
	}
	if p.Matches == nil {
		p.Matches = []JobView{}
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM user_jobs uj JOIN jobs j ON j.id = uj.job_id
		WHERE uj.user_id = $1 AND j.company_id = $2 AND j.closed_at IS NULL AND uj.stage IN ('scored', 'pending_score')
		  AND NOT EXISTS (SELECT 1 FROM user_hidden_companies h WHERE h.user_id = $1 AND h.company_id = $2)`,
		userID, companyID).Scan(&p.MatchCount); err != nil {
		return nil, fmt.Errorf("company match count: %w", err)
	}
	rows, err := pool.Query(ctx, `
		SELECT j.id, j.title, coalesce(j.location_raw, ''), j.url, j.posted_at, j.first_seen_at,
		       EXISTS (SELECT 1 FROM user_jobs uj WHERE uj.user_id = $1 AND uj.job_id = j.id AND uj.stage IN ('scored', 'pending_score'))
		FROM jobs j WHERE j.company_id = $2 AND j.closed_at IS NULL
		ORDER BY coalesce(j.posted_at, j.first_seen_at) DESC LIMIT 200`, userID, companyID)
	if err != nil {
		return nil, fmt.Errorf("company roles: %w", err)
	}
	for rows.Next() {
		var r OpenRole
		if err := rows.Scan(&r.ID, &r.Title, &r.Location, &r.URL, &r.PostedAt, &r.SeenAt, &r.Match); err != nil {
			rows.Close()
			return nil, err
		}
		p.Roles = append(p.Roles, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = pool.Query(ctx, `
		SELECT coalesce(nullif(a.sponsorship, ''), 'unknown'), count(*)
		FROM user_jobs uj JOIN jobs j ON j.id = uj.job_id JOIN analyses a ON a.id = uj.analysis_id
		WHERE uj.user_id = $1 AND j.company_id = $2 GROUP BY 1`, userID, companyID)
	if err != nil {
		return nil, fmt.Errorf("company sponsorship: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		p.Sponsorship[k] = n
	}
	return p, rows.Err()
}

// SetCompanyFollowed follows or unfollows a company; a company the user
// can't see is pgx.ErrNoRows.
func SetCompanyFollowed(ctx context.Context, pool *pgxpool.Pool, userID, companyID int64, follow bool) error {
	var visible bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM companies WHERE id = $1 AND ats <> 'manual')`, companyID).Scan(&visible); err != nil {
		return err
	}
	if !visible {
		return pgx.ErrNoRows
	}
	var err error
	if follow {
		_, err = pool.Exec(ctx, `INSERT INTO company_follows (user_id, company_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, companyID)
	} else {
		_, err = pool.Exec(ctx, `DELETE FROM company_follows WHERE user_id = $1 AND company_id = $2`, userID, companyID)
	}
	if err != nil {
		return fmt.Errorf("follow company %d: %w", companyID, err)
	}
	return nil
}
