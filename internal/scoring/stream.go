package scoring

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// streamRequest is a chat-completions request that streams its reply.
type streamRequest struct {
	request
	Stream        bool `json:"stream"`
	StreamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

// streamChunk is one server-sent event of a streamed reply. The last one
// carries the usage (stream_options.include_usage) and no choices.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *completionUsage `json:"usage"`
	Cost  *float64         `json:"cost"`
}

// ChatStream sends the messages and calls onDelta with each piece of the
// reply as it arrives, returning the whole reply and its usage at the end.
// Unlike Chat it does not retry: a 429 or other failure before the first
// piece comes back as an error, and a stream cut short returns what
// arrived with the error.
func (c *Client) ChatStream(ctx context.Context, messages []Message, onDelta func(string)) (*Completion, error) {
	r := streamRequest{request: request{
		Model:       c.model,
		Temperature: 0.3,
		MaxTokens:   c.maxTokens,
		Messages:    messages,
	}, Stream: true}
	r.StreamOptions.IncludeUsage = true
	b, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	if len(c.extraBody) > 0 {
		merged := make(map[string]any, len(c.extraBody)+8)
		for k, v := range c.extraBody {
			merged[k] = v
		}
		var own map[string]any
		_ = json.Unmarshal(b, &own)
		for k, v := range own {
			merged[k] = v
		}
		if b, err = json.Marshal(merged); err != nil {
			return nil, err
		}
	}
	endpoint := c.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &statusError{resp.StatusCode, fmt.Sprintf("HTTP %d from %s: %s", resp.StatusCode, endpoint, truncate(raw))}
	}
	out := &Completion{}
	var text strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var ch streamChunk
		if json.Unmarshal([]byte(data), &ch) != nil {
			continue
		}
		for _, choice := range ch.Choices {
			if d := choice.Delta.Content; d != "" {
				text.WriteString(d)
				if onDelta != nil {
					onDelta(d)
				}
			}
		}
		if u := ch.Usage; u != nil {
			out.Usage = UsageStats{
				PromptTokens:     int64(u.PromptTokens),
				CachedTokens:     int64(u.PromptTokensDetails.CachedTokens),
				CompletionTokens: int64(u.CompletionTokens),
			}
			out.CostUSD = firstCost(ch.Cost, u.Cost, u.EstimatedCost)
		}
	}
	out.Content = text.String()
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("read stream: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	return out, nil
}
