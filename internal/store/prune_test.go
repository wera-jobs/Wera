package store_test

import (
	"context"
	"testing"
	"time"

	"wera/internal/store"
	"wera/internal/testutil"
)

func TestPrune(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	user := testutil.User(t, pool, false)
	companyID, _ := testutil.Company(t, pool, "other")
	ids := testutil.Jobs(t, pool, companyID,
		testutil.Job{Title: "Long Closed"}, testutil.Job{Title: "Long Closed, Tracked"},
		testutil.Job{Title: "Closed A Month"}, testutil.Job{Title: "Open"})
	longClosed, tracked, month, open := ids[0], ids[1], ids[2], ids[3]
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE jobs SET closed_at = now() - interval '100 days' WHERE id = ANY($1)`, []int64{longClosed, tracked})
	exec(`UPDATE jobs SET closed_at = now() - interval '40 days' WHERE id = $1`, month)
	exec(`INSERT INTO applications (user_id, job_id, status, notes) VALUES ($1, $2, 'applied', '')`, user.ID, tracked)
	model := testutil.Unique("prune-model")
	if err := store.SaveJobEmbeddings(ctx, pool, model, map[int64][]float32{month: {1}, open: {1}}); err != nil {
		t.Fatal(err)
	}
	// The open job was scored, then rescored after a profile edit 40 days
	// ago; the month-closed one was rescored only days ago.
	score := func(job int64, hash, age string) (id int64) {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			INSERT INTO analyses (job_id, kind, model, profile_hash, fit_score, raw, user_id, created_at)
			VALUES ($1, 'score', 'test', $2, 70, '{}', $3, now() - $4::interval) RETURNING id`,
			job, testutil.Unique(hash), user.ID, age).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	oldOpen, newOpen := score(open, "old", "60 days"), score(open, "new", "40 days")
	oldRecent := score(month, "old", "60 days")
	score(month, "new", "3 days")
	exec(`INSERT INTO user_jobs (user_id, job_id, content_hash, stage, analysis_id) VALUES ($1, $2, 'h', 'scored', $3)`, user.ID, open, newOpen)

	if _, err := store.Prune(ctx, pool, time.Minute); err != nil {
		t.Fatal(err)
	}
	exists := func(q string, id int64) bool {
		t.Helper()
		var ok bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (`+q+`)`, id).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	job := `SELECT 1 FROM jobs WHERE id = $1`
	if exists(job, longClosed) || !exists(job, tracked) || !exists(job, month) || !exists(job, open) {
		t.Error("want only the long-closed, untracked posting deleted")
	}
	vec := `SELECT 1 FROM job_embeddings WHERE job_id = $1`
	if exists(vec, month) || !exists(vec, open) {
		t.Error("want only the closed posting's embedding deleted")
	}
	an := `SELECT 1 FROM analyses WHERE id = $1`
	if exists(an, oldOpen) || !exists(an, newOpen) || !exists(an, oldRecent) {
		t.Error("want only the score superseded over 30 days ago deleted")
	}
}
