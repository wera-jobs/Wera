package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/pay"
)

// payArgs are the pay columns for a posting's text: nils when it states none.
func payArgs(description string) (minPay, maxPay *float64, currency, period *string) {
	p, ok := pay.FromText(description)
	if !ok {
		return nil, nil, nil, nil
	}
	return &p.Min, &p.Max, &p.Currency, &p.Period
}

// payValues is payArgs as query arguments.
func payValues(description string) []any {
	lo, hi, cur, per := payArgs(description)
	return []any{lo, hi, cur, per}
}

// FillPay reads the pay of up to limit open jobs that were saved before
// pay was read (see migration 0038), and returns how many it read.
// Called in the background at startup and on every pipeline run until
// none is left.
func FillPay(ctx context.Context, pool *pgxpool.Pool, limit int) (int, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, COALESCE(description, '') FROM jobs
		WHERE NOT pay_checked AND closed_at IS NULL ORDER BY id LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type job struct {
		id   int64
		text string
	}
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.text); err != nil {
			rows.Close()
			return 0, err
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	b := &pgx.Batch{}
	for _, j := range jobs {
		lo, hi, cur, per := payArgs(j.text)
		b.Queue(`UPDATE jobs SET pay_min = $2, pay_max = $3, pay_currency = $4, pay_period = $5, pay_checked = true WHERE id = $1`,
			j.id, lo, hi, cur, per)
	}
	if err := pool.SendBatch(ctx, b).Close(); err != nil {
		return 0, fmt.Errorf("fill pay: %w", err)
	}
	return len(jobs), nil
}
