package store_test

import (
	"context"
	"testing"

	"wera/internal/store"
	"wera/internal/testutil"
)

func TestDeleteUserLeavesNothingPersonal(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	u := testutil.User(t, pool, false)
	other := testutil.User(t, pool, false)
	companyID, _ := testutil.Company(t, pool, "ai_ml")
	jobs := testutil.Jobs(t, pool, companyID, testutil.Job{Title: "Platform Engineer"}, testutil.Job{Title: "Backend Engineer"})

	if _, err := pool.Exec(ctx, `INSERT INTO email_outbox (user_id, to_addr, tag, subject, html, text) VALUES ($1, $2, 'digest', 's', 'h', 't')`, u.ID, u.Email); err != nil {
		t.Fatal(err)
	}
	var mine, shared int64
	add := `INSERT INTO analyses (job_id, kind, model, profile_hash, fit_score, reason, raw, skills_matched, cost_usd, user_id)
		VALUES ($1, 'score', 'm', $2, 80, 'fits their ops work', '{"note":"x"}', '{Go}', 0.5, $3) RETURNING id`
	if err := pool.QueryRow(ctx, add, jobs[0], testutil.Unique("hash"), u.ID).Scan(&mine); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, add, jobs[1], testutil.Unique("hash"), u.ID).Scan(&shared); err != nil {
		t.Fatal(err)
	}
	// Another user with the same profile text reads the second score.
	if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, stage, analysis_id, content_hash) VALUES ($1, $2, 'scored', $3, 'h')`, other.ID, jobs[1], shared); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM analyses WHERE id = ANY($1)`, []int64{mine, shared}) })

	if err := store.DeleteUser(ctx, pool, u.ID); err != nil {
		t.Fatal(err)
	}

	var mail int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox WHERE to_addr = $1`, u.Email).Scan(&mail); err != nil || mail != 0 {
		t.Errorf("mail left behind: %d, %v", mail, err)
	}
	var reason *string
	var skills []string
	var raw string
	var cost float64
	if err := pool.QueryRow(ctx, `SELECT reason, raw::text, skills_matched, cost_usd FROM analyses WHERE id = $1`, mine).Scan(&reason, &raw, &skills, &cost); err != nil {
		t.Fatal(err)
	}
	if reason != nil || raw != "{}" || skills != nil {
		t.Errorf("AI notes left behind: reason %v, raw %s, skills %v", reason, raw, skills)
	}
	if cost != 0.5 {
		t.Errorf("cost should stay on the books, got %v", cost)
	}
	if err := pool.QueryRow(ctx, `SELECT reason FROM analyses WHERE id = $1`, shared).Scan(&reason); err != nil || reason == nil {
		t.Errorf("a score another user reads was blanked: %v, %v", reason, err)
	}
}
