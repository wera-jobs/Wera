package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// JobQuery carries every /api/jobs filter.
type JobQuery struct {
	Industry     string
	Category     string
	MinScore     int
	Sponsorship  string // "yes" | "unknown" (only these two are meaningful)
	WorkMode     string
	Status       string // application status
	Q            string // title/company search
	Since        time.Time
	PostedWithin int    // days; 0 = any age (see listedAt)
	Sort         string // "score" (default) | "newest"
	Limit        int
	Offset       int
}

// JobView is one row of the API job list: the job plus company industry,
// the latest score analysis, and the application status.
type JobView struct {
	ID              int64      `json:"id"`
	CompanyID       int64      `json:"company_id"`
	Company         string     `json:"company"`
	Industry        string     `json:"industry"`
	Source          string     `json:"source"`
	ExtID           string     `json:"ext_id"`
	Title           string     `json:"title"`
	LocationRaw     string     `json:"location_raw"`
	IsRemote        *bool      `json:"is_remote"`
	URL             string     `json:"url"`
	Department      *string    `json:"department"`
	PostedAt        *time.Time `json:"posted_at"`
	FirstSeenAt     time.Time  `json:"first_seen_at"`
	Stage           string     `json:"stage"`
	MatchedCategory []string   `json:"matched_categories"`
	ExcludeReason   *string    `json:"exclude_reason"`
	ExcludeEvidence *string    `json:"exclude_evidence"`
	FitScore        *int       `json:"fit_score"`
	Verdict         *string    `json:"verdict"`
	Seniority       *string    `json:"seniority"`
	YearsRequired   *int       `json:"years_required"`
	Sponsorship     *string    `json:"sponsorship"`
	WorkMode        *string    `json:"work_mode"`
	LocationSummary *string    `json:"location_summary"`
	SkillsMatched   []string   `json:"skills_matched"`
	SkillsMissing   []string   `json:"skills_missing"`
	Reason          *string    `json:"reason"`
	AppStatus       *string    `json:"application_status"`
	AppNotes        *string    `json:"application_notes"`
	// EstimatedScore is shown until the full AI score arrives: the AI's
	// quick grade when there is one (EstimateAI), else the local relevance
	// estimate (35..85).
	EstimatedScore *int `json:"estimated_score"`
	EstimateAI     bool `json:"estimate_ai"`
	// AutofilledAt is when the extension last filled an application for
	// the job (nil: never).
	AutofilledAt *time.Time `json:"autofilled_at"`
	// ClosedAt is when the posting came down (nil: still open).
	ClosedAt *time.Time `json:"closed_at"`
}

// jobViewSelect selects one user's view of jobs: their rule outcome,
// the analysis linked to it, and their application. $1 is the user id;
// callers append conditions starting at $2.
const jobViewSelect = `
	SELECT j.id, j.company_id, c.name, c.industry, j.source, j.ext_id, j.title,
	       COALESCE(j.location_raw, ''), j.is_remote, j.url, j.department, j.posted_at,
	       j.first_seen_at, uj.stage, uj.matched_categories,
	       uj.exclude_reason, uj.exclude_evidence,
	       a.fit_score, a.verdict, a.seniority, a.years_required,
	       a.sponsorship, a.work_mode, a.location_summary,
	       a.skills_matched, a.skills_missing, a.reason,
	       ap.status, ap.notes, uj.estimated_score,
	       EXISTS (SELECT 1 FROM quick_grades q WHERE q.user_id = uj.user_id AND q.job_id = j.id),
	       (SELECT max(r.updated_at) FROM autofill_runs r WHERE r.user_id = uj.user_id AND r.job_id = j.id),
	       j.closed_at
	FROM user_jobs uj
	JOIN jobs j ON j.id = uj.job_id
	JOIN companies c ON c.id = j.company_id
	LEFT JOIN analyses a ON a.id = uj.analysis_id
	LEFT JOIN applications ap ON ap.job_id = j.id AND ap.user_id = uj.user_id
	WHERE uj.user_id = $1
	  AND NOT EXISTS (SELECT 1 FROM user_hidden_companies h
	                  WHERE h.user_id = uj.user_id AND h.company_id = j.company_id)`

// jobViewScan lists the scan targets shared by every JobView query.
func jobViewScan(v *JobView) []any {
	return []any{&v.ID, &v.CompanyID, &v.Company, &v.Industry, &v.Source, &v.ExtID,
		&v.Title, &v.LocationRaw, &v.IsRemote, &v.URL, &v.Department, &v.PostedAt,
		&v.FirstSeenAt, &v.Stage, &v.MatchedCategory,
		&v.ExcludeReason, &v.ExcludeEvidence,
		&v.FitScore, &v.Verdict, &v.Seniority, &v.YearsRequired,
		&v.Sponsorship, &v.WorkMode, &v.LocationSummary,
		&v.SkillsMatched, &v.SkillsMissing, &v.Reason,
		&v.AppStatus, &v.AppNotes, &v.EstimatedScore, &v.EstimateAI, &v.AutofilledAt, &v.ClosedAt}
}

// normalizeLimit clamps a page size for API queries.
func normalizeLimit(limit, def, ceiling int) int {
	if limit <= 0 {
		return def
	}
	if limit > ceiling {
		return ceiling
	}
	return limit
}

// listedAt is when a posting went up: the date its job board gives, or
// when Wera first saw it, whichever is earlier. A company Wera starts
// watching brings postings that are months old, all first seen that day.
const listedAt = "LEAST(COALESCE(j.posted_at, j.first_seen_at), j.first_seen_at)"

// Freshness. Most hiring happens in a posting's first weeks, and a posting
// up for months is often filled already or kept up for show, so Today and
// the scoring queue put a posting behind fresher ones a point for every
// six days it has been up past its third week, up to 20 points (reached
// at 141 days). A very strong old match still goes ahead of a weak new
// one. On the live data (2026-11-05), of the 30 best scored jobs of seven
// users, postings over 60 days old fell from 11-25 per user to 0-4, and
// the average fit score of those 30 from 87.1 to 84.0. Steeper curves
// tested cost more fit for few more fresh jobs.
const (
	freshDays     = 21
	daysPerPoint  = 6
	maxAgePenalty = 20
)

// agePenalty is AgePenalty in SQL, for a row of jobs j.
var agePenalty = fmt.Sprintf("least(%d, greatest(0, extract(epoch FROM now() - %s) / 86400 - %d) / %d)",
	maxAgePenalty, listedAt, freshDays, daysPerPoint)

// AgePenalty is how many points a posting listed at listed loses against
// fresher ones at now. A zero time (unknown) loses none.
func AgePenalty(listed, now time.Time) float64 {
	if listed.IsZero() {
		return 0
	}
	days := now.Sub(listed).Hours() / 24
	return min(maxAgePenalty, max(0, days-freshDays)/daysPerPoint)
}

// ListJobs runs the /api/jobs query for one user. By default it hides
// jobs the AI ruled out, failed scores and closed jobs. Filtering by application status lists what the
// user is tracking, which stays theirs however the posting ends up: most
// come down within weeks of the application.
func ListJobs(ctx context.Context, pool *pgxpool.Pool, userID int64, q JobQuery) ([]JobView, error) {
	var where []string
	args := []any{userID}

	if q.Status == "" {
		where = append(where, "j.closed_at IS NULL")
		where = append(where, "uj.stage IN ('scored','pending_score')")
	}
	if q.Industry != "" {
		args = append(args, q.Industry)
		where = append(where, fmt.Sprintf("c.industry = $%d", len(args)))
	}
	if q.Category != "" {
		args = append(args, q.Category)
		where = append(where, fmt.Sprintf("uj.matched_categories @> ARRAY[$%d::text]", len(args)))
	}
	if q.MinScore > 0 {
		args = append(args, q.MinScore)
		where = append(where, fmt.Sprintf("a.fit_score >= $%d", len(args)))
	}
	if q.Sponsorship != "" {
		args = append(args, q.Sponsorship)
		where = append(where, fmt.Sprintf("a.sponsorship = $%d", len(args)))
	}
	if q.WorkMode != "" {
		args = append(args, q.WorkMode)
		where = append(where, fmt.Sprintf("a.work_mode = $%d", len(args)))
	}
	if q.Status != "" {
		args = append(args, q.Status)
		where = append(where, fmt.Sprintf("ap.status = $%d", len(args)))
	}
	if q.Q != "" {
		args = append(args, "%"+strings.ToLower(q.Q)+"%")
		where = append(where, fmt.Sprintf("(LOWER(j.title) LIKE $%d OR LOWER(c.name) LIKE $%d)",
			len(args), len(args)))
	}
	if !q.Since.IsZero() {
		args = append(args, q.Since)
		where = append(where, fmt.Sprintf("j.first_seen_at >= $%d", len(args)))
	}

	if q.PostedWithin > 0 {
		args = append(args, q.PostedWithin)
		where = append(where, fmt.Sprintf("%s >= now() - make_interval(days => $%d)", listedAt, len(args)))
	}

	orderBy := "a.fit_score DESC NULLS LAST, uj.estimated_score DESC NULLS LAST, j.first_seen_at DESC"
	if q.Sort == "newest" {
		orderBy = listedAt + " DESC, j.id DESC"
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, normalizeLimit(q.Limit, 50, 200), offset)

	sqlText := jobViewSelect + "\nAND " + strings.Join(where, " AND ") +
		fmt.Sprintf("\nORDER BY %s LIMIT $%d OFFSET $%d", orderBy, len(args)-1, len(args))

	rows, err := pool.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()
	var out []JobView
	for rows.Next() {
		var v JobView
		if err := rows.Scan(jobViewScan(&v)...); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetJob returns one of the user's jobs by id, regardless of stage (the
// detail view is always reachable; excluded jobs keep their evidence).
func GetJob(ctx context.Context, pool *pgxpool.Pool, userID, id int64) (*JobView, error) {
	var v JobView
	err := pool.QueryRow(ctx, jobViewSelect+"\nAND j.id = $2", userID, id).
		Scan(jobViewScan(&v)...)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// JobDescription returns a job's plain-text description ("" when none).
func JobDescription(ctx context.Context, pool *pgxpool.Pool, id int64) (string, error) {
	var d *string
	if err := pool.QueryRow(ctx, `SELECT description FROM jobs WHERE id = $1`, id).Scan(&d); err != nil {
		return "", err
	}
	if d == nil {
		return "", nil
	}
	return *d, nil
}
