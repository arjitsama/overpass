package webmesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeMCP mimics the live server's tools/list (captured 2026-09-19) and
// answers SSE for tools/call to cover both response forms.
func fakeMCP(t *testing.T, calls *[]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		*calls = append(*calls, req.Method)
		switch req.Method {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-03-26","capabilities":{"tools":{}},"serverInfo":{"name":"fake"}}}`, req.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[
				{"name":"verify_agent","inputSchema":{"properties":{"agent_host":{"type":"string"},"environment":{"default":"prod","type":"string"}},"required":["agent_host"],"type":"object"}},
				{"name":"search_encounter_store","inputSchema":{"properties":{"verified_only":{"type":"boolean"}},"type":"object"}}]}}`, req.ID)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"echo %s\"}]}}\n\n", req.ID, strings.ReplaceAll(string(req.Params), `"`, `\"`))
		default:
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"nope"}}`, req.ID)
		}
	}))
}

func TestVerifyBuildsCallFromSchema(t *testing.T) {
	var calls []string
	srv := fakeMCP(t, &calls)
	defer srv.Close()
	out, err := New(srv.URL).Verify(context.Background(), "gs-blacksburg.example")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"name":"verify_agent"`) || !strings.Contains(out, `"agent_host":"gs-blacksburg.example"`) {
		t.Fatalf("call built wrong: %s", out)
	}
	if strings.Join(calls, ",") != "initialize,notifications/initialized,tools/list,tools/call" {
		t.Fatalf("sequence %v", calls)
	}
}

func TestDiscoverWithoutToolIsNamed(t *testing.T) {
	var calls []string
	srv := fakeMCP(t, &calls)
	defer srv.Close()
	if _, err := New(srv.URL).Discover(context.Background(), "uplink-uhf"); !errors.Is(err, ErrNoTool) {
		t.Fatalf("err = %v", err)
	}
}

func TestRPCErrorSurfaces(t *testing.T) {
	var calls []string
	srv := fakeMCP(t, &calls)
	defer srv.Close()
	if _, err := New(srv.URL).call(context.Background(), "bogus", nil); err == nil || !strings.Contains(err.Error(), "-32601") {
		t.Fatalf("err = %v", err)
	}
}

// Read-only live check against the reference agent, skipped unless ANS_LIVE=1.
func TestLiveTools(t *testing.T) {
	if os.Getenv("ANS_LIVE") != "1" {
		t.Skip("set ANS_LIVE=1 for the live MCP check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := New("")
	tools, err := c.Tools(ctx)
	if err != nil || len(tools) == 0 {
		t.Fatalf("tools: %v %v", tools, err)
	}
	out, err := c.Verify(ctx, "agent.webmesh.ai")
	if err != nil || out == "" {
		t.Fatalf("verify: %q %v", out, err)
	}
	t.Logf("verify_agent(agent.webmesh.ai): %.300s", out)
}

// pick prefers an exact name, then verb_<x>, and never a mere substring.
func TestPickToolNames(t *testing.T) {
	schema := json.RawMessage(`{"properties":{"agent_host":{"type":"string"}},"required":["agent_host"]}`)
	tools := []Tool{{Name: "unverify_x", InputSchema: schema}, {Name: "verify_agent", InputSchema: schema}, {Name: "verify", InputSchema: schema}}
	if got, _, _ := pick(tools, "verify", []string{"agent_host"}); got.Name != "verify" {
		t.Fatalf("picked %s", got.Name)
	}
	if got, _, _ := pick(tools[:2], "verify", []string{"agent_host"}); got.Name != "verify_agent" {
		t.Fatalf("picked %s", got.Name)
	}
	if _, _, ok := pick(tools[:1], "verify", []string{"agent_host"}); ok {
		t.Fatal("picked unverify_x")
	}
}

func TestCrossOriginRedirectRefused(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/mcp/", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	if _, err := New(srv.URL).Tools(context.Background()); err == nil || !strings.Contains(err.Error(), "cross-origin") {
		t.Fatalf("err = %v", err)
	}
}

func TestMismatchedResponseIDRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":999,"result":{}}`))
	}))
	defer srv.Close()
	if _, err := New(srv.URL).call(context.Background(), "initialize", nil); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v", err)
	}
}
