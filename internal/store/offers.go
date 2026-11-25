package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Offer is an offer the user got for one job. Amounts are yearly, in
// Currency; BonusPct is a percent of the base salary.
type Offer struct {
	JobID        int64      `json:"job_id"`
	BaseSalary   *float64   `json:"base_salary"`
	BonusPct     *float64   `json:"bonus_pct"`
	AnnualEquity *float64   `json:"annual_equity"`
	SignOn       *float64   `json:"sign_on"`
	Currency     string     `json:"currency"`
	WorkMode     string     `json:"work_mode"` // remote | hybrid | onsite, or "" when not said
	Location     string     `json:"location"`
	DeadlineAt   *time.Time `json:"deadline_at"`
	Notes        string     `json:"notes"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	// Set by Offers: the job it is for.
	JobTitle  string  `json:"job_title,omitempty"`
	Company   string  `json:"company,omitempty"`
	CompanyID int64   `json:"company_id,omitempty"`
	AppStatus *string `json:"application_status,omitempty"`
}

// OfferInput is what PutOffer stores (all of it: a put replaces the offer).
type OfferInput struct {
	BaseSalary, BonusPct, AnnualEquity, SignOn *float64
	Currency, WorkMode, Location, Notes        string
	DeadlineAt                                 *time.Time
}

const offerColumns = `o.job_id, o.base_salary::float8, o.bonus_pct::float8, o.annual_equity::float8, o.sign_on::float8,
	o.currency, o.work_mode, o.location, o.deadline_at, o.notes, o.created_at, o.updated_at`

func offerScan(o *Offer, extra ...any) []any {
	return append([]any{&o.JobID, &o.BaseSalary, &o.BonusPct, &o.AnnualEquity, &o.SignOn, &o.Currency, &o.WorkMode,
		&o.Location, &o.DeadlineAt, &o.Notes, &o.CreatedAt, &o.UpdatedAt}, extra...)
}

// GetOffer returns the user's offer for a job (pgx.ErrNoRows when none).
func GetOffer(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64) (*Offer, error) {
	var o Offer
	if err := pool.QueryRow(ctx, `SELECT `+offerColumns+` FROM user_job_offers o WHERE o.user_id = $1 AND o.job_id = $2`,
		userID, jobID).Scan(offerScan(&o)...); err != nil {
		return nil, err
	}
	return &o, nil
}

// PutOffer creates or replaces the user's offer for one of their jobs. It
// returns pgx.ErrNoRows when the job isn't one of the user's.
func PutOffer(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64, in OfferInput) (*Offer, error) {
	var o Offer
	err := pool.QueryRow(ctx, `
		INSERT INTO user_job_offers AS o (user_id, job_id, base_salary, bonus_pct, annual_equity, sign_on, currency, work_mode, location, deadline_at, notes)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
		WHERE EXISTS (SELECT 1 FROM user_jobs WHERE user_id = $1 AND job_id = $2)
		ON CONFLICT (user_id, job_id) DO UPDATE SET
		  base_salary = EXCLUDED.base_salary, bonus_pct = EXCLUDED.bonus_pct, annual_equity = EXCLUDED.annual_equity,
		  sign_on = EXCLUDED.sign_on, currency = EXCLUDED.currency, work_mode = EXCLUDED.work_mode,
		  location = EXCLUDED.location, deadline_at = EXCLUDED.deadline_at, notes = EXCLUDED.notes, updated_at = now()
		RETURNING `+offerColumns,
		userID, jobID, in.BaseSalary, in.BonusPct, in.AnnualEquity, in.SignOn, in.Currency, in.WorkMode, in.Location,
		in.DeadlineAt, in.Notes).Scan(offerScan(&o)...)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// DeleteOffer removes the user's offer for a job and reports whether there was one.
func DeleteOffer(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64) (bool, error) {
	tag, err := pool.Exec(ctx, `DELETE FROM user_job_offers WHERE user_id = $1 AND job_id = $2`, userID, jobID)
	return tag.RowsAffected() > 0, err
}

// Offers lists all of the user's offers with their jobs, soonest deadline first.
func Offers(ctx context.Context, pool *pgxpool.Pool, userID int64) ([]Offer, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+offerColumns+`, j.title, c.name, c.id, ap.status
		FROM user_job_offers o JOIN jobs j ON j.id = o.job_id JOIN companies c ON c.id = j.company_id
		LEFT JOIN applications ap ON ap.user_id = o.user_id AND ap.job_id = o.job_id
		WHERE o.user_id = $1
		ORDER BY o.deadline_at ASC NULLS LAST, o.updated_at DESC
		LIMIT 200`, userID)
	if err != nil {
		return nil, fmt.Errorf("offers: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Offer, error) {
		var o Offer
		err := row.Scan(offerScan(&o, &o.JobTitle, &o.Company, &o.CompanyID, &o.AppStatus)...)
		return o, err
	})
}
