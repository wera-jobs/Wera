package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"wera/internal/store"
	"wera/internal/testutil"
)

func TestNotificationsEndpoints(t *testing.T) {
	ts, pool := testServer(t, nil)
	ctx := context.Background()
	other := testutil.User(t, pool, false)
	var mine, theirs int64
	add := func(user int64, key string) int64 {
		t.Helper()
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO notifications (user_id, kind, key, title) VALUES ($1, 'reminder', $2, 'Interview') RETURNING id`, user, key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	mine, theirs = add(testUserID, testutil.Unique("k")), add(other.ID, testutil.Unique("k"))
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM notifications WHERE id = ANY($1)`, []int64{mine, theirs})
	})

	get := func() (out struct {
		Notifications []store.Notification
		Unread        int
	}) {
		t.Helper()
		resp, err := client.Get(ts.URL + "/api/notifications")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	got := get()
	for _, n := range got.Notifications {
		if n.ID == theirs {
			t.Error("listed someone else's notice")
		}
	}
	if got.Unread < 1 {
		t.Errorf("unread: %d", got.Unread)
	}
	// Marking someone else's id does nothing to it; marking all reads mine.
	post := func(body string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/notifications/read", strings.NewReader(body))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("read %s: %d", body, resp.StatusCode)
		}
	}
	post(`{"ids":[` + itoa64(theirs) + `]}`)
	var read *string
	_ = pool.QueryRow(ctx, `SELECT read_at::text FROM notifications WHERE id = $1`, theirs).Scan(&read)
	if read != nil {
		t.Error("marked someone else's notice read")
	}
	post(`{}`)
	if got = get(); got.Unread != 0 {
		t.Errorf("unread after reading all: %d", got.Unread)
	}
}

func itoa64(n int64) string { b, _ := json.Marshal(n); return string(b) }
