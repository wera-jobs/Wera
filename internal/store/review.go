package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WeeklyReview is one week of the user's search, from their own tracker
// and matches.
type WeeklyReview struct {
	Week        time.Time    `json:"week"` // Monday, UTC
	Applied     []ReviewItem `json:"applied"`
	AppliedPrev int          `json:"applied_prev"` // the week before
	Replies     []ReviewItem `json:"replies"`      // moved to interviewing, offer or rejected this week
	StrongNew   int          `json:"strong_new"`   // strong matches scored this week
	StrongActed int          `json:"strong_acted"` // of them, saved, applied or dismissed
	StrongBest  *ReviewItem  `json:"strong_best"`  // the best one not acted on
	Waiting     int          `json:"waiting"`      // applied 14+ days ago, still applied, no next step
	OldApplied  int          `json:"old_applied"`  // of this week's, to postings up 60+ days
	Gaps        []ReviewGap  `json:"gaps"`         // skills missing in jobs that went nowhere (last 4 weeks)
	GapsOf      int          `json:"gaps_of"`      // how many such jobs
	MinScore    int          `json:"min_score"`    // the user's strong-match score
	WeeklyGoal  int          `json:"weekly_goal"`  // applications a week the user aims for; 0 is no goal
	// GoalStreakWeeks counts the weeks in a row, up to this one, that met
	// the goal. A week still in progress that hasn't met it yet doesn't
	// break the streak: it just isn't counted.
	GoalStreakWeeks int `json:"goal_streak_weeks"`
}

// ReviewItem is a job in the review.
type ReviewItem struct {
	JobID   int64  `json:"job_id"`
	Title   string `json:"title"`
	Company string `json:"company"`
	Status  string `json:"status,omitempty"`
	Score   *int   `json:"score,omitempty"`
}

// ReviewGap is a skill and how many of the jobs that went nowhere wanted it.
type ReviewGap struct {
	Skill string `json:"skill"`
	Count int    `json:"count"`
}

// GetWeeklyReview builds the review of the week starting on monday (UTC).
func GetWeeklyReview(ctx context.Context, pool *pgxpool.Pool, userID int64, monday time.Time) (*WeeklyReview, error) {
	end := monday.AddDate(0, 0, 7)
	r := &WeeklyReview{Week: monday, Applied: []ReviewItem{}, Replies: []ReviewItem{}, Gaps: []ReviewGap{}}
	items := func(sql string, args ...any) ([]ReviewItem, error) {
		rows, err := pool.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []ReviewItem{}
		for rows.Next() {
			var it ReviewItem
			if err := rows.Scan(&it.JobID, &it.Title, &it.Company, &it.Status, &it.Score); err != nil {
				return nil, err
			}
			out = append(out, it)
		}
		return out, rows.Err()
	}
	var err error
	// Sent this week: the first time each application left "saved".
	if r.Applied, err = items(`
		WITH sent AS (
		  SELECT job_id, min(at) AS at FROM application_changes
		  WHERE user_id = $1 AND status IN ('applied', 'interviewing', 'offer', 'rejected') GROUP BY job_id
		)
		SELECT j.id, j.title, c.name, ap.status, a.fit_score
		FROM sent s JOIN applications ap ON ap.user_id = $1 AND ap.job_id = s.job_id
		JOIN jobs j ON j.id = s.job_id JOIN companies c ON c.id = j.company_id
		LEFT JOIN user_jobs uj ON uj.user_id = $1 AND uj.job_id = j.id LEFT JOIN analyses a ON a.id = uj.analysis_id
		WHERE s.at >= $2 AND s.at < $3 ORDER BY s.at`, userID, monday, end); err != nil {
		return nil, fmt.Errorf("review applied: %w", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM (SELECT job_id, min(at) AS at FROM application_changes
		  WHERE user_id = $1 AND status IN ('applied', 'interviewing', 'offer', 'rejected') GROUP BY job_id) s
		WHERE s.at >= $2 AND s.at < $3`, userID, monday.AddDate(0, 0, -7), monday).Scan(&r.AppliedPrev); err != nil {
		return nil, fmt.Errorf("review applied before: %w", err)
	}
	if r.Replies, err = items(`
		SELECT DISTINCT ON (j.id) j.id, j.title, c.name, ch.status, a.fit_score
		FROM application_changes ch JOIN jobs j ON j.id = ch.job_id JOIN companies c ON c.id = j.company_id
		LEFT JOIN user_jobs uj ON uj.user_id = $1 AND uj.job_id = j.id LEFT JOIN analyses a ON a.id = uj.analysis_id
		WHERE ch.user_id = $1 AND ch.status IN ('interviewing', 'offer', 'rejected') AND ch.at >= $2 AND ch.at < $3
		ORDER BY j.id, ch.at DESC`, userID, monday, end); err != nil {
		return nil, fmt.Errorf("review replies: %w", err)
	}
	settings, err := GetSettings(ctx, pool, userID)
	if err != nil {
		return nil, fmt.Errorf("review settings: %w", err)
	}
	r.MinScore = settings.Notifications.MinScore
	r.WeeklyGoal = settings.WeeklyApplicationGoal
	if r.GoalStreakWeeks, err = GoalStreak(ctx, pool, userID, monday, r.WeeklyGoal); err != nil {
		return nil, err
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE ap.status IS NOT NULL)
		FROM user_jobs uj JOIN analyses a ON a.id = uj.analysis_id
		LEFT JOIN applications ap ON ap.user_id = uj.user_id AND ap.job_id = uj.job_id
		WHERE uj.user_id = $1 AND a.fit_score >= $4 AND a.created_at >= $2 AND a.created_at < $3`,
		userID, monday, end, r.MinScore).Scan(&r.StrongNew, &r.StrongActed); err != nil {
		return nil, fmt.Errorf("review strong: %w", err)
	}
	best, err := items(`
		SELECT j.id, j.title, c.name, '', a.fit_score
		FROM user_jobs uj JOIN analyses a ON a.id = uj.analysis_id JOIN jobs j ON j.id = uj.job_id AND j.closed_at IS NULL
		JOIN companies c ON c.id = j.company_id
		LEFT JOIN applications ap ON ap.user_id = uj.user_id AND ap.job_id = uj.job_id
		WHERE uj.user_id = $1 AND uj.stage = 'scored' AND a.fit_score >= $4 AND a.created_at >= $2 AND a.created_at < $3 AND ap.status IS NULL
		ORDER BY a.fit_score DESC LIMIT 1`, userID, monday, end, r.MinScore)
	if err != nil {
		return nil, fmt.Errorf("review best: %w", err)
	}
	if len(best) > 0 {
		r.StrongBest = &best[0]
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM applications
		WHERE user_id = $1 AND status = 'applied' AND archived_at IS NULL AND next_step_at IS NULL
		  AND coalesce(applied_at, updated_at) < $2::timestamptz - interval '14 days'`, userID, end).Scan(&r.Waiting); err != nil {
		return nil, fmt.Errorf("review waiting: %w", err)
	}
	ids := make([]int64, 0, len(r.Applied))
	for _, a := range r.Applied {
		ids = append(ids, a.JobID)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE id = ANY($1)
		  AND coalesce(posted_at, first_seen_at) < now() - interval '60 days'`, ids).Scan(&r.OldApplied); err != nil {
		return nil, fmt.Errorf("review old postings: %w", err)
	}
	// Skills wanted by jobs that went nowhere: sent in the last four weeks
	// (at least a week ago), rejected or still waiting.
	rows, err := pool.Query(ctx, `
		WITH nowhere AS (
		  SELECT ap.job_id, a.skills_missing FROM applications ap
		  JOIN user_jobs uj ON uj.user_id = ap.user_id AND uj.job_id = ap.job_id JOIN analyses a ON a.id = uj.analysis_id
		  WHERE ap.user_id = $1 AND ap.status IN ('applied', 'rejected')
		    AND coalesce(ap.applied_at, ap.updated_at) >= $2::timestamptz - interval '28 days'
		    AND coalesce(ap.applied_at, ap.updated_at) < $2::timestamptz - interval '7 days'
		)
		SELECT (SELECT count(*) FROM nowhere), s, count(*) FROM nowhere, unnest(coalesce(skills_missing, '{}')) s
		GROUP BY s ORDER BY count(*) DESC, s LIMIT 5`, userID, end)
	if err != nil {
		return nil, fmt.Errorf("review gaps: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var g ReviewGap
		if err := rows.Scan(&r.GapsOf, &g.Skill, &g.Count); err != nil {
			return nil, err
		}
		if g.Count >= 2 {
			r.Gaps = append(r.Gaps, g)
		}
	}
	return r, rows.Err()
}

// maxStreakWeeks bounds how far back a goal streak is counted.
const maxStreakWeeks = 104

// GoalStreak counts the weeks in a row, ending with the week that starts
// on monday (UTC), in which the user sent at least goal applications. When
// that week hasn't reached the goal yet, the streak is the run of weeks
// before it, so it isn't shown as broken until the week is over. A goal of
// 0 has no streak.
func GoalStreak(ctx context.Context, pool *pgxpool.Pool, userID int64, monday time.Time, goal int) (int, error) {
	if goal <= 0 {
		return 0, nil
	}
	end := monday.AddDate(0, 0, 7)
	rows, err := pool.Query(ctx, `
		SELECT date_trunc('week', s.at AT TIME ZONE 'UTC'), count(*)
		FROM (SELECT job_id, min(at) AS at FROM application_changes
		      WHERE user_id = $1 AND status IN ('applied', 'interviewing', 'offer', 'rejected') GROUP BY job_id) s
		WHERE s.at >= $2 AND s.at < $3
		GROUP BY 1`, userID, monday.AddDate(0, 0, -7*maxStreakWeeks), end)
	if err != nil {
		return 0, fmt.Errorf("goal streak: %w", err)
	}
	defer rows.Close()
	sent := map[time.Time]int{}
	for rows.Next() {
		var week time.Time
		var n int
		if err := rows.Scan(&week, &n); err != nil {
			return 0, err
		}
		sent[time.Date(week.Year(), week.Month(), week.Day(), 0, 0, 0, 0, time.UTC)] = n
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	streak := 0
	week := monday
	if sent[week] < goal {
		week = week.AddDate(0, 0, -7)
	}
	for ; streak < maxStreakWeeks && sent[week] >= goal; week = week.AddDate(0, 0, -7) {
		streak++
	}
	return streak, nil
}
