package scoring

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// request is the chat-completions request body.
type request struct {
	Model          string    `json:"model"`
	PromptCacheKey string    `json:"prompt_cache_key,omitempty"`
	Temperature    float64   `json:"temperature"`
	MaxTokens      int       `json:"max_tokens"`
	Messages       []Message `json:"messages"`
	// ResponseFormat asks for a JSON object reply ({"type":"json_object"}).
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type responseFormat struct {
	Type string `json:"type"`
}

// jsonModeRejected remembers the endpoints and models that refused
// response_format, so later calls skip it instead of failing first.
var jsonModeRejected sync.Map

// completionUsage mirrors the OpenAI-style usage block. Cached tokens are
// nested in prompt_tokens_details.
type completionUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	// Providers' own per-call cost in USD: Coral reports "cost",
	// DeepInfra "estimated_cost".
	Cost          *float64 `json:"cost"`
	EstimatedCost *float64 `json:"estimated_cost"`
}

// response is the (subset of the) chat-completions reply we consume.
type response struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage completionUsage `json:"usage"`
	// Cost is a top-level per-call cost in USD, for providers that put it
	// there; otherwise the usage block's, otherwise the price table.
	Cost *float64 `json:"cost"`
}

// Completion is one successful model reply.
type Completion struct {
	Content string
	Usage   UsageStats
	// CostUSD is the API-reported cost when the provider supplies one.
	CostUSD *float64
}

// UsageStats carries the token accounting for one call.
type UsageStats struct {
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
}

// Client talks to an OpenAI-compatible chat-completions endpoint (Coral
// Bricks, DeepInfra, OpenAI, a local server, ...). It applies 60s timeouts and retries 429s with backoff.
type Client struct {
	http    *http.Client
	baseURL string
	apiKey  string
	model   string

	// OnRateLimit, when set, is invoked once for every 429 response so
	// the scorer can halve its effective concurrency.
	OnRateLimit func()

	// logFirstCall ensures the full usage object is logged once so field
	// names can be checked against what the provider actually returns.
	logFirst sync.Once
	log      *slog.Logger

	maxTokens   int
	baseBackoff time.Duration  // first 429 backoff; doubles each retry
	cacheKeys   bool           // send prompt_cache_key
	extraBody   map[string]any // merged into every request body
	json        bool           // ask for a JSON object reply
}

// ClientOptions configures NewClient.
type ClientOptions struct {
	BaseURL   string
	APIKey    string
	Model     string
	Timeout   time.Duration // default 60s
	MaxTokens int           // default 600
	Log       *slog.Logger
	// Backoff is the base delay before the first 429 retry; the sequence
	// doubles each time (1s, 2s, 4s, 8s by default). Tests shrink it.
	Backoff time.Duration
	// CacheKeys sends prompt_cache_key, for providers that route by it
	// (Coral). Others cache shared prefixes on their own.
	CacheKeys bool
	// ExtraBody is merged into every request body, for provider- or
	// model-specific options (e.g. turning off reasoning).
	ExtraBody map[string]any
	// JSON asks for a JSON object reply (response_format json_object) when
	// the messages mention JSON. A provider that rejects the field is
	// asked again without it, and not asked with it again.
	JSON bool
}

// NewClient builds a scoring client.
func NewClient(opts ClientOptions) *Client {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 600
	}
	backoff := opts.Backoff
	if backoff <= 0 {
		backoff = time.Second
	}
	return &Client{
		http:        &http.Client{Timeout: timeout},
		baseURL:     trimTrailingSlash(opts.BaseURL),
		apiKey:      opts.APIKey,
		model:       opts.Model,
		log:         opts.Log,
		maxTokens:   maxTokens,
		baseBackoff: backoff,
		cacheKeys:   opts.CacheKeys,
		extraBody:   opts.ExtraBody,
		json:        opts.JSON,
	}
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// Chat sends the messages to {baseURL}/chat/completions with the shared
// prompt cache key. On 429 it retries up to 5 attempts, honoring
// Retry-After when present and exponential backoff (1s, 2s, 4s, 8s)
// otherwise, notifying OnRateLimit each time.
func (c *Client) Chat(ctx context.Context, cacheKey string, messages []Message) (*Completion, error) {
	if !c.cacheKeys {
		cacheKey = ""
	}
	r := request{
		Model:          c.model,
		PromptCacheKey: cacheKey,
		Temperature:    0.1,
		MaxTokens:      c.maxTokens,
		Messages:       messages,
	}
	if c.wantsJSON(messages) {
		r.ResponseFormat = &responseFormat{Type: "json_object"}
		comp, err := c.send(ctx, r)
		if err == nil || !rejectsJSONMode(err) {
			return comp, err
		}
		jsonModeRejected.Store(c.jsonModeKey(), true)
		if c.log != nil {
			c.log.Warn("provider rejected response_format; sending without it", "model", c.model, "err", err)
		}
		r.ResponseFormat = nil
	}
	return c.send(ctx, r)
}

// wantsJSON reports whether this call should ask for JSON mode: the client
// is set for it, the provider has not refused it, the extra body does not
// set its own, and the messages say JSON (OpenAI refuses json_object
// otherwise).
func (c *Client) wantsJSON(messages []Message) bool {
	if !c.json {
		return false
	}
	if _, own := c.extraBody["response_format"]; own {
		return false
	}
	if _, refused := jsonModeRejected.Load(c.jsonModeKey()); refused {
		return false
	}
	for _, m := range messages {
		if strings.Contains(strings.ToLower(m.Content), "json") {
			return true
		}
	}
	return false
}

func (c *Client) jsonModeKey() string { return c.baseURL + "|" + c.model }

// statusError is a non-429 HTTP failure from the provider.
type statusError struct {
	code int
	msg  string
}

func (e *statusError) Error() string { return e.msg }

// rejectsJSONMode reports whether err is a provider refusing the
// response_format field rather than failing for another reason.
func rejectsJSONMode(err error) bool {
	var se *statusError
	if !errors.As(err, &se) || (se.code != http.StatusBadRequest && se.code != http.StatusUnprocessableEntity) {
		return false
	}
	m := strings.ToLower(se.msg)
	return strings.Contains(m, "response_format") || strings.Contains(m, "json_object") || strings.Contains(m, "response format")
}

// send posts one request, retrying 429s.
func (c *Client) send(ctx context.Context, r request) (*Completion, error) {
	body, err := c.body(r)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	endpoint := c.baseURL + "/chat/completions"

	backoffs := []time.Duration{
		c.baseBackoff, c.baseBackoff * 2, c.baseBackoff * 4, c.baseBackoff * 8,
	}
	var lastErr error
	attempt := 0
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request: %w", err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		_ = resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK:
			if readErr != nil {
				return nil, fmt.Errorf("read response: %w", readErr)
			}
			return c.decode(raw)
		case http.StatusTooManyRequests:
			lastErr = fmt.Errorf("HTTP 429 from %s", endpoint)
			if c.OnRateLimit != nil {
				c.OnRateLimit()
			}
		default:
			return nil, &statusError{resp.StatusCode, fmt.Sprintf("HTTP %d from %s: %s", resp.StatusCode, endpoint, truncate(raw))}
		}

		if attempt >= len(backoffs) {
			break
		}
		delay := backoffs[attempt]
		if secs, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil && secs > 0 {
			if d := time.Duration(secs) * time.Second; d > delay {
				delay = d
			}
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		attempt++
	}
	return nil, fmt.Errorf("giving up after 429 retries: %w", lastErr)
}

// decode parses a 200 response body and logs the full usage object once.
func (c *Client) decode(raw []byte) (*Completion, error) {
	var resp response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("response has no choices")
	}
	u := resp.Usage
	if c.log != nil {
		c.logFirst.Do(func() {
			var pretty map[string]any
			if json.Unmarshal(raw, &pretty) == nil {
				if usageRaw, ok := pretty["usage"]; ok {
					c.log.Info("first scoring usage object", "usage", usageRaw)
				}
			}
		})
	}
	return &Completion{
		Content: resp.Choices[0].Message.Content,
		Usage: UsageStats{
			PromptTokens:     int64(u.PromptTokens),
			CachedTokens:     int64(u.PromptTokensDetails.CachedTokens),
			CompletionTokens: int64(u.CompletionTokens),
		},
		CostUSD: firstCost(resp.Cost, u.Cost, u.EstimatedCost),
	}, nil
}

// body encodes the request with any extra fields merged in (the request's
// own fields win).
func (c *Client) body(r request) ([]byte, error) {
	b, err := json.Marshal(r)
	if err != nil || len(c.extraBody) == 0 {
		return b, err
	}
	merged := make(map[string]any, len(c.extraBody)+6)
	for k, v := range c.extraBody {
		merged[k] = v
	}
	var own map[string]any
	if err := json.Unmarshal(b, &own); err != nil {
		return nil, err
	}
	for k, v := range own {
		merged[k] = v
	}
	return json.Marshal(merged)
}

// firstCost is the first positive reported cost, or nil.
func firstCost(costs ...*float64) *float64 {
	for _, c := range costs {
		if c != nil && *c > 0 {
			return c
		}
	}
	return nil
}

// truncate keeps error messages short: the first 300 bytes of a body.
func truncate(b []byte) string {
	const n = 300
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
