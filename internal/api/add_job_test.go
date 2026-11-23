package api

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wera/internal/testutil"
)

// Adding jobs is limited per user across the dashboard and the extension,
// and the work after an addition (scoring, sharing) runs at most
// cap(addSlots) at a time.
func TestAddJobIsLimitedAndBounded(t *testing.T) {
	var running, most atomic.Int64
	var started sync.WaitGroup
	release := make(chan struct{})
	ts, pool := testServerWith(t, nil, func(s *Server) {
		s.ScoreNow = func(ctx context.Context, userID, jobID int64) error {
			n := running.Add(1)
			for {
				m := most.Load()
				if n <= m || most.CompareAndSwap(m, n) {
					break
				}
			}
			started.Done()
			select {
			case <-release:
			case <-ctx.Done():
			}
			running.Add(-1)
			return nil
		}
	})
	ext, email := connectExtension(t, ts.URL)
	company := "AddLimit Co " + testutil.Unique("c")
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM companies WHERE name = $1`, company)
		pool.Exec(context.Background(), `DELETE FROM users WHERE email = $1`, email)
	})

	// The first 4 additions start scoring; the rest wait for a slot.
	started.Add(4)
	for i := 1; i <= addJobsPerHour; i++ {
		path := "/api/ext/jobs"
		if i%2 == 0 {
			path = "/api/jobs" // the same handler and the same allowance
		}
		body := fmt.Sprintf(`{"url":"https://careers.example.com/%s","company":%q,"title":"Engineer %d"}`, testutil.Unique("job"), company, i)
		if code, resp := do(t, ext, http.MethodPost, ts.URL+path, body); code != 201 {
			t.Fatalf("add %d: want 201, got %d %s", i, code, resp)
		}
	}
	started.Wait()
	time.Sleep(100 * time.Millisecond) // give any extra goroutine time to start
	if n := most.Load(); n != 4 {
		t.Errorf("at most 4 added jobs should be processed at once, saw %d", n)
	}
	started.Add(addJobsPerHour - 4)
	close(release)
	started.Wait()

	body := fmt.Sprintf(`{"url":"https://careers.example.com/%s","company":%q,"title":"One too many"}`, testutil.Unique("job"), company)
	if code, _ := do(t, ext, http.MethodPost, ts.URL+"/api/ext/jobs", body); code != 429 {
		t.Errorf("addition %d in an hour: want 429, got %d", addJobsPerHour+1, code)
	}
}
