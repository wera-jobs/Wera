package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/config"
)

// Profile is one user's scoring profile and filter preferences, plus what
// they gave at signup (resume text and questionnaire answers as JSON).
type Profile struct {
	UserID      int64              `json:"-"`
	Markdown    string             `json:"markdown"`
	ProfileHash string             `json:"-"`
	Preferences config.Preferences `json:"preferences"`
	UpdatedAt   time.Time          `json:"updated_at"`
	ResumeText  string             `json:"-"`
	Answers     json.RawMessage    `json:"answers"`
}

// Ready reports whether the pipeline can match jobs for this profile:
// it needs profile text and at least one role family and level.
func (p *Profile) Ready() bool {
	return p.Markdown != "" && len(p.Preferences.RoleFamilies) > 0 && len(p.Preferences.Levels) > 0
}

const profileColumns = `user_id, markdown, profile_hash, preferences, updated_at, resume_text, answers`

func scanProfile(row pgx.Row) (*Profile, error) {
	var p Profile
	var prefs, answers []byte
	if err := row.Scan(&p.UserID, &p.Markdown, &p.ProfileHash, &prefs, &p.UpdatedAt,
		&p.ResumeText, &answers); err != nil {
		return nil, err
	}
	p.Answers = answers
	if err := json.Unmarshal(prefs, &p.Preferences); err != nil {
		return nil, fmt.Errorf("profile %d preferences: %w", p.UserID, err)
	}
	// A profile started by a resume upload has preferences '{}'; keep the
	// lists non-nil so the API sends [] rather than null.
	if p.Preferences.RoleFamilies == nil {
		p.Preferences.RoleFamilies = []string{}
	}
	if p.Preferences.Levels == nil {
		p.Preferences.Levels = []string{}
	}
	return &p, nil
}

// GetProfile returns a user's profile, or nil when they have none yet.
func GetProfile(ctx context.Context, pool *pgxpool.Pool, userID int64) (*Profile, error) {
	p, err := scanProfile(pool.QueryRow(ctx, `SELECT `+profileColumns+` FROM profiles WHERE user_id = $1`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// ReadyProfiles returns every profile the pipeline can match jobs for.
func ReadyProfiles(ctx context.Context, pool *pgxpool.Pool) ([]*Profile, error) {
	rows, err := pool.Query(ctx, `SELECT `+profileColumns+` FROM profiles WHERE markdown <> '' ORDER BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Profile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		if p.Ready() {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

// SaveProfile upserts a user's profile. Changed preferences need nothing
// here: the next filter pass sees they differ from the ones the jobs were
// filtered with (see FilterForUser). When the text changed, scored jobs are
// requeued so they are scored against the new text.
func SaveProfile(ctx context.Context, pool *pgxpool.Pool, userID int64, markdown, profileHash string, prefs config.Preferences, answers json.RawMessage) error {
	if len(answers) == 0 {
		answers = json.RawMessage(`{}`)
	}
	prefsJSON, err := json.Marshal(prefs)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var oldHash string
	err = tx.QueryRow(ctx, `SELECT profile_hash FROM profiles WHERE user_id = $1 FOR UPDATE`, userID).Scan(&oldHash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO profiles (user_id, markdown, profile_hash, preferences, answers, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (user_id) DO UPDATE
		SET markdown = EXCLUDED.markdown, profile_hash = EXCLUDED.profile_hash,
		    preferences = EXCLUDED.preferences, answers = EXCLUDED.answers, updated_at = now()`,
		userID, markdown, profileHash, prefsJSON, []byte(answers)); err != nil {
		return fmt.Errorf("save profile: %w", err)
	}

	if oldHash != "" && oldHash != profileHash {
		if _, err := tx.Exec(ctx, `
			UPDATE user_jobs SET stage = 'pending_score', updated_at = now()
			WHERE user_id = $1 AND stage IN ('scored','score_failed')`, userID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// SaveResumeText stores the plain text of a user's resume, creating an
// empty profile row when the user has none yet.
func SaveResumeText(ctx context.Context, pool *pgxpool.Pool, userID int64, text string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO profiles (user_id, resume_text) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET resume_text = EXCLUDED.resume_text`, userID, text)
	if err != nil {
		return fmt.Errorf("save resume text: %w", err)
	}
	return nil
}
