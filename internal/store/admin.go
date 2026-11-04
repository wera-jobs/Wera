package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminUser is one account as the admin app lists it.
type AdminUser struct {
	ID            int64      `json:"id"`
	Email         string     `json:"email"`
	Name          string     `json:"name"`
	IsAdmin       bool       `json:"is_admin"`
	CreatedAt     time.Time  `json:"created_at"`
	LastLogin     *time.Time `json:"last_login_at"`
	EmailVerified bool       `json:"email_verified"`
	BudgetUSD     float64    `json:"monthly_budget_usd"`
	MonthUSD      float64    `json:"month_spend_usd"`
	HasProfile    bool       `json:"has_profile"`
	Matches       int64      `json:"matches"`
	Scored        int64      `json:"scored"`
	Tracked       int64      `json:"tracked"`
	Applied       int64      `json:"applied"`
	Sessions      int64      `json:"sessions"`
	Extensions    int64      `json:"extensions"`
	JobsAdded     int64      `json:"jobs_added"`
}

const adminUserSelect = `
	SELECT u.id, u.email, u.name, u.is_admin, u.created_at, u.last_login_at, u.email_verified_at IS NOT NULL,
	       u.monthly_budget_usd::float8,
	       coalesce((SELECT sum(cost_usd) FROM llm_usage l WHERE l.user_id = u.id AND l.created_at >= date_trunc('month', now())), 0)::float8,
	       EXISTS (SELECT 1 FROM profiles p WHERE p.user_id = u.id AND p.markdown <> ''),
	       (SELECT count(*) FROM user_jobs uj WHERE uj.user_id = u.id AND uj.stage IN ('scored', 'pending_score')),
	       (SELECT count(*) FROM user_jobs uj WHERE uj.user_id = u.id AND uj.stage = 'scored'),
	       (SELECT count(*) FROM applications a WHERE a.user_id = u.id),
	       (SELECT count(*) FROM applications a WHERE a.user_id = u.id AND a.status NOT IN ('saved', 'not_interested')),
	       (SELECT count(*) FROM sessions s WHERE s.user_id = u.id AND s.expires_at > now()),
	       (SELECT count(*) FROM api_tokens t WHERE t.user_id = u.id),
	       (SELECT count(*) FROM jobs j WHERE j.added_by = u.id)
	FROM users u`

func scanAdminUser(row pgx.Row) (*AdminUser, error) {
	var u AdminUser
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.IsAdmin, &u.CreatedAt, &u.LastLogin, &u.EmailVerified,
		&u.BudgetUSD, &u.MonthUSD, &u.HasProfile, &u.Matches, &u.Scored, &u.Tracked, &u.Applied,
		&u.Sessions, &u.Extensions, &u.JobsAdded)
	return &u, err
}

// AdminUsers lists every account, newest first.
func AdminUsers(ctx context.Context, pool *pgxpool.Pool) ([]AdminUser, error) {
	rows, err := pool.Query(ctx, adminUserSelect+` ORDER BY u.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminUser{}
	for rows.Next() {
		u, err := scanAdminUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// AdminUserByID returns one account (pgx.ErrNoRows when missing).
func AdminUserByID(ctx context.Context, pool *pgxpool.Pool, id int64) (*AdminUser, error) {
	return scanAdminUser(pool.QueryRow(ctx, adminUserSelect+` WHERE u.id = $1`, id))
}

// UserChange is an admin's edit of an account; nil fields stay as they are.
type UserChange struct {
	Name      *string  `json:"name"`
	IsAdmin   *bool    `json:"is_admin"`
	BudgetUSD *float64 `json:"monthly_budget_usd"`
}

// UpdateUser applies an admin's change (pgx.ErrNoRows when the user is gone).
func UpdateUser(ctx context.Context, pool *pgxpool.Pool, id int64, c UserChange) error {
	tag, err := pool.Exec(ctx, `
		UPDATE users SET name = coalesce($2, name), is_admin = coalesce($3, is_admin),
		                 monthly_budget_usd = coalesce($4, monthly_budget_usd)
		WHERE id = $1`, id, c.Name, c.IsAdmin, c.BudgetUSD)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// AdminCount is how many accounts are admins.
func AdminCount(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE is_admin`).Scan(&n)
	return n, err
}

// SignOutEverywhere ends every session of the user and disconnects their
// extensions; it returns how many were ended.
func SignOutEverywhere(ctx context.Context, pool *pgxpool.Pool, userID int64) (int64, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	a, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	if err != nil {
		return 0, err
	}
	b, err := tx.Exec(ctx, `DELETE FROM api_tokens WHERE user_id = $1`, userID)
	if err != nil {
		return 0, err
	}
	return a.RowsAffected() + b.RowsAffected(), tx.Commit(ctx)
}

// ContentItem is a community post or comment under moderation, whatever
// its status.
type ContentItem struct {
	Kind       string          `json:"kind"` // post | comment
	ID         int64           `json:"id"`
	PostID     int64           `json:"post_id"`
	Title      string          `json:"title"` // the post's title (for a comment, its post's)
	Body       string          `json:"body"`
	Status     string          `json:"status"` // published | rejected
	Moderation json.RawMessage `json:"moderation"`
	AuthorID   int64           `json:"author_id"`
	Author     string          `json:"author"`
	CreatedAt  time.Time       `json:"created_at"`
}

// ContentStatuses are the states an admin may set.
var ContentStatuses = map[string]bool{"published": true, "rejected": true}

// AdminContent lists posts and comments, newest first: kind "post",
// "comment" or "" for both; status "published", "rejected" or "" for any.
func AdminContent(ctx context.Context, pool *pgxpool.Pool, kind, status string, limit, offset int) ([]ContentItem, error) {
	rows, err := pool.Query(ctx, `
		SELECT * FROM (
		  SELECT 'post' AS kind, p.id, p.id AS post_id, p.title, p.body, p.status, coalesce(p.moderation, 'null'::jsonb),
		         u.id, u.email, p.created_at
		  FROM posts p JOIN users u ON u.id = p.user_id
		  UNION ALL
		  SELECT 'comment', c.id, c.post_id, p.title, c.body, c.status, coalesce(c.moderation, 'null'::jsonb),
		         u.id, u.email, c.created_at
		  FROM post_comments c JOIN posts p ON p.id = c.post_id JOIN users u ON u.id = c.user_id
		) x
		WHERE ($1 = '' OR x.kind = $1) AND ($2 = '' OR x.status = $2)
		ORDER BY x.created_at DESC LIMIT $3 OFFSET $4`, kind, status, normalizeLimit(limit, 50, 200), max(offset, 0))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ContentItem{}
	for rows.Next() {
		var c ContentItem
		var mod []byte
		if err := rows.Scan(&c.Kind, &c.ID, &c.PostID, &c.Title, &c.Body, &c.Status, &mod, &c.AuthorID, &c.Author, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Moderation = mod
		out = append(out, c)
	}
	return out, rows.Err()
}

func contentTable(kind string) (string, error) {
	switch kind {
	case "post":
		return "posts", nil
	case "comment":
		return "post_comments", nil
	}
	return "", fmt.Errorf("unknown content kind %q", kind)
}

// SetContentStatus publishes or hides a post or comment, overriding the
// AI moderation (pgx.ErrNoRows when it is gone).
func SetContentStatus(ctx context.Context, pool *pgxpool.Pool, kind string, id int64, status string) error {
	table, err := contentTable(kind)
	if err != nil {
		return err
	}
	if !ContentStatuses[status] {
		return fmt.Errorf("unknown status %q", status)
	}
	tag, err := pool.Exec(ctx, `UPDATE `+table+` SET status = $2 WHERE id = $1`, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// DeleteContent removes a post (with its comments) or a comment.
func DeleteContent(ctx context.Context, pool *pgxpool.Pool, kind string, id int64) error {
	table, err := contentTable(kind)
	if err != nil {
		return err
	}
	tag, err := pool.Exec(ctx, `DELETE FROM `+table+` WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// AuditEntry is one recorded admin action.
type AuditEntry struct {
	ID        int64           `json:"id"`
	ActorID   *int64          `json:"actor_id"`
	Actor     string          `json:"actor"`
	Action    string          `json:"action"`
	Target    string          `json:"target"`
	Detail    json.RawMessage `json:"detail"`
	CreatedAt time.Time       `json:"created_at"`
}

// RecordAudit notes an admin action. detail is stored as JSON.
func RecordAudit(ctx context.Context, pool *pgxpool.Pool, actorID int64, actorEmail, action, target string, detail any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `INSERT INTO admin_audit (actor_id, actor_email, action, target, detail) VALUES ($1, $2, $3, $4, $5)`,
		actorID, actorEmail, action, target, raw)
	return err
}

// AuditLog lists admin actions, newest first; before (an id, 0 for none)
// pages back through older ones.
func AuditLog(ctx context.Context, pool *pgxpool.Pool, before int64, limit int) ([]AuditEntry, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, actor_id, actor_email, action, target, detail, created_at FROM admin_audit
		WHERE $1 = 0 OR id < $1 ORDER BY id DESC LIMIT $2`, before, normalizeLimit(limit, 50, 200))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var detail []byte
		if err := rows.Scan(&e.ID, &e.ActorID, &e.Actor, &e.Action, &e.Target, &detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Detail = detail
		out = append(out, e)
	}
	return out, rows.Err()
}

// Overview is the admin home page: the state of the whole service.
type Overview struct {
	Users           int64      `json:"users"`
	UsersReady      int64      `json:"users_with_profile"`
	SignupsWeek     int64      `json:"signups_7d"`
	ActiveWeek      int64      `json:"active_7d"`
	Admins          int64      `json:"admins"`
	CompaniesOn     int64      `json:"companies_enabled"`
	CompaniesFailed int64      `json:"companies_failing"`
	JobsOpen        int64      `json:"jobs_open"`
	JobsNewDay      int64      `json:"jobs_new_24h"`
	JobsAdded       int64      `json:"jobs_added_open"`
	PendingScores   int64      `json:"pending_scores"`
	MonthSpendUSD   float64    `json:"month_spend_usd"`
	EmailQueued     int64      `json:"email_queued"`
	EmailFailedWeek int64      `json:"email_failed_7d"`
	ContentHidden   int64      `json:"content_hidden"`
	LastRunAt       *time.Time `json:"last_run_at"`
	LastRunStatus   *string    `json:"last_run_status"`
}

// AdminOverview gathers the overview in one round trip.
func AdminOverview(ctx context.Context, pool *pgxpool.Pool) (*Overview, error) {
	var o Overview
	err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM users),
		       (SELECT count(*) FROM profiles WHERE markdown <> ''),
		       (SELECT count(*) FROM users WHERE created_at > now() - interval '7 days'),
		       (SELECT count(*) FROM users WHERE last_login_at > now() - interval '7 days'
		          OR id IN (SELECT user_id FROM api_tokens WHERE last_used_at > now() - interval '7 days')),
		       (SELECT count(*) FROM users WHERE is_admin),
		       (SELECT count(*) FROM companies WHERE enabled),
		       (SELECT count(*) FROM companies WHERE enabled AND last_fetch_ok = false),
		       (SELECT count(*) FROM jobs WHERE closed_at IS NULL),
		       (SELECT count(*) FROM jobs WHERE first_seen_at > now() - interval '24 hours'),
		       (SELECT count(*) FROM jobs WHERE closed_at IS NULL AND source = 'manual'),
		       (SELECT count(*) FROM user_jobs uj JOIN jobs j ON j.id = uj.job_id WHERE uj.stage = 'pending_score' AND j.closed_at IS NULL),
		       coalesce((SELECT sum(cost_usd) FROM llm_usage WHERE created_at >= date_trunc('month', now())), 0)::float8,
		       (SELECT count(*) FROM email_outbox WHERE status = 'queued'),
		       (SELECT count(*) FROM email_outbox WHERE status = 'failed' AND created_at > now() - interval '7 days'),
		       (SELECT count(*) FROM posts WHERE status <> 'published') + (SELECT count(*) FROM post_comments WHERE status <> 'published'),
		       (SELECT started_at FROM runs ORDER BY id DESC LIMIT 1),
		       (SELECT status FROM runs ORDER BY id DESC LIMIT 1)`).
		Scan(&o.Users, &o.UsersReady, &o.SignupsWeek, &o.ActiveWeek, &o.Admins, &o.CompaniesOn, &o.CompaniesFailed,
			&o.JobsOpen, &o.JobsNewDay, &o.JobsAdded, &o.PendingScores, &o.MonthSpendUSD, &o.EmailQueued,
			&o.EmailFailedWeek, &o.ContentHidden, &o.LastRunAt, &o.LastRunStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return &o, nil
	}
	return &o, err
}
