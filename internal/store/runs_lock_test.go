package store_test

import (
	"context"
	"testing"

	"wera/internal/store"
	"wera/internal/testutil"
)

// A release on a canceled context, as on shutdown, must still free the
// lock: a pooled connection would otherwise keep it.
func TestLockReleaseWithCanceledContext(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	user := testutil.User(t, pool, false)
	canceled, cancel := context.WithCancel(ctx)
	cancel()

	got, release, err := store.TryUserLock(ctx, pool, user.ID)
	if err != nil || !got {
		t.Fatalf("first lock: got %v, err %v", got, err)
	}
	release(canceled)
	got, release, err = store.TryUserLock(ctx, pool, user.ID)
	if err != nil || !got {
		t.Fatalf("lock after canceled release: got %v, err %v", got, err)
	}
	release(ctx)

	const key = 987654321
	got, release, err = store.TryAdvisoryLock(ctx, pool, key)
	if err != nil || !got {
		t.Fatalf("advisory lock: got %v, err %v", got, err)
	}
	release(canceled)
	// No session, pooled or not, may still hold the key.
	var held bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND objid = $1)`, key).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held {
		t.Fatal("advisory lock still held after a canceled release")
	}
}
