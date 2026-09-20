package llmplan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Anthropic Messages API constants, from the current docs (platform.claude.com,
// Tool use). anthropicVersion is the required anthropic-version header value.
const (
	anthropicEndpoint = "https://api.anthropic.com/v1/messages"
	anthropicVersion  = "2023-06-01"
	// DefaultModel is a capable, fast model that fits the planner's 10s budget.
	DefaultModel = "claude-sonnet-5"
	// APIKeyEnv holds the key (human gate H5); never read from a config file.
	APIKeyEnv = "ANTHROPIC_API_KEY"
)

// Client is the real Model, calling the Anthropic Messages API.
type Client struct {
	APIKey string
	HTTP   *http.Client
}

// NewClient builds a client with the key from the environment (H5). It returns
// (nil, false) when the key is unset, so the caller can decide to run greedy-only.
func NewClient() (*Client, bool) {
	key := os.Getenv(APIKeyEnv)
	if key == "" {
		return nil, false
	}
	return &Client{APIKey: key, HTTP: &http.Client{Timeout: 30 * time.Second}}, true
}

// Create implements Model against POST /v1/messages.
func (c *Client) Create(ctx context.Context, req MessageRequest) (MessageResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return MessageResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicEndpoint, bytes.NewReader(body))
	if err != nil {
		return MessageResponse{}, err
	}
	httpReq.Header.Set("x-api-key", c.APIKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	httpReq.Header.Set("content-type", "application/json")

	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(httpReq)
	if err != nil {
		return MessageResponse{}, fmt.Errorf("anthropic: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return MessageResponse{}, fmt.Errorf("anthropic: %s: %s", resp.Status, bytes.TrimSpace(raw))
	}
	var out MessageResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return MessageResponse{}, fmt.Errorf("anthropic: decode response: %w", err)
	}
	return out, nil
}
