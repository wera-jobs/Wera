package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AppAnalytics summarizes how a user's applications are going.
type AppAnalytics struct {
	// Now: applications in each status at present (archived ones included).
	Now map[string]int `json:"now"`
	// Reached: applications that ever got to each stage. Applied counts
	// anything sent (applied, interviewing, offer or rejected).
	Reached struct {
		Applied      int `json:"applied"`
		Interviewing int `json:"interviewing"`
		Offer        int `json:"offer"`
	} `json:"reached"`
	// Weeks: applications sent each week (by the first time they were
	// applied), oldest first, and how many of them heard back (an
	// interview, an offer or a rejection since).
	Weeks []WeekStat `json:"weeks"`
	// Sources: the same by applicant system (the posting's source).
	Sources []SourceStat `json:"sources"`
	// Resumes: the same by the resume sent (tailored, base, upload or
	// unknown), in Source.
	Resumes []SourceStat `json:"resumes"`
	// Fits: the same by the job's fit score when applied to ("80+",
	// "60-79", "under 60" or "unscored"), in Source, highest band first.
	Fits []SourceStat `json:"fits"`
	// Median days from applying to a first interview, and from a first
	// interview to an offer; nil until there is one of each.
	DaysToInterview *float64 `json:"days_to_interview"`
	DaysToOffer     *float64 `json:"days_to_offer"`
}

// WeekStat is one week's applications.
type WeekStat struct {
	Week      time.Time `json:"week"` // Monday, UTC
	Applied   int       `json:"applied"`
	Responded int       `json:"responded"`
	Interview int       `json:"interview"`
}

// SourceStat is one applicant system's applications.
type SourceStat struct {
	Source    string `json:"source"`
	Applied   int    `json:"applied"`
	Responded int    `json:"responded"`
	Interview int    `json:"interview"`
}

// The per-application facts the summaries are built from: when it was
// first applied to, first interviewed for, first offered, first rejected.
const appFirsts = `
	WITH firsts AS (
	  SELECT ap.job_id, j.source,
	         coalesce(ap.resume_kind, (SELECT r.resume_kind FROM autofill_runs r WHERE r.user_id = ap.user_id AND r.job_id = ap.job_id
	                                   AND r.resume_kind IS NOT NULL ORDER BY r.updated_at DESC LIMIT 1), 'unknown') AS resume,
	         coalesce((SELECT CASE WHEN a.fit_score >= 80 THEN '80+' WHEN a.fit_score >= 60 THEN '60-79' ELSE 'under 60' END
	                   FROM user_jobs uj JOIN analyses a ON a.id = uj.analysis_id
	                   WHERE uj.user_id = ap.user_id AND uj.job_id = ap.job_id AND a.fit_score IS NOT NULL), 'unscored') AS fit,
	         min(c.at) FILTER (WHERE c.status IN ('applied', 'interviewing', 'offer', 'rejected')) AS sent,
	         min(c.at) FILTER (WHERE c.status = 'applied') AS applied,
	         min(c.at) FILTER (WHERE c.status IN ('interviewing', 'offer')) AS interview,
	         min(c.at) FILTER (WHERE c.status = 'offer') AS offer,
	         min(c.at) FILTER (WHERE c.status IN ('interviewing', 'offer', 'rejected')) AS response
	  FROM applications ap
	  JOIN jobs j ON j.id = ap.job_id
	  LEFT JOIN application_changes c ON c.user_id = ap.user_id AND c.job_id = ap.job_id
	  WHERE ap.user_id = $1
	  GROUP BY ap.job_id, j.source, ap.user_id, ap.resume_kind
	)`

// ApplicationAnalytics computes the summary for one user, over the last
// `weeks` weeks for the weekly series. A user with no applications gets
// zeros and empty lists.
func ApplicationAnalytics(ctx context.Context, pool *pgxpool.Pool, userID int64, weeks int) (*AppAnalytics, error) {
	out := &AppAnalytics{Now: map[string]int{}, Weeks: []WeekStat{}, Sources: []SourceStat{}, Resumes: []SourceStat{}, Fits: []SourceStat{}}
	for _, s := range []string{"saved", "applied", "interviewing", "offer", "rejected", "not_interested"} {
		out.Now[s] = 0
	}
	rows, err := pool.Query(ctx, `SELECT status, count(*) FROM applications WHERE user_id = $1 GROUP BY status`, userID)
	if err != nil {
		return nil, fmt.Errorf("analytics now: %w", err)
	}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return nil, err
		}
		out.Now[s] = n
	}
	rows.Close()

	err = pool.QueryRow(ctx, appFirsts+`
		SELECT count(sent), count(interview), count(offer),
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM interview - coalesce(applied, sent)) / 86400)
		         FILTER (WHERE interview IS NOT NULL AND coalesce(applied, sent) < interview),
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM offer - interview) / 86400)
		         FILTER (WHERE offer IS NOT NULL AND interview < offer)
		FROM firsts`, userID).Scan(&out.Reached.Applied, &out.Reached.Interviewing, &out.Reached.Offer, &out.DaysToInterview, &out.DaysToOffer)
	if err != nil {
		return nil, fmt.Errorf("analytics reached: %w", err)
	}

	rows, err = pool.Query(ctx, appFirsts+`,
	  span AS (
	    SELECT generate_series(date_trunc('week', now() AT TIME ZONE 'UTC') - make_interval(weeks => $2 - 1),
	                           date_trunc('week', now() AT TIME ZONE 'UTC'), interval '1 week') AS week
	  )
		SELECT s.week AT TIME ZONE 'UTC', count(f.sent), count(f.response), count(f.interview)
		FROM span s LEFT JOIN firsts f ON date_trunc('week', f.sent AT TIME ZONE 'UTC') = s.week
		GROUP BY s.week ORDER BY s.week`, userID, weeks)
	if err != nil {
		return nil, fmt.Errorf("analytics weeks: %w", err)
	}
	for rows.Next() {
		var w WeekStat
		if err := rows.Scan(&w.Week, &w.Applied, &w.Responded, &w.Interview); err != nil {
			rows.Close()
			return nil, err
		}
		out.Weeks = append(out.Weeks, w)
	}
	rows.Close()

	// Bands read best first; the other breakdowns biggest first.
	const bandOrder = `array_position(ARRAY['80+', '60-79', 'under 60', 'unscored'], fit)`
	for _, by := range []struct {
		col, order string
		out        *[]SourceStat
	}{{"source", "count(sent) DESC, 1", &out.Sources}, {"resume", "count(sent) DESC, 1", &out.Resumes}, {"fit", bandOrder, &out.Fits}} {
		rows, err := pool.Query(ctx, appFirsts+`
			SELECT `+by.col+`, count(sent), count(response), count(interview) FROM firsts
			WHERE sent IS NOT NULL GROUP BY 1 ORDER BY `+by.order, userID)
		if err != nil {
			return nil, fmt.Errorf("analytics by %s: %w", by.col, err)
		}
		for rows.Next() {
			var s SourceStat
			if err := rows.Scan(&s.Source, &s.Applied, &s.Responded, &s.Interview); err != nil {
				rows.Close()
				return nil, err
			}
			*by.out = append(*by.out, s)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
