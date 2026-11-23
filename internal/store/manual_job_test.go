package store_test

import (
	"context"
	"testing"

	"wera/internal/normalize"
	"wera/internal/store"
	"wera/internal/testutil"
)

// An added posting is cleaned and hashed like a fetched one: a NUL from a
// captured page saves instead of failing, and the hash matches what a
// fetch of the same posting computes, so its facts and scores carry over.
func TestAddManualJobCleansLikeAFetch(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	user := testutil.User(t, pool, false)
	company := testutil.Unique("Clean Co")
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM companies WHERE name = $1`, company) })

	id, created, err := store.AddManualJob(ctx, pool, user.ID, store.ManualJob{
		URL: "https://careers.example.com/" + testutil.Unique("job"), Company: company,
		Title: "Data\x00 Engineer", Location: "Austin, TX\xff", Description: "Build\x00 pipelines.",
	})
	if err != nil || !created {
		t.Fatalf("add: %v (created %v)", err, created)
	}
	var title, location, desc, hash string
	if err := pool.QueryRow(ctx, `SELECT title, location_raw, description, content_hash FROM jobs WHERE id = $1`, id).
		Scan(&title, &location, &desc, &hash); err != nil {
		t.Fatal(err)
	}
	if title != "Data Engineer" || location != "Austin, TX" || desc != "Build pipelines." {
		t.Errorf("stored %q / %q / %q", title, location, desc)
	}
	if want := normalize.ContentHash("Data Engineer", "Austin, TX", "Build pipelines."); hash != want {
		t.Errorf("content_hash %s, want the fetch's %s", hash, want)
	}
}
