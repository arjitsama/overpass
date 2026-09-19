// Package webmesh is a small MCP client for GoDaddy's reference agent at
// https://agent.webmesh.ai/mcp/ (streamable HTTP, MCP 2025-03-26). It reads
// tools/list on every session and builds verify and discover calls from the
// returned input schemas rather than assuming tool names: the live server
// names its verifier verify_agent{agent_host, environment}, not verify, and
// offers no discover tool. Read-only; nothing here writes to any registry.
package webmesh

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultURL is the reference agent's MCP endpoint. /mcp redirects here.
const DefaultURL = "https://agent.webmesh.ai/mcp/"

// ProtocolVersion is the MCP revision the server speaks.
const ProtocolVersion = "2025-03-26"

const maxResponseBytes = 4 << 20

// ErrNoTool means the server offers no tool that fits the request.
var ErrNoTool = errors.New("webmesh: no matching MCP tool")

// Tool is one entry from tools/list.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Client talks to one MCP endpoint.
type Client struct {
	URL     string
	HTTP    *http.Client
	mu      sync.Mutex
	session string
	nextID  atomic.Int64
}

// New returns a client for url (DefaultURL if empty).
func New(url string) *Client {
	if url == "" {
		url = DefaultURL
	}
	return &Client{URL: url, HTTP: &http.Client{Timeout: 60 * time.Second, CheckRedirect: sameOrigin}}
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// Tools runs the MCP handshake (initialize, notifications/initialized) and
// returns tools/list.
func (c *Client) Tools(ctx context.Context) ([]Tool, error) {
	c.setSession("")
	if _, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion, "capabilities": map[string]any{},
		"clientInfo": map[string]string{"name": "overpass", "version": "0.1.0"},
	}); err != nil {
		return nil, err
	}
	if err := c.notify(ctx, "notifications/initialized"); err != nil {
		return nil, err
	}
	raw, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("webmesh: tools/list: %w", err)
	}
	return out.Tools, nil
}

// Verify asks the reference agent to verify host, with the tool and argument
// names taken from tools/list. It returns the tool's text content.
func (c *Client) Verify(ctx context.Context, host string) (string, error) {
	tools, err := c.Tools(ctx)
	if err != nil {
		return "", err
	}
	t, arg, ok := pick(tools, "verify", []string{"agent_host", "host", "fqdn", "hostname", "domain"})
	if !ok {
		return "", ErrNoTool
	}
	return c.CallTool(ctx, t.Name, map[string]any{arg: host})
}

// Discover asks the reference agent to search for agents by query. The live
// server offers no discover tool today; this returns ErrNoTool until it does.
func (c *Client) Discover(ctx context.Context, query string) (string, error) {
	tools, err := c.Tools(ctx)
	if err != nil {
		return "", err
	}
	t, arg, ok := pick(tools, "discover", []string{"query", "q", "text", "capability", "tag"})
	if !ok {
		return "", ErrNoTool
	}
	return c.CallTool(ctx, t.Name, map[string]any{arg: query})
}

// pick finds the tool named verb, or else verb_<something> (the live
// server's verify_agent), whose schema has a string property from args,
// required ones first. An exact name wins over a prefixed one.
func pick(tools []Tool, verb string, args []string) (Tool, string, bool) {
	for _, exact := range []bool{true, false} {
		for _, t := range tools {
			n := strings.ToLower(t.Name)
			if (exact && n != verb) || (!exact && !strings.HasPrefix(n, verb+"_")) {
				continue
			}
			if arg, ok := stringArg(t, args); ok {
				return t, arg, true
			}
		}
	}
	return Tool{}, "", false
}

// stringArg returns the first string property of t's input schema that is in
// args, looking at required properties first.
func stringArg(t Tool, args []string) (string, bool) {
	var schema struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if json.Unmarshal(t.InputSchema, &schema) != nil {
		return "", false
	}
	for _, candidates := range [][]string{schema.Required, args} {
		for _, a := range candidates {
			if p, ok := schema.Properties[a]; ok && p.Type == "string" && contains(args, a) {
				return a, true
			}
		}
	}
	return "", false
}

// sameOrigin allows only redirects on the original scheme and host (the
// server redirects /mcp to /mcp/).
func sameOrigin(req *http.Request, via []*http.Request) error {
	if len(via) >= 3 {
		return errors.New("too many redirects")
	}
	o := via[0].URL
	if req.URL.Scheme != o.Scheme || !strings.EqualFold(req.URL.Host, o.Host) {
		return fmt.Errorf("refusing cross-origin redirect to %s", req.URL.Redacted())
	}
	return nil
}

func (c *Client) setSession(s string) {
	c.mu.Lock()
	c.session = s
	c.mu.Unlock()
}

func (c *Client) getSession() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// CallTool runs tools/call and returns the text parts of the result.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	raw, err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("webmesh: tools/call %s: %w", name, err)
	}
	var b strings.Builder
	for _, p := range out.Content {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	if out.IsError {
		return b.String(), fmt.Errorf("webmesh: tool %s reported an error", name)
	}
	return b.String(), nil
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	resp, err := c.post(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.setSession(sid)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("webmesh: %s: HTTP %s", method, resp.Status)
	}
	msg, err := readResponse(resp)
	if err != nil {
		return nil, fmt.Errorf("webmesh: %s: %w", method, err)
	}
	if string(msg.ID) != fmt.Sprint(id) {
		return nil, fmt.Errorf("webmesh: %s: response id %s does not match request %d", method, msg.ID, id)
	}
	if msg.Error != nil {
		return nil, fmt.Errorf("webmesh: %s: %d %s", method, msg.Error.Code, msg.Error.Message)
	}
	return msg.Result, nil
}

func (c *Client) notify(ctx context.Context, method string) error {
	body, _ := json.Marshal(map[string]string{"jsonrpc": "2.0", "method": method})
	resp, err := c.post(ctx, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("webmesh: %s: HTTP %s", method, resp.Status)
	}
	return nil
}

func (c *Client) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if s := c.getSession(); s != "" {
		req.Header.Set("Mcp-Session-Id", s)
	}
	return c.HTTP.Do(req)
}

// readResponse accepts a plain JSON body or an SSE stream whose data lines
// carry the JSON-RPC response (both allowed by streamable HTTP).
func readResponse(resp *http.Response) (rpcResponse, error) {
	body := io.LimitReader(resp.Body, maxResponseBytes)
	var msg rpcResponse
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		err := json.NewDecoder(body).Decode(&msg)
		return msg, err
	}
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), maxResponseBytes)
	var data []string
	try := func() (rpcResponse, bool) {
		var m rpcResponse // fresh per event, so nothing leaks between events
		ok := json.Unmarshal([]byte(strings.Join(data, "\n")), &m) == nil && (m.Result != nil || m.Error != nil)
		data = nil
		return m, ok
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case line == "" && len(data) > 0:
			if m, ok := try(); ok {
				return m, nil
			}
		}
	}
	if len(data) > 0 {
		if m, ok := try(); ok {
			return m, nil
		}
	}
	return rpcResponse{}, errors.New("no JSON-RPC response in event stream")
}
