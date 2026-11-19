package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LogoRecheck is how long a logo, or a look that found none, stands
// before the board is read again.
const LogoRecheck = 30 * 24 * time.Hour

// LogoTarget is a company whose logo is due a look.
type LogoTarget struct {
	CompanyID int64
	ATS       string
	Token     string
}

// LogoBacklog lists enabled companies never looked at first, then those
// last looked at more than LogoRecheck ago, up to limit.
func LogoBacklog(ctx context.Context, pool *pgxpool.Pool, limit int) ([]LogoTarget, error) {
	rows, err := pool.Query(ctx, `
		SELECT c.id, c.ats, c.token FROM companies c
		LEFT JOIN company_logos l ON l.company_id = c.id
		WHERE c.enabled AND c.ats <> 'manual'
		  AND (l.company_id IS NULL OR l.checked_at < now() - $1::interval)
		ORDER BY l.checked_at NULLS FIRST, c.id
		LIMIT $2`, LogoRecheck, limit)
	if err != nil {
		return nil, fmt.Errorf("logo backlog: %w", err)
	}
	defer rows.Close()
	var out []LogoTarget
	for rows.Next() {
		var t LogoTarget
		if err := rows.Scan(&t.CompanyID, &t.ATS, &t.Token); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SaveLogo records a look at a company's board: the logo found (data and
// its type), or none (empty data). A look that failed for a passing reason
// is SaveLogoFailed.
func SaveLogo(ctx context.Context, pool *pgxpool.Pool, companyID int64, contentType string, data []byte, source string) error {
	found := len(data) > 0
	_, err := pool.Exec(ctx, `
		INSERT INTO company_logos (company_id, found, content_type, data, source_url, checked_at)
		VALUES ($1, $2, nullif($3, ''), $4, nullif($5, ''), now())
		ON CONFLICT (company_id) DO UPDATE SET found = EXCLUDED.found, content_type = EXCLUDED.content_type,
		  data = EXCLUDED.data, source_url = EXCLUDED.source_url, checked_at = now()`,
		companyID, found, contentType, noBytes(data), source)
	if err != nil {
		return fmt.Errorf("save logo for company %d: %w", companyID, err)
	}
	return nil
}

// LogoRetry is how soon a look that failed for a passing reason (a
// timeout, a 5xx) is tried again.
const LogoRetry = 24 * time.Hour

// SaveLogoFailed records a look that failed, so a pass moves on and the
// company is tried again after LogoRetry. It keeps a logo already found.
func SaveLogoFailed(ctx context.Context, pool *pgxpool.Pool, companyID int64) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO company_logos (company_id, found, checked_at)
		VALUES ($1, false, now() - $2::interval + $3::interval)
		ON CONFLICT (company_id) DO UPDATE SET checked_at = EXCLUDED.checked_at`,
		companyID, LogoRecheck, LogoRetry)
	if err != nil {
		return fmt.Errorf("save failed logo look for company %d: %w", companyID, err)
	}
	return nil
}

func noBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}

// CompanyLogo returns a company's logo and when it was found; ok is false
// when there is none.
func CompanyLogo(ctx context.Context, pool *pgxpool.Pool, companyID int64) (data []byte, contentType string, at time.Time, ok bool, err error) {
	err = pool.QueryRow(ctx, `
		SELECT data, content_type, checked_at FROM company_logos
		WHERE company_id = $1 AND found`, companyID).Scan(&data, &contentType, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", time.Time{}, false, nil
	}
	if err != nil {
		return nil, "", time.Time{}, false, fmt.Errorf("company logo %d: %w", companyID, err)
	}
	return data, contentType, at, true, nil
}

// LogoURL is where the API serves a company's logo; v changes when the
// logo does, so browsers may cache it for good.
func LogoURL(companyID int64, v time.Time) string {
	return fmt.Sprintf("/api/companies/%d/logo?v=%d", companyID, v.Unix())
}
