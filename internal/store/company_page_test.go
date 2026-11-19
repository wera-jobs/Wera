package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"wera/internal/store"
	"wera/internal/testutil"
)

func TestCompanyPage(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	me, other := testutil.User(t, pool, false), testutil.User(t, pool, false)
	companyID, name := testutil.Company(t, pool, "other")
	ids := testutil.Jobs(t, pool, companyID, testutil.Job{Title: "Match Role"}, testutil.Job{Title: "Other Role"}, testutil.Job{Title: "Closed Role"})
	if _, err := pool.Exec(ctx, `UPDATE jobs SET closed_at = now() WHERE id = $1`, ids[2]); err != nil {
		t.Fatal(err)
	}
	var aid int64
	if err := pool.QueryRow(ctx, `INSERT INTO analyses (job_id, kind, model, profile_hash, fit_score, raw, user_id, sponsorship) VALUES ($1, 'score', 'm', $2, 80, '{}', $3, 'yes') RETURNING id`, ids[0], testutil.Unique("h"), me.ID).Scan(&aid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage, analysis_id) VALUES ($1, $2, 'h', 'scored', $3)`, me.ID, ids[0], aid); err != nil {
		t.Fatal(err)
	}

	p, err := store.GetCompanyPage(ctx, pool, me.ID, companyID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != name || p.OpenJobs != 2 || p.MatchCount != 1 || len(p.Matches) != 1 || p.Matches[0].ID != ids[0] || len(p.Roles) != 2 || p.Sponsorship["yes"] != 1 || p.Followed {
		t.Errorf("page: %+v", p)
	}
	matched := 0
	for _, r := range p.Roles {
		if r.Match {
			matched++
		}
	}
	if matched != 1 {
		t.Errorf("roles marked as matches: %d", matched)
	}

	// A company someone typed in is theirs alone.
	var manual int64
	if err := pool.QueryRow(ctx, `INSERT INTO companies (name, ats, token, industry, enabled) VALUES ($1, 'manual', $1, 'other', false) RETURNING id`, testutil.Unique("Manual")).Scan(&manual); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM companies WHERE id = $1`, manual) })
	mj := testutil.Jobs(t, pool, manual, testutil.Job{Title: "Manual Role"})
	if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage) VALUES ($1, $2, 'h', 'scored')`, me.ID, mj[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetCompanyPage(ctx, pool, me.ID, manual); err != nil {
		t.Errorf("own manual company: %v", err)
	}
	if _, err := store.GetCompanyPage(ctx, pool, other.ID, manual); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("someone else's manual company: %v", err)
	}
	if err := store.SetCompanyFollowed(ctx, pool, other.ID, manual, true); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("following a manual company: %v", err)
	}

	// Following: new roles there become a notice, once a day, kept current.
	if err := store.SetCompanyFollowed(ctx, pool, me.ID, companyID, true); err != nil {
		t.Fatal(err)
	}
	if p, _ = store.GetCompanyPage(ctx, pool, me.ID, companyID); !p.Followed {
		t.Error("not followed")
	}
	fresh := testutil.Jobs(t, pool, companyID, testutil.Job{Title: "Fresh Role"})
	if _, err := pool.Exec(ctx, `UPDATE jobs SET first_seen_at = now() WHERE id = $1`, fresh[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MakeNotifications(ctx, pool, time.Now()); err != nil {
		t.Fatal(err)
	}
	list, _, _ := store.ListNotifications(ctx, pool, me.ID, 0)
	found := false
	for _, n := range list {
		if n.Kind == "followed_company" && n.Title == "1 new role at "+name && n.JobID != nil && *n.JobID == fresh[0] {
			found = true
		}
	}
	if !found {
		t.Errorf("no followed-company notice: %+v", list)
	}
	if err := store.SetCompanyFollowed(ctx, pool, me.ID, companyID, false); err != nil {
		t.Fatal(err)
	}
	if p, _ = store.GetCompanyPage(ctx, pool, me.ID, companyID); p.Followed {
		t.Error("still followed")
	}
}
