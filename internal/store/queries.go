package store

import (
	"context"
	"encoding/json"
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
	MinPay       int    // a year's pay in US dollars (an hourly rate counts 2,080 hours); jobs stating no pay or pay in another currency are left out
	Sponsorship  string // "yes" | "unknown" (only these two are meaningful)
	WorkMode     string
	Status       string // application status
	Archived     bool   // only archived applications (otherwise a status list leaves them out)
	Q            string // title/company search
	Since        time.Time
	PostedWithin int    // days; 0 = any age (see listedAt)
	Sort         string // "score" (default) | "newest"
	Followed     bool   // only companies the user follows
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
	DismissReason   *string    `json:"dismiss_reason"` // why the user passed on it, when they said
	NextStep        *string    `json:"next_step"`
	NextStepAt      *time.Time `json:"next_step_at"`
	Contact         *Contact   `json:"contact"`
	ArchivedAt      *time.Time `json:"archived_at"` // off the tracker's board, history kept
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
	// The pay the posting states (see internal/pay); nil when it states none.
	PayMin      *float64 `json:"pay_min"`
	PayMax      *float64 `json:"pay_max"`
	PayCurrency *string  `json:"pay_currency"`
	PayPeriod   *string  `json:"pay_period"`
	// FitParts are the ratings the fit score was computed from (see
	// scoring.Ratings.Score); nil for jobs scored before ratings existed.
	FitParts *FitParts `json:"fit_parts"`
	// ResumeUsed is the resume (and letter) that went with the application:
	// what the user set, else what the extension last attached.
	ResumeUsed *ResumeUsed `json:"resume_used"`
	apResume   *string
	apLetter   *bool
	runFiles   *string // kind|name|letter of the latest run that attached a resume
	// LogoURL is where the company's logo is served; empty when it has none.
	LogoURL string `json:"logo_url,omitempty"`
	logoAt  *time.Time
	// Early: the posting went up in the last three days and Wera found it
	// within a day of that, so applying now puts the user among the first.
	// Never claimed for a posting with no date.
	Early bool `json:"early"`
	// CompanyFollowed: the user follows the company (their new roles show up as a notice).
	CompanyFollowed bool `json:"company_followed"`
	// AlsoIn lists the other places the same role (same company, title and
	// text) is posted for among the user's matches; lists show the role
	// once (see oneSibling).
	AlsoIn  []string `json:"also_in,omitempty"`
	ratings *string  // the ratings object as the model wrote it, parsed by finish
}

// FitParts are a scored job's parts of fit, each 0-10, and whether
// something rules the job out (a blocker), as the scorer rated them.
type FitParts struct {
	Role      int  `json:"role"`
	Seniority int  `json:"seniority"`
	Skills    int  `json:"skills"`
	Domain    int  `json:"domain"`
	Location  int  `json:"location"`
	Blocker   bool `json:"blocker"`
}

// ResumeUsed says which resume went with an application, and who said so.
type ResumeUsed struct {
	Kind   string `json:"kind"`   // tailored | base | upload; empty when only the letter is known
	Name   string `json:"name"`   // the file name, when the extension attached it
	Letter bool   `json:"letter"` // a cover letter went too
	By     string `json:"by"`     // you | extension
}

func resumeUsed(apKind *string, apLetter *bool, run *string) *ResumeUsed {
	var r *ResumeUsed
	if run != nil {
		if p := strings.SplitN(*run, "|", 3); len(p) == 3 {
			r = &ResumeUsed{Kind: p[0], Name: p[1], Letter: p[2] == "true", By: "extension"}
		}
	}
	if apKind != nil {
		r = &ResumeUsed{Kind: *apKind, By: "you", Letter: r != nil && r.Letter}
	}
	if apLetter != nil {
		if r == nil {
			r = &ResumeUsed{By: "you"}
		}
		r.Letter = *apLetter
	}
	return r
}

// finish fills what a scan leaves raw: the fit parts, parsed leniently
// (a reply that doesn't parse, or rates outside 0-10, gives none).
func (v *JobView) finish() {
	if v.logoAt != nil {
		v.LogoURL = LogoURL(v.CompanyID, *v.logoAt)
	}
	v.ResumeUsed = resumeUsed(v.apResume, v.apLetter, v.runFiles)
	if v.ratings == nil || v.FitScore == nil {
		return
	}
	var p FitParts
	if json.Unmarshal([]byte(*v.ratings), &p) != nil {
		return
	}
	for _, r := range []int{p.Role, p.Seniority, p.Skills, p.Domain, p.Location} {
		if r < 0 || r > 10 {
			return
		}
	}
	v.FitParts = &p
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
	       ap.status, ap.notes, ap.dismiss_reason, ap.next_step, ap.next_step_at, ap.contact, ap.archived_at, uj.estimated_score,
	       EXISTS (SELECT 1 FROM quick_grades q WHERE q.user_id = uj.user_id AND q.job_id = j.id),
	       (SELECT max(r.updated_at) FROM autofill_runs r WHERE r.user_id = uj.user_id AND r.job_id = j.id),
	       j.closed_at, j.pay_min::float8, j.pay_max::float8, j.pay_currency, j.pay_period,
	       substring(a.raw #>> '{}' from '"ratings"\s*:\s*(\{[^}]*\})'),
	       COALESCE(j.posted_at > now() - interval '3 days' AND j.first_seen_at <= j.posted_at + interval '1 day', false),
	       (SELECT l.checked_at FROM company_logos l WHERE l.company_id = j.company_id AND l.found),
	       ap.resume_kind, ap.letter_sent,
	       (SELECT r.resume_kind || '|' || coalesce(r.resume_name, '') || '|' || r.letter FROM autofill_runs r
	        WHERE r.user_id = uj.user_id AND r.job_id = j.id AND r.resume_kind IS NOT NULL ORDER BY r.updated_at DESC LIMIT 1),
	       EXISTS (SELECT 1 FROM company_follows f WHERE f.user_id = uj.user_id AND f.company_id = j.company_id)
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
		&v.AppStatus, &v.AppNotes, &v.DismissReason, &v.NextStep, &v.NextStepAt, &v.Contact, &v.ArchivedAt, &v.EstimatedScore, &v.EstimateAI, &v.AutofilledAt, &v.ClosedAt,
		&v.PayMin, &v.PayMax, &v.PayCurrency, &v.PayPeriod, &v.ratings, &v.Early, &v.logoAt, &v.apResume, &v.apLetter, &v.runFiles, &v.CompanyFollowed}
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

// followedOnly keeps a row of jobs j (for user uj) when the user follows
// its company; the follows primary key (user_id, company_id) answers it.
const followedOnly = "EXISTS (SELECT 1 FROM company_follows f WHERE f.user_id = uj.user_id AND f.company_id = j.company_id)"

// oneSibling keeps one posting of a role posted once per city (same
// company and text_hash, see migration 0052) in a list of matches: the
// best scored, then best estimated, then the oldest. A posting the user
// tracks always shows, and only open, untracked or saved siblings in the
// queue hide one.
const oneSibling = `(ap.status IS NOT NULL OR j.text_hash IS NULL OR NOT EXISTS (
	  SELECT 1 FROM jobs j2
	  JOIN user_jobs uj2 ON uj2.job_id = j2.id AND uj2.user_id = uj.user_id AND uj2.stage IN ('scored', 'pending_score')
	  LEFT JOIN analyses a2 ON a2.id = uj2.analysis_id
	  LEFT JOIN applications ap2 ON ap2.job_id = j2.id AND ap2.user_id = uj2.user_id
	  WHERE j2.company_id = j.company_id AND j2.text_hash = j.text_hash AND j2.id <> j.id AND j2.closed_at IS NULL
	    AND (ap2.status IS NULL OR ap2.status = 'saved') AND ap2.archived_at IS NULL
	    AND (COALESCE(a2.fit_score, -1), COALESCE(uj2.estimated_score, -1), -j2.id)
	      > (COALESCE(a.fit_score, -1), COALESCE(uj.estimated_score, -1), -j.id)))`

// maxAlsoIn bounds the other places listed for one role.
const maxAlsoIn = 10

// fillAlsoIn sets AlsoIn on each view: where else the same role is posted
// among the user's open matches.
func fillAlsoIn(ctx context.Context, pool *pgxpool.Pool, userID int64, views []JobView) error {
	if len(views) == 0 {
		return nil
	}
	ids := make([]int64, len(views))
	for i, v := range views {
		ids[i] = v.ID
	}
	rows, err := pool.Query(ctx, `
		SELECT j.id, (array_agg(DISTINCT COALESCE(NULLIF(s.location_raw, ''), 'Unlisted')))[1:$3]
		FROM jobs j
		JOIN jobs s ON s.company_id = j.company_id AND s.text_hash = j.text_hash AND s.id <> j.id AND s.closed_at IS NULL
		JOIN user_jobs us ON us.job_id = s.id AND us.user_id = $1 AND us.stage IN ('scored', 'pending_score')
		WHERE j.id = ANY($2::bigint[]) AND j.text_hash IS NOT NULL
		  AND COALESCE(NULLIF(s.location_raw, ''), 'Unlisted') IS DISTINCT FROM COALESCE(NULLIF(j.location_raw, ''), 'Unlisted')
		GROUP BY j.id`, userID, ids, maxAlsoIn)
	if err != nil {
		return fmt.Errorf("load other places: %w", err)
	}
	defer rows.Close()
	at := map[int64][]string{}
	for rows.Next() {
		var id int64
		var locs []string
		if err := rows.Scan(&id, &locs); err != nil {
			return err
		}
		at[id] = locs
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range views {
		views[i].AlsoIn = at[views[i].ID]
	}
	return nil
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

	if q.Archived {
		where = append(where, "ap.archived_at IS NOT NULL")
	} else if q.Status != "" {
		where = append(where, "ap.archived_at IS NULL")
	}
	if q.Status == "" && !q.Archived {
		where = append(where, "j.closed_at IS NULL")
		where = append(where, "uj.stage IN ('scored','pending_score')")
		where = append(where, oneSibling)
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
	if q.MinPay > 0 {
		args = append(args, q.MinPay)
		// The minimum is US dollars: pay in another currency can't be
		// compared without a rate, so it doesn't count as meeting it.
		where = append(where, fmt.Sprintf("COALESCE(j.pay_currency, 'USD') = 'USD' AND COALESCE(j.pay_max, j.pay_min) * CASE j.pay_period WHEN 'hour' THEN 2080 ELSE 1 END >= $%d", len(args)))
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

	if q.Followed {
		where = append(where, followedOnly)
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
		v.finish()
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if q.Status == "" && !q.Archived {
		if err := fillAlsoIn(ctx, pool, userID, out); err != nil {
			return nil, err
		}
	}
	return out, nil
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
	v.finish()
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
