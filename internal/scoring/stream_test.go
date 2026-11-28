package scoring

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("case") == "busy" {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": keep-alive\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"prompt_tokens_details\":{\"cached_tokens\":4},\"cost\":0.001}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	var pieces []string
	c := NewClient(ClientOptions{BaseURL: srv.URL, Model: "m"})
	comp, err := c.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, func(d string) { pieces = append(pieces, d) })
	if err != nil {
		t.Fatal(err)
	}
	if comp.Content != "Hello" || strings.Join(pieces, "|") != "Hel|lo" {
		t.Errorf("content %q, pieces %v", comp.Content, pieces)
	}
	if comp.Usage.PromptTokens != 10 || comp.Usage.CachedTokens != 4 || comp.Usage.CompletionTokens != 2 || comp.CostUSD == nil || *comp.CostUSD != 0.001 {
		t.Errorf("usage %+v cost %v", comp.Usage, comp.CostUSD)
	}
	busy := NewClient(ClientOptions{BaseURL: srv.URL + "/x?case=busy#", Model: "m"})
	if _, err := busy.ChatStream(context.Background(), nil, nil); err == nil {
		t.Error("a 429 must come back as an error")
	}
}
