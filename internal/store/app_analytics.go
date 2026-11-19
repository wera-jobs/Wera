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
	         min(c.at) FILTER (WHERE c.status IN ('applied', 'interviewing', 'offer', 'rejected')) AS sent,
	         min(c.at) FILTER (WHERE c.status = 'applied') AS applied,
	         min(c.at) FILTER (WHERE c.status IN ('interviewing', 'offer')) AS interview,
	         min(c.at) FILTER (WHERE c.status = 'offer') AS offer,
	         min(c.at) FILTER (WHERE c.status IN ('interviewing', 'offer', 'rejected')) AS response
	  FROM applications ap
	  JOIN jobs j ON j.id = ap.job_id
	  LEFT JOIN application_changes c ON c.user_id = ap.user_id AND c.job_id = ap.job_id
	  WHERE ap.user_id = $1
	  GROUP BY ap.job_id, j.source
	)`

// ApplicationAnalytics computes the summary for one user, over the last
// `weeks` weeks for the weekly series. A user with no applications gets
// zeros and empty lists.
func ApplicationAnalytics(ctx context.Context, pool *pgxpool.Pool, userID int64, weeks int) (*AppAnalytics, error) {
	out := &AppAnalytics{Now: map[string]int{}, Weeks: []WeekStat{}, Sources: []SourceStat{}}
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

	rows, err = pool.Query(ctx, appFirsts+`
		SELECT source, count(sent), count(response), count(interview) FROM firsts
		WHERE sent IS NOT NULL GROUP BY source ORDER BY count(sent) DESC, source`, userID)
	if err != nil {
		return nil, fmt.Errorf("analytics sources: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s SourceStat
		if err := rows.Scan(&s.Source, &s.Applied, &s.Responded, &s.Interview); err != nil {
			return nil, err
		}
		out.Sources = append(out.Sources, s)
	}
	return out, rows.Err()
}
