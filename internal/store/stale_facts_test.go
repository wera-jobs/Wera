package store_test

import (
	"context"
	"slices"
	"testing"

	"wera/internal/store"
	"wera/internal/testutil"
)

// Facts extracted from a posting's earlier text are not read for resume
// keywords or the fit breakdown once the text changed.
func TestResumeInputsIgnoreStaleFacts(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	user := testutil.User(t, pool, false)
	companyID, _ := testutil.Company(t, pool, "ai_ml")
	job := testutil.Jobs(t, pool, companyID, testutil.Job{Title: "Data Engineer", Location: "Remote, US", Description: "Build pipelines."})[0]
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO user_jobs (user_id, job_id, content_hash, stage) SELECT $1, id, content_hash, 'pending_score' FROM jobs WHERE id = $2`, user.ID, job)
	exec(`INSERT INTO job_facts (job_id, content_hash, model, facts) SELECT id, content_hash, 'test', '{"skills_required":["Cobol"]}' FROM jobs WHERE id = $1`, job)

	read := func() (keywords, required []string) {
		t.Helper()
		keywords, err := store.JobKeywords(ctx, pool, user.ID, job)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, required, err = store.JobFitInputs(ctx, pool, user.ID, job); err != nil {
			t.Fatal(err)
		}
		return keywords, required
	}
	if kw, req := read(); !slices.Contains(kw, "Cobol") || !slices.Equal(req, []string{"Cobol"}) {
		t.Fatalf("current facts: keywords %v, required %v", kw, req)
	}
	// The posting's text changed; its facts are not extracted again yet.
	exec(`UPDATE jobs SET content_hash = content_hash || '-edited' WHERE id = $1`, job)
	if kw, req := read(); slices.Contains(kw, "Cobol") || len(req) != 0 {
		t.Errorf("stale facts read: keywords %v, required %v", kw, req)
	}
}
