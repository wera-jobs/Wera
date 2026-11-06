package pipeline

import (
	"context"
	"testing"

	"wera/internal/store"
	"wera/internal/testutil"
)

// Only matches get a row, and a pass reads only postings added, changed or
// reopened since the last one: rejected postings are not read again.
func TestFilterKeepsOnlyMatchesAndReadsOnlyChanges(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	eng := testEngine(t)
	companyID, _ := testutil.Company(t, pool, "ai_ml")
	ids := testutil.Jobs(t, pool, companyID,
		testutil.Job{Title: "Site Reliability Engineer", Location: "Remote, US", Description: "Run Go services."},
		testutil.Job{Title: "Accountant", Location: "Remote, US", Description: "Close the books."},
		testutil.Job{Title: "Platform Engineer", Location: "Remote, US", Description: "Build the platform."},
		testutil.Job{Title: "Platform Engineer, Tools", Location: "Remote, US", Description: "Build tools."},
	)
	sre, accountant, platform, tracked := ids[0], ids[1], ids[2], ids[3]
	u := testutil.User(t, pool, false)
	p := testutil.Profile(t, pool, u.ID, sreProfile, srePrefs)
	pass := func() *store.FilterOutcome {
		t.Helper()
		out, err := store.FilterForUser(ctx, pool, eng, u.ID, &p.Preferences)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.MarkFiltered(ctx, pool, u.ID, &p.Preferences); err != nil {
			t.Fatal(err)
		}
		return out
	}
	pass()
	for id, want := range map[int64]string{sre: "pending_score", accountant: "", platform: "pending_score", tracked: "pending_score"} {
		if stage, _ := stageOf(t, pool, u.ID, id); stage != want {
			t.Errorf("job %d: %q, want %q", id, stage, want)
		}
	}

	// Dropping a role family drops its matches, except one the user tracks.
	if _, err := pool.Exec(ctx, `INSERT INTO applications (user_id, job_id, status, notes) VALUES ($1, $2, 'saved', '')`, u.ID, tracked); err != nil {
		t.Fatal(err)
	}
	p.Preferences.RoleFamilies = []string{"sre"}
	if out := pass(); !out.Refiltered {
		t.Fatalf("a preference change should filter every job: %+v", out)
	}
	if stage, _ := stageOf(t, pool, u.ID, platform); stage != "" {
		t.Errorf("a match the new preferences reject keeps a row: %s", stage)
	}
	if stage, _ := stageOf(t, pool, u.ID, tracked); stage != "excluded" {
		t.Errorf("a tracked job the preferences reject: %q, want excluded", stage)
	}

	// Nothing changed since: the next pass reads nothing. (Every posting in
	// the test database is aged past the pass's overlap first.)
	if _, err := pool.Exec(ctx, `UPDATE jobs SET changed_at = changed_at - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE profiles SET filtered_through = now() WHERE user_id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	if out := pass(); out.Excluded+out.Pending != 0 {
		t.Errorf("a pass with nothing changed read %d jobs", out.Excluded+out.Pending)
	}

	// A new posting, and one that closed and is listed again, are read.
	fresh := testutil.Jobs(t, pool, companyID, testutil.Job{Title: "Site Reliability Engineer II", Location: "Remote, US", Description: "On-call."})[0]
	var ext string
	pool.QueryRow(ctx, `UPDATE jobs SET closed_at = now() WHERE id = $1 RETURNING ext_id`, sre).Scan(&ext)
	if err := store.TouchJobs(ctx, pool, companyID, []string{ext}); err != nil {
		t.Fatal(err)
	}
	if out := pass(); out.Pending != 2 || out.Excluded != 0 {
		t.Errorf("want the new and the reopened posting read, got %+v", out)
	}
	if stage, _ := stageOf(t, pool, u.ID, fresh); stage != "pending_score" {
		t.Errorf("new posting: %q", stage)
	}
}
