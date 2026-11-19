package store_test

import (
	"context"
	"testing"

	"wera/internal/store"
	"wera/internal/testutil"
)

func TestResumeUsed(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	me := testutil.User(t, pool, false)
	companyID, _ := testutil.Company(t, pool, "other")
	ids := testutil.Jobs(t, pool, companyID, testutil.Job{Title: "Resume Role One"}, testutil.Job{Title: "Resume Role Two"}, testutil.Job{Title: "Resume Role Three"})
	for _, id := range ids {
		if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage) VALUES ($1, $2, 'h', 'scored')`, me.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	get := func(id int64) *store.ResumeUsed {
		t.Helper()
		j, err := store.GetJob(ctx, pool, me.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		return j.ResumeUsed
	}
	if r := get(ids[0]); r != nil {
		t.Errorf("before any run: %+v", r)
	}
	// The extension attaches the tailored copy on page one, the letter on page two.
	run, err := store.StartAutofillRun(ctx, pool, me.ID, &ids[0], "boards.example.com", 5, 0, store.RunFiles{ResumeKind: "tailored", ResumeName: "Ada Resume.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddAutofillPage(ctx, pool, me.ID, run.ID, 2, 0, store.RunFiles{Letter: true}); err != nil {
		t.Fatal(err)
	}
	if r := get(ids[0]); r == nil || r.Kind != "tailored" || r.Name != "Ada Resume.pdf" || !r.Letter || r.By != "extension" {
		t.Errorf("from the extension: %+v", r)
	}
	// The user corrects it; their word wins.
	base := "base"
	no := false
	if err := store.SetApplicationDetails(ctx, pool, me.ID, ids[0], store.ApplicationDetails{ResumeKind: &base, LetterSent: &no}); err != nil {
		t.Fatal(err)
	}
	if r := get(ids[0]); r == nil || r.Kind != "base" || r.Letter || r.By != "you" {
		t.Errorf("set by the user: %+v", r)
	}
	// By resume in analytics: one applied with base, one with an upload, one unknown.
	for i, id := range ids {
		if err := store.PutApplication(ctx, pool, me.ID, id, store.ApplicationInput{Status: "applied"}); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			if _, err := store.StartAutofillRun(ctx, pool, me.ID, &id, "x.example.com", 1, 0, store.RunFiles{ResumeKind: "upload"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	a, err := store.ApplicationAnalytics(ctx, pool, me.ID, 12)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, r := range a.Resumes {
		got[r.Source] = r.Applied
	}
	if got["base"] != 1 || got["upload"] != 1 || got["unknown"] != 1 {
		t.Errorf("by resume: %+v", a.Resumes)
	}
}
