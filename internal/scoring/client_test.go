package scoring

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// jsonDecode reads the request body into out.
func jsonDecode(r *http.Request, out any) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

const fakeReply = `{"choices":[{"message":{"content":"{\"ratings\":{\"role\":8,\"seniority\":8,\"skills\":7,\"domain\":6,\"location\":7,\"blocker\":false},\"seniority\":\"junior\",\"years_required\":null,\"sponsorship\":\"unknown\",\"sponsorship_quote\":null,\"us_eligible\":true,\"work_mode\":\"remote\",\"location_summary\":\"Remote\",\"skills_matched\":[],\"skills_missing\":[],\"reason\":\"ok\"}"}}],"usage":{"prompt_tokens":1000,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":900}}}`

func okHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}
}

func TestChatSuccess(t *testing.T) {
	srv := httptest.NewServer(okHandler(fakeReply))
	t.Cleanup(srv.Close)

	c := NewClient(ClientOptions{BaseURL: srv.URL + "/", APIKey: "cb_test", Model: "glm-5.3-flash-fast"})
	comp, err := c.Chat(context.Background(), "wera-profile-abc", []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if comp.Usage.PromptTokens != 1000 || comp.Usage.CachedTokens != 900 || comp.Usage.CompletionTokens != 50 {
		t.Errorf("usage: %+v", comp.Usage)
	}
	if comp.Content == "" {
		t.Error("empty content")
	}
}

func TestChatAuthAndBody(t *testing.T) {
	var gotAuth, gotCache string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req request
		if err := jsonDecode(r, &req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotCache = req.PromptCacheKey
		w.Write([]byte(fakeReply))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(ClientOptions{BaseURL: srv.URL, APIKey: "cb_test", Model: "glm-5.3-flash-fast", CacheKeys: true})
	if _, err := c.Chat(context.Background(), "wera-profile-xyz", []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if gotAuth != "Bearer cb_test" {
		t.Errorf("auth header: %q", gotAuth)
	}
	if gotCache != "wera-profile-xyz" {
		t.Errorf("prompt_cache_key: %q", gotCache)
	}

	// Providers that do not route by cache key never see one.
	c = NewClient(ClientOptions{BaseURL: srv.URL, APIKey: "k", Model: "m"})
	if _, err := c.Chat(context.Background(), "wera-profile-xyz", []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if gotCache != "" {
		t.Errorf("prompt_cache_key sent without CacheKeys: %q", gotCache)
	}
}

func TestChatExtraBodyAndReportedCost(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		if err := jsonDecode(r, &got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}],"usage":{"prompt_tokens":10,"completion_tokens":2,"estimated_cost":0.0042}}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(ClientOptions{BaseURL: srv.URL, APIKey: "k", Model: "deepseek-ai/DeepSeek-V4.1-Flash",
		ExtraBody: map[string]any{"reasoning_effort": "none", "model": "ignored"}})
	comp, err := c.Chat(context.Background(), "", []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if got["reasoning_effort"] != "none" || got["model"] != "deepseek-ai/DeepSeek-V4.1-Flash" {
		t.Errorf("extra body not merged (own fields must win): %v", got)
	}
	if comp.CostUSD == nil || *comp.CostUSD != 0.0042 {
		t.Errorf("DeepInfra estimated_cost not read: %v", comp.CostUSD)
	}
}

func TestChat429Backoff(t *testing.T) {
	var calls atomic.Int32
	var rateLimitSeen atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 2 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(fakeReply))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(ClientOptions{BaseURL: srv.URL, APIKey: "cb_test", Model: "m", Timeout: 5 * time.Second, Backoff: time.Millisecond})
	c.OnRateLimit = func() { rateLimitSeen.Add(1) }
	comp, err := c.Chat(context.Background(), "k", []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Chat after 429s: %v", err)
	}
	if comp.Usage.PromptTokens != 1000 {
		t.Errorf("usage: %+v", comp.Usage)
	}
	if calls.Load() != 3 {
		t.Errorf("want 3 calls (2x429 + success), got %d", calls.Load())
	}
	if rateLimitSeen.Load() != 2 {
		t.Errorf("OnRateLimit: want 2, got %d", rateLimitSeen.Load())
	}
}

func TestChat429GivesUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "0")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	c := NewClient(ClientOptions{BaseURL: srv.URL, APIKey: "cb_test", Model: "m", Timeout: 5 * time.Second, Backoff: time.Millisecond})
	if _, err := c.Chat(context.Background(), "k", nil); err == nil {
		t.Fatal("expected error after exhausting 429 retries")
	}
}

func TestChatServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	c := NewClient(ClientOptions{BaseURL: srv.URL, APIKey: "cb_test", Model: "m", Timeout: 5 * time.Second})
	if _, err := c.Chat(context.Background(), "k", nil); err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestChatJSONMode(t *testing.T) {
	var formats []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := jsonDecode(r, &body); err != nil {
			t.Fatal(err)
		}
		formats = append(formats, body["response_format"])
		okHandler(fakeReply)(w, r)
	}))
	defer srv.Close()
	jsonMsgs := []Message{{Role: "user", Content: "Reply with JSON only."}}
	textMsgs := []Message{{Role: "user", Content: "Write a letter."}}

	on := NewClient(ClientOptions{BaseURL: srv.URL, Model: "json-on", JSON: true})
	off := NewClient(ClientOptions{BaseURL: srv.URL, Model: "json-off"})
	for _, call := range []struct {
		c    *Client
		msgs []Message
	}{{on, jsonMsgs}, {on, textMsgs}, {off, jsonMsgs}} {
		if _, err := call.c.Chat(context.Background(), "", call.msgs); err != nil {
			t.Fatal(err)
		}
	}
	if f, ok := formats[0].(map[string]any); !ok || f["type"] != "json_object" {
		t.Errorf("JSON call sent response_format %v, want json_object", formats[0])
	}
	if formats[1] != nil || formats[2] != nil {
		t.Errorf("plain-text and JSON-off calls sent response_format %v, %v", formats[1], formats[2])
	}
}

func TestChatJSONModeFallback(t *testing.T) {
	var withFormat, without atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := jsonDecode(r, &body); err != nil {
			t.Fatal(err)
		}
		if body["response_format"] != nil {
			withFormat.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"response_format is not supported by this model"}}`))
			return
		}
		without.Add(1)
		okHandler(fakeReply)(w, r)
	}))
	defer srv.Close()
	msgs := []Message{{Role: "user", Content: "Reply with JSON only."}}
	for range 2 {
		c := NewClient(ClientOptions{BaseURL: srv.URL, Model: "no-json-mode", JSON: true})
		comp, err := c.Chat(context.Background(), "", msgs)
		if err != nil || comp.Content == "" {
			t.Fatalf("Chat = %v, %v; want the reply sent without response_format", comp, err)
		}
	}
	// The refusal is remembered: the second client goes straight to plain.
	if withFormat.Load() != 1 || without.Load() != 2 {
		t.Errorf("calls with response_format %d, without %d; want 1 and 2", withFormat.Load(), without.Load())
	}
}

func TestChatJSONModeOtherErrorsStay(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"context length exceeded"}}`))
	}))
	defer srv.Close()
	c := NewClient(ClientOptions{BaseURL: srv.URL, Model: "json-other-error", JSON: true})
	if _, err := c.Chat(context.Background(), "", []Message{{Role: "user", Content: "JSON please"}}); err == nil {
		t.Fatal("want the 400 returned")
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (no retry for an unrelated 400)", calls.Load())
	}
}
