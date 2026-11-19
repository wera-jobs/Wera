package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ProblemReport is one "Report a problem" from the extension.
type ProblemReport struct {
	ID          int64      `json:"id"`
	UserID      int64      `json:"user_id"`
	UserEmail   string     `json:"user_email"`
	UserName    string     `json:"user_name"`
	CreatedAt   time.Time  `json:"created_at"`
	Source      string     `json:"source"`
	Note        string     `json:"note"`
	Page        string     `json:"page"`
	Version     string     `json:"version"`
	DetailsSize int        `json:"details_size"`
	Details     string     `json:"details,omitempty"`
	ResolvedAt  *time.Time `json:"resolved_at"`
}

// AddProblemReport saves a report and returns its id.
func AddProblemReport(ctx context.Context, pool *pgxpool.Pool, userID int64, source, note, page, version, details string) (int64, error) {
	var id int64
	err := pool.QueryRow(ctx, `
		INSERT INTO problem_reports (user_id, source, note, page, version, details)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, userID, source, note, page, version, details).Scan(&id)
	return id, err
}

const reportColumns = `r.id, r.user_id, u.email, u.name, r.created_at, r.source, r.note, r.page, r.version, length(r.details), r.resolved_at`

// ProblemReports lists reports, newest first, without their details:
// the open ones only unless all is set.
func ProblemReports(ctx context.Context, pool *pgxpool.Pool, all bool, before int64, limit int) ([]ProblemReport, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+reportColumns+` FROM problem_reports r JOIN users u ON u.id = r.user_id
		WHERE ($1 OR r.resolved_at IS NULL) AND ($2 = 0 OR r.id < $2)
		ORDER BY r.id DESC LIMIT $3`, all, before, normalizeLimit(limit, 50, 200))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProblemReport{}
	for rows.Next() {
		var p ProblemReport
		if err := rows.Scan(&p.ID, &p.UserID, &p.UserEmail, &p.UserName, &p.CreatedAt, &p.Source, &p.Note, &p.Page, &p.Version, &p.DetailsSize, &p.ResolvedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProblemReport is one report with its details.
func GetProblemReport(ctx context.Context, pool *pgxpool.Pool, id int64) (*ProblemReport, error) {
	var p ProblemReport
	err := pool.QueryRow(ctx, `
		SELECT `+reportColumns+`, r.details FROM problem_reports r JOIN users u ON u.id = r.user_id WHERE r.id = $1`, id).
		Scan(&p.ID, &p.UserID, &p.UserEmail, &p.UserName, &p.CreatedAt, &p.Source, &p.Note, &p.Page, &p.Version, &p.DetailsSize, &p.ResolvedAt, &p.Details)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ResolveProblemReport marks a report dealt with, or open again. False when there is no such report.
func ResolveProblemReport(ctx context.Context, pool *pgxpool.Pool, id int64, resolved bool) (bool, error) {
	tag, err := pool.Exec(ctx, `
		UPDATE problem_reports SET resolved_at = CASE WHEN $2 THEN coalesce(resolved_at, now()) END WHERE id = $1`, id, resolved)
	return tag.RowsAffected() > 0, err
}
