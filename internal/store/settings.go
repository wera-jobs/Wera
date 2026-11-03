package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Settings are a user's preferences outside matching. StrongMatches and
// ProductUpdates are stored, but nothing sends those emails yet.
type Settings struct {
	Theme         string        `json:"theme"`        // system | light | dark
	DefaultSort   string        `json:"default_sort"` // score | newest
	Notifications Notifications `json:"notifications"`
}

// Notifications are a user's alert preferences.
type Notifications struct {
	EmailDigest    bool   `json:"email_digest"`
	Frequency      string `json:"frequency"` // daily | weekly
	StrongMatches  bool   `json:"strong_matches"`
	MinScore       int    `json:"min_score"` // alert threshold for strong matches
	ProductUpdates bool   `json:"product_updates"`
}

// DefaultSettings applies to users who never changed anything.
func DefaultSettings() Settings {
	return Settings{
		Theme: "system", DefaultSort: "score",
		Notifications: Notifications{EmailDigest: true, Frequency: "daily", StrongMatches: true, MinScore: 80},
	}
}

// Validate checks enumerations and ranges.
func (s *Settings) Validate() error {
	switch {
	case s.Theme != "system" && s.Theme != "light" && s.Theme != "dark":
		return fmt.Errorf("theme must be system, light or dark")
	case s.DefaultSort != "score" && s.DefaultSort != "newest":
		return fmt.Errorf("default_sort must be score or newest")
	case s.Notifications.Frequency != "daily" && s.Notifications.Frequency != "weekly":
		return fmt.Errorf("notification frequency must be daily or weekly")
	case s.Notifications.MinScore < 50 || s.Notifications.MinScore > 100:
		return fmt.Errorf("alert threshold must be between 50 and 100")
	}
	return nil
}

// GetSettings returns a user's settings with defaults for missing keys.
func GetSettings(ctx context.Context, pool *pgxpool.Pool, userID int64) (Settings, error) {
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT settings FROM users WHERE id = $1`, userID).Scan(&raw); err != nil {
		return DefaultSettings(), err
	}
	return decodeSettings(raw), nil
}

// decodeSettings reads stored settings; keys present override the defaults.
func decodeSettings(raw []byte) Settings {
	s := DefaultSettings()
	_ = json.Unmarshal(raw, &s)
	return s
}

// SaveSettings stores a user's settings.
func SaveSettings(ctx context.Context, pool *pgxpool.Pool, userID int64, s Settings) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `UPDATE users SET settings = $2 WHERE id = $1`, userID, raw)
	return err
}

// HiddenCompany is one company a user hid.
type HiddenCompany struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Industry string `json:"industry"`
}

// HiddenCompanies lists the companies a user hid.
func HiddenCompanies(ctx context.Context, pool *pgxpool.Pool, userID int64) ([]HiddenCompany, error) {
	rows, err := pool.Query(ctx, `
		SELECT c.id, c.name, c.industry FROM user_hidden_companies h JOIN companies c ON c.id = h.company_id
		WHERE h.user_id = $1 ORDER BY c.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HiddenCompany{}
	for rows.Next() {
		var c HiddenCompany
		if err := rows.Scan(&c.ID, &c.Name, &c.Industry); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetCompanyHidden hides or unhides a company for a user.
func SetCompanyHidden(ctx context.Context, pool *pgxpool.Pool, userID, companyID int64, hidden bool) error {
	var err error
	if hidden {
		_, err = pool.Exec(ctx, `
			INSERT INTO user_hidden_companies (user_id, company_id)
			SELECT $1, id FROM companies WHERE id = $2 ON CONFLICT DO NOTHING`, userID, companyID)
	} else {
		_, err = pool.Exec(ctx, `DELETE FROM user_hidden_companies WHERE user_id = $1 AND company_id = $2`, userID, companyID)
	}
	return err
}

// DeleteUser removes an account and everything personal it left behind.
// Most tables cascade. Two keep rows for the sake of the books, so their
// personal parts go here: mail in the outbox, and what the AI wrote about
// this person, whose cost still counts toward the month's budgets. A score
// another user's identical profile also reads stays whole.
func DeleteUser(ctx context.Context, pool *pgxpool.Pool, userID int64) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM email_outbox WHERE user_id = $1`, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE analyses a
			SET reason = NULL, raw = '{}', skills_matched = NULL, skills_missing = NULL
			WHERE a.user_id = $1 AND a.kind IN ('score', 'deep')
			  AND NOT EXISTS (SELECT 1 FROM user_jobs uj WHERE uj.analysis_id = a.id AND uj.user_id <> $1)`, userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		return err
	})
}
