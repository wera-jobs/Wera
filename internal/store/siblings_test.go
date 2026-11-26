package store_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"wera/internal/store"
	"wera/internal/testutil"
)

// One role posted for three cities shows once in Today, the jobs list and
// the digest, as its best scored posting, with the other cities listed. A
// posting the user tracks still shows on its own.
func TestSiblingPostingsShowOnce(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	user := testutil.User(t, pool, false)
	companyID, _ := testutil.Company(t, pool, "other")
	const text = "Run the platform team's Kubernetes clusters."
	ids := testutil.Jobs(t, pool, companyID,
		testutil.Job{Title: "Platform Engineer", Location: "Austin, TX", Description: text},
		testutil.Job{Title: "Platform Engineer", Location: "Seattle, WA", Description: text},
		testutil.Job{Title: "Platform Engineer", Location: "Chicago, IL", Description: text},
		testutil.Job{Title: "Platform Engineer", Location: "Denver, CO", Description: "Another team's role."})
	for i, fit := range []int{80, 86, 80, 70} {
		if _, err := pool.Exec(ctx, `
			WITH a AS (INSERT INTO analyses (job_id, kind, model, profile_hash, fit_score, raw, user_id)
			           VALUES ($2, 'score', 'test', 'k', $3, '{}', $1) RETURNING id)
			INSERT INTO user_jobs (user_id, job_id, content_hash, stage, analysis_id)
			SELECT $1, $2, 'h', 'scored', id FROM a`, user.ID, ids[i], fit); err != nil {
			t.Fatal(err)
		}
	}
	shown := func(views []store.JobView) map[int64][]string {
		out := map[int64][]string{}
		for _, v := range views {
			if v.CompanyID == companyID {
				slices.Sort(v.AlsoIn)
				out[v.ID] = v.AlsoIn
			}
		}
		return out
	}
	check := func(name string, got map[int64][]string) {
		t.Helper()
		if len(got) != 2 || got[ids[3]] != nil {
			t.Fatalf("%s: shown %v, want the Seattle posting and the other role", name, got)
		}
		if want := []string{"Austin, TX", "Chicago, IL"}; !slices.Equal(got[ids[1]], want) {
			t.Errorf("%s: Seattle also in %v, want %v", name, got[ids[1]], want)
		}
	}

	today, _, err := store.TodayJobs(ctx, pool, user.ID, 50, false)
	if err != nil {
		t.Fatal(err)
	}
	check("today", shown(today))
	list, err := store.ListJobs(ctx, pool, user.ID, store.JobQuery{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	check("list", shown(list))
	digest, err := store.NewMatches(ctx, pool, user.ID, time.Time{}, 200)
	if err != nil {
		t.Fatal(err)
	}
	check("digest", shown(digest))

	// Saving the Austin posting keeps it on its own; the best one stays too.
	if err := store.PutApplication(ctx, pool, user.ID, ids[0], store.ApplicationInput{Status: "saved"}); err != nil {
		t.Fatal(err)
	}
	today, _, err = store.TodayJobs(ctx, pool, user.ID, 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := shown(today); len(got) != 3 || !slices.Contains(keys(got), ids[0]) || !slices.Contains(keys(got), ids[1]) {
		t.Errorf("after saving Austin: shown %v, want Austin, Seattle and the other role", got)
	}
}

func keys(m map[int64][]string) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
