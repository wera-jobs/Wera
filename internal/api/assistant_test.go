package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"wera/internal/config"
	"wera/internal/testutil"
)

// events splits a server-sent event stream into (event, data) pairs.
func events(body string) [][2]string {
	var out [][2]string
	for _, block := range strings.Split(body, "\n\n") {
		var ev, data string
		for _, line := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				ev = v
			} else if v, ok := strings.CutPrefix(line, "data: "); ok {
				data = v
			}
		}
		if ev != "" {
			out = append(out, [2]string{ev, data})
		}
	}
	return out
}

func TestAskAssistant(t *testing.T) {
	ts, llm := llmServer(t)
	pool := testPool(t)
	testutil.Profile(t, pool, testUserID, "# Candidate Profile\n## Target roles\nSRE.", config.Preferences{
		RoleFamilies: []string{"sre"}, Levels: []string{"entry"}, MaxYearsRequired: 3})
	t.Cleanup(func() {
		pool.Exec(t.Context(), `DELETE FROM assistant_threads WHERE user_id = $1`, testUserID)
		pool.Exec(t.Context(), `DELETE FROM llm_usage WHERE user_id = $1 AND kind = 'assistant'`, testUserID)
	})

	code, body := do(t, client, http.MethodPost, ts.URL+"/api/assistant", fmt.Sprintf(`{"message":"Should I apply?","job_id":%d}`, testJobIDs[0]))
	if code != 200 {
		t.Fatalf("ask: %d %s", code, body)
	}
	evs := events(body)
	if len(evs) != 5 || evs[0][0] != "thread" || evs[1][0] != "delta" || evs[4][0] != "done" {
		t.Fatalf("events: %v", evs)
	}
	var done struct {
		MessageID int64    `json:"message_id"`
		Content   string   `json:"content"`
		Sources   []string `json:"sources"`
	}
	json.Unmarshal([]byte(evs[4][1]), &done)
	if strings.Contains(done.Content, "—") || !strings.HasPrefix(done.Content, "Yes, apply") {
		t.Errorf("answer: %q", done.Content)
	}
	if strings.Join(done.Sources, ",") != "resume,job" {
		t.Errorf("sources: %v", done.Sources)
	}
	prompt, _ := llm.lastUser.Load().(string)
	for _, want := range []string{"<posting>", "<question>\nShould I apply?", "This job on the tracker: not on the tracker"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	var thread struct {
		ThreadID int64 `json:"thread_id"`
	}
	json.Unmarshal([]byte(evs[0][1]), &thread)

	// A follow-up in the same thread keeps the job and sends the earlier turns.
	code, body = do(t, client, http.MethodPost, ts.URL+"/api/assistant", fmt.Sprintf(`{"message":"And the pay?","thread_id":%d}`, thread.ThreadID))
	if code != 200 || !strings.Contains(body, "event: done") {
		t.Fatalf("follow-up: %d %s", code, body)
	}
	code, body = do(t, client, http.MethodGet, fmt.Sprintf("%s/api/assistant/threads/%d", ts.URL, thread.ThreadID), "")
	var got struct {
		Thread struct {
			JobID *int64 `json:"job_id"`
		} `json:"thread"`
		Messages []struct{ Role string } `json:"messages"`
	}
	json.Unmarshal([]byte(body), &got)
	if code != 200 || len(got.Messages) != 4 || got.Thread.JobID == nil || *got.Thread.JobID != testJobIDs[0] {
		t.Errorf("thread: %d %s", code, body)
	}
	code, body = do(t, client, http.MethodGet, ts.URL+"/api/assistant/threads", "")
	if code != 200 || !strings.Contains(body, `"questions_left":28`) {
		t.Errorf("threads: %d %s", code, body)
	}

	// Bad input, someone else's thread, and the daily limit.
	if code, _ := do(t, client, http.MethodPost, ts.URL+"/api/assistant", `{"message":"  "}`); code != 400 {
		t.Errorf("empty question: %d", code)
	}
	if code, _ := do(t, client, http.MethodPost, ts.URL+"/api/assistant", `{"message":"hi","thread_id":99999999}`); code != 404 {
		t.Errorf("unknown thread: %d", code)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO llm_usage (user_id, kind, model) SELECT $1, 'assistant', 'test' FROM generate_series(1, 28)`, testUserID); err != nil {
		t.Fatal(err)
	}
	if code, body := do(t, client, http.MethodPost, ts.URL+"/api/assistant", `{"message":"One more?"}`); code != 429 {
		t.Errorf("over the daily limit: %d %s", code, body)
	}

	// Deleting: one thread, then all.
	if code, _ := do(t, client, http.MethodDelete, fmt.Sprintf("%s/api/assistant/threads/%d", ts.URL, thread.ThreadID), ""); code != 204 {
		t.Errorf("delete: %d", code)
	}
	if code, _ := do(t, client, http.MethodGet, fmt.Sprintf("%s/api/assistant/threads/%d", ts.URL, thread.ThreadID), ""); code != 404 {
		t.Errorf("deleted thread: %d", code)
	}
	if code, _ := do(t, client, http.MethodDelete, ts.URL+"/api/assistant/threads", ""); code != 204 {
		t.Errorf("delete all: %d", code)
	}
}
