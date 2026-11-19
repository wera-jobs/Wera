package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Notification is an in-app notice.
type Notification struct {
	ID        int64      `json:"id"`
	Kind      string     `json:"kind"` // strong_matches | posting_closed | reminder
	JobID     *int64     `json:"job_id"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	At        *time.Time `json:"at"` // a reminder's time, shown in the user's own zone
	CreatedAt time.Time  `json:"created_at"`
	ReadAt    *time.Time `json:"read_at"`
}

// ListNotifications returns the user's newest notices, up to limit, and
// how many are unread.
func ListNotifications(ctx context.Context, pool *pgxpool.Pool, userID int64, limit int) ([]Notification, int, error) {
	limit = normalizeLimit(limit, 30, 100)
	rows, err := pool.Query(ctx, `
		SELECT id, kind, job_id, title, body, at, created_at, read_at FROM notifications
		WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.Kind, &n.JobID, &n.Title, &n.Body, &n.At, &n.CreatedAt, &n.ReadAt); err != nil {
			return nil, 0, err
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var unread int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND read_at IS NULL`, userID).Scan(&unread)
	return out, unread, err
}

// MarkNotificationsRead marks the given notices read, or all of the
// user's when ids is empty.
func MarkNotificationsRead(ctx context.Context, pool *pgxpool.Pool, userID int64, ids []int64) error {
	_, err := pool.Exec(ctx, `
		UPDATE notifications SET read_at = now()
		WHERE user_id = $1 AND read_at IS NULL AND (coalesce(cardinality($2::bigint[]), 0) = 0 OR id = ANY($2))`, userID, ids)
	if err != nil {
		return fmt.Errorf("mark notifications read: %w", err)
	}
	return nil
}

// MakeNotifications creates the notices due at now, for every user, and
// returns how many it created or brought back as unread. Running it again
// changes nothing new (each notice has a key).
//
//   - strong_matches: one per user per day (UTC), "N new strong matches",
//     kept current through the day, for users with strong-match alerts on:
//     open jobs scored that day at or above their alert score, not yet
//     dealt with. It turns unread again when the count grows.
//   - posting_closed: a saved, applied or interviewing job whose posting
//     closed in the last two days.
//   - reminder: interviews and next steps due within the next day (see
//     DueReminders), on applications that aren't archived.
func MakeNotifications(ctx context.Context, pool *pgxpool.Pool, now time.Time) (int, error) {
	total := 0
	tag, err := pool.Exec(ctx, `
		WITH strong AS (
		  SELECT uj.user_id, count(*) AS n,
		         (array_agg(j.id ORDER BY a.fit_score DESC, j.id))[1] AS top_job,
		         (array_agg(j.title || ' at ' || c.name ORDER BY a.fit_score DESC, j.id))[1] AS top
		  FROM user_jobs uj
		  JOIN users u ON u.id = uj.user_id
		  JOIN analyses a ON a.id = uj.analysis_id
		  JOIN jobs j ON j.id = uj.job_id AND j.closed_at IS NULL
		  JOIN companies c ON c.id = j.company_id
		  LEFT JOIN applications ap ON ap.user_id = uj.user_id AND ap.job_id = uj.job_id
		  WHERE uj.stage = 'scored'
		    AND a.created_at >= date_trunc('day', $1::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
		    AND a.created_at <= $1
		    AND coalesce((u.settings->'notifications'->>'strong_matches')::bool, true)
		    AND a.fit_score >= coalesce((u.settings->'notifications'->>'min_score')::int, 80)
		    AND ap.status IS NULL
		  GROUP BY uj.user_id
		)
		INSERT INTO notifications (user_id, kind, key, job_id, title, body, created_at)
		SELECT user_id, 'strong_matches', 'matches:' || to_char($1::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD'), top_job,
		       n || CASE WHEN n = 1 THEN ' new strong match' ELSE ' new strong matches' END,
		       'Best: ' || top, $1
		FROM strong
		ON CONFLICT (user_id, key) DO UPDATE
		SET title = EXCLUDED.title, body = EXCLUDED.body, job_id = EXCLUDED.job_id,
		    created_at = EXCLUDED.created_at, read_at = NULL
		WHERE notifications.title <> EXCLUDED.title`, now)
	if err != nil {
		return 0, fmt.Errorf("strong match notices: %w", err)
	}
	total += int(tag.RowsAffected())

	tag, err = pool.Exec(ctx, `
		INSERT INTO notifications (user_id, kind, key, job_id, title, body, created_at)
		SELECT ap.user_id, 'posting_closed', 'closed:' || j.id, j.id,
		       'A posting you''re tracking closed', j.title || ' at ' || c.name, $1
		FROM applications ap
		JOIN jobs j ON j.id = ap.job_id JOIN companies c ON c.id = j.company_id
		WHERE ap.status IN ('saved', 'applied', 'interviewing') AND ap.archived_at IS NULL
		  AND j.closed_at > $1::timestamptz - interval '2 days' AND j.closed_at <= $1
		ON CONFLICT (user_id, key) DO NOTHING`, now)
	if err != nil {
		return total, fmt.Errorf("closed posting notices: %w", err)
	}
	total += int(tag.RowsAffected())

	due, err := DueReminders(ctx, pool, now, now.Add(24*time.Hour))
	if err != nil {
		return total, err
	}
	for _, r := range due {
		title := "Next step: " + r.What
		if r.Event {
			title = eventTitle(r.What)
		}
		key := fmt.Sprintf("reminder:%d:%t:%s:%d", r.JobID, r.Event, r.What, r.At.Unix())
		tag, err := pool.Exec(ctx, `
			INSERT INTO notifications (user_id, kind, key, job_id, title, body, at, created_at)
			VALUES ($1, 'reminder', $2, $3, $4, $5, $6, $7)
			ON CONFLICT (user_id, key) DO NOTHING`,
			r.UserID, key, r.JobID, title, r.Title+" at "+r.Company, r.At, now)
		if err != nil {
			return total, fmt.Errorf("reminder notice: %w", err)
		}
		total += int(tag.RowsAffected())
	}
	return total, nil
}

func eventTitle(kind string) string {
	switch kind {
	case "phone_screen":
		return "Phone screen"
	case "interview":
		return "Interview"
	case "onsite":
		return "Onsite"
	case "assessment":
		return "Assessment due"
	case "offer_call":
		return "Offer call"
	}
	return "Event"
}
