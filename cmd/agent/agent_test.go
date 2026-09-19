package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/errs"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type running struct {
	url    string
	client *http.Client
	stop   context.CancelFunc
	done   chan error
	agent  *agent
}

func testConfig(role string) config.Config {
	c := config.Config{Role: role, Host: role + ".localhost", Port: 8443}
	c.ApplyDefaults()
	return c
}

func start(t *testing.T, cfg config.Config) *running {
	t.Helper()
	a, err := newAgent(cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.serve(ctx, ln) }()
	pool := x509.NewCertPool()
	pool.AddCert(a.tls.Certificates[0].Leaf)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: pool, ServerName: "localhost"},
		ForceAttemptHTTP2: true, // browsers and curl use h2; test what they get
	}}
	r := &running{url: "https://" + ln.Addr().String(), client: client, stop: stop, done: done, agent: a}
	t.Cleanup(func() {
		stop()
		<-done
	})
	return r
}

func getJSON(t *testing.T, c *http.Client, method, url string, v any) int {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("%s %s: body not JSON: %v", method, url, err)
	}
	return resp.StatusCode
}

// Acceptance 2 (unit level): /health returns status ok over TLS.
func TestHealthOK(t *testing.T) {
	r := start(t, testConfig("station"))
	var body map[string]string
	if code := getJSON(t, r.client, http.MethodGet, r.url+"/health", &body); code != 200 {
		t.Fatalf("status %d", code)
	}
	if body["status"] != "ok" || body["role"] != "station" || body["host"] != "station.localhost" {
		t.Fatalf("body %v", body)
	}
}

// Acceptance 3 (unit level): a subscriber that connects after startup still
// receives agent_started.
func TestEventsReplaysAgentStarted(t *testing.T) {
	r := start(t, testConfig("ops"))
	resp, err := r.client.Get(r.url + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var e bus.Event
		if err := json.Unmarshal([]byte(line[6:]), &e); err != nil {
			t.Fatal(err)
		}
		if e.Kind != "agent_started" || e.Agent != "ops.localhost" || e.TS == 0 {
			t.Fatalf("first event %+v", e)
		}
		return
	}
	t.Fatalf("stream ended without an event: %v", sc.Err())
}

func TestRejectionsAreNamed(t *testing.T) {
	r := start(t, testConfig("ops"))
	cases := []struct {
		method, path string
		status       int
		code         errs.Code
	}{
		{http.MethodGet, "/nope", 404, errs.NotFound},
		{http.MethodGet, "/health/extra", 404, errs.NotFound},
		{http.MethodPost, "/health", 405, errs.MethodNotAllowed},
		{http.MethodDelete, "/events", 405, errs.MethodNotAllowed},
	}
	for _, c := range cases {
		var body errs.Error
		if got := getJSON(t, r.client, c.method, r.url+c.path, &body); got != c.status || body.Code != c.code {
			t.Errorf("%s %s = %d %+v", c.method, c.path, got, body)
		}
	}
}

func TestRecoverReturnsInternal(t *testing.T) {
	a := &agent{log: quiet}
	h := a.recoverMW(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	var body errs.Error
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != 500 || body.Code != errs.Internal || strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestRecoverAfterHeadersWritten(t *testing.T) {
	a := &agent{log: quiet}
	h := a.recoverMW(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("data: partial\n\n"))
		panic("late")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Body.String() != "data: partial\n\n" {
		t.Fatalf("error spliced into started body: %q", rec.Body.String())
	}
}

func TestShutdownWithOpenStream(t *testing.T) {
	r := start(t, testConfig("ops"))
	resp, err := r.client.Get(r.url + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("proto %s, want HTTP/2", resp.Proto)
	}
	r.stop()
	select {
	case err := <-r.done:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
		r.done <- nil // let Cleanup's receive finish
	case <-time.After(shutdownTimeout - time.Second):
		t.Fatal("shutdown blocked by open SSE stream")
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "agent_stopping") {
		t.Fatalf("stream did not carry agent_stopping: %q", body)
	}
}

func TestLoadsConfiguredCert(t *testing.T) {
	gen, err := selfSigned("gs.example", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keyDER, err := x509.MarshalPKCS8PrivateKey(gen.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig("station")
	cfg.Cert = config.Cert{CertFile: filepath.Join(dir, "c.pem"), KeyFile: filepath.Join(dir, "k.pem")}
	writePEM(t, cfg.Cert.CertFile, "CERTIFICATE", gen.Certificate[0])
	writePEM(t, cfg.Cert.KeyFile, "PRIVATE KEY", keyDER)

	c, err := loadOrGenerateCert(cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if string(c.Certificate[0]) != string(gen.Certificate[0]) {
		t.Fatal("served cert is not the configured one")
	}
	cfg.Cert.KeyFile = filepath.Join(dir, "missing.pem")
	if _, err := loadOrGenerateCert(cfg, quiet); err == nil {
		t.Fatal("missing key accepted")
	}
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSelfSignedCoversHost(t *testing.T) {
	c, err := selfSigned("gs-blacksburg.localhost", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"gs-blacksburg.localhost", "localhost", "127.0.0.1"} {
		if err := c.Leaf.VerifyHostname(h); err != nil {
			t.Errorf("%s: %v", h, err)
		}
	}
}

func TestRunRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "a.yaml")
	if err := os.WriteFile(cfg, []byte("role: ops\nport: 8443\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := [][]string{
		{},
		{"--config", filepath.Join(dir, "missing.yaml")},
		{"--config", cfg, "--role", "rogue"},
		{"--bogus"},
	}
	for _, args := range cases {
		if err := run(context.Background(), args, io.Discard); err == nil {
			t.Errorf("run(%v) = nil, want error", args)
		}
	}
}
