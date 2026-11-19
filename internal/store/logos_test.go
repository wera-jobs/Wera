package store_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"wera/internal/store"
	"wera/internal/testutil"
)

func TestCompanyLogos(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	user := testutil.User(t, pool, false)
	withLogo, _ := testutil.Company(t, pool, "other")
	without, _ := testutil.Company(t, pool, "other")
	failing, _ := testutil.Company(t, pool, "other")
	fresh, _ := testutil.Company(t, pool, "other")
	ids := []int64{withLogo, without, failing, fresh}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM company_logos WHERE company_id = ANY($1)`, ids)
	})
	due := func() map[int64]bool {
		t.Helper()
		out := map[int64]bool{}
		backlog, err := store.LogoBacklog(ctx, pool, 100000)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range backlog {
			out[b.CompanyID] = true
		}
		return out
	}
	for _, id := range ids {
		if !due()[id] {
			t.Fatalf("company %d never looked at is not due", id)
		}
	}
	if err := store.SaveLogo(ctx, pool, withLogo, "image/png", []byte("png"), "https://x/logo.png"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveLogo(ctx, pool, without, "", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveLogoFailed(ctx, pool, failing); err != nil {
		t.Fatal(err)
	}
	d := due()
	if d[withLogo] || d[without] || d[failing] || !d[fresh] {
		t.Errorf("due after looking: %v", d)
	}
	// A month on, all are due again; a failed look a day on.
	if _, err := pool.Exec(ctx, `UPDATE company_logos SET checked_at = checked_at - interval '31 days' WHERE company_id = ANY($1)`, ids[:2]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE company_logos SET checked_at = checked_at - interval '25 hours' WHERE company_id = $1`, failing); err != nil {
		t.Fatal(err)
	}
	if d = due(); !d[withLogo] || !d[without] || !d[failing] {
		t.Errorf("due again: %v", d)
	}
	// A failed look keeps a logo already found.
	if err := store.SaveLogoFailed(ctx, pool, withLogo); err != nil {
		t.Fatal(err)
	}
	data, ct, _, ok, err := store.CompanyLogo(ctx, pool, withLogo)
	if err != nil || !ok || string(data) != "png" || ct != "image/png" {
		t.Errorf("logo: %q %q %v %v", data, ct, ok, err)
	}
	if _, _, _, ok, err := store.CompanyLogo(ctx, pool, without); ok || err != nil {
		t.Errorf("no logo: %v %v", ok, err)
	}

	// Jobs carry the logo's URL, versioned; a company without one carries none.
	jobIDs := testutil.Jobs(t, pool, withLogo, testutil.Job{Title: "Logo Role"})
	jobIDs = append(jobIDs, testutil.Jobs(t, pool, without, testutil.Job{Title: "Logo Role Bare"})...)
	for _, id := range jobIDs {
		if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage) VALUES ($1, $2, 'h', 'pending_score')`, user.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := store.ListJobs(ctx, pool, user.ID, store.JobQuery{Q: "logo role"})
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		want := j.CompanyID == withLogo
		if got := strings.HasPrefix(j.LogoURL, "/api/companies/"+strconv.FormatInt(withLogo, 10)+"/logo?v="); got != want {
			t.Errorf("%s: logo_url %q", j.Title, j.LogoURL)
		}
	}
}
