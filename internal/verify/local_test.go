package verify

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/config"
)

// Acceptance 1: against the local ANS reference stack (scripts/local-ans.sh
// start), the station registered by scripts/local-register.sh and served by
// bin/agent --config configs/local/station-ans.yaml verifies, and the
// unregistered ops agent fails with reasons. Skipped unless ANS_LOCAL=1;
// scripts/accept/phase-3.sh sets it up and runs it.
func TestLocalStack(t *testing.T) {
	if os.Getenv("ANS_LOCAL") != "1" {
		t.Skip("set ANS_LOCAL=1 with the local stack and agents running (scripts/accept/phase-3.sh)")
	}
	resp, err := http.Get("http://localhost:18081/root-keys")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	cfg := config.Config{
		Environments: map[string]config.Environment{"local": {
			RegistryURL: "http://localhost:18080", LogURL: "http://localhost:18081", LogPublicURL: "https://localhost:18081",
			FinderURL: "http://localhost:18082/v1", DNSServer: "127.0.0.1:15353", InsecureHTTP: true,
			RootKeys: []string{strings.TrimSpace(string(key))}}},
		Peers: []config.Peer{
			{Name: "gs-blacksburg", URL: "https://gs-blacksburg.localhost:8444", Env: "local", Dial: "127.0.0.1:8444"},
			{Name: "ops", URL: "https://ops.localhost:8443", Env: "local", Dial: "127.0.0.1:8443"},
		},
	}
	var events []bus.Event
	v, err := New(cfg, Options{Self: "test", Emit: func(e bus.Event) { events = append(events, e) }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	good := v.VerifyPeer(ctx, "gs-blacksburg.localhost")
	for _, c := range good.Checks {
		t.Logf("station %-15s %-4s %s", c.Name, c.Verdict, c.Reason)
	}
	if !good.OK() {
		t.Fatalf("registered station failed: %v", good.Failed())
	}
	for _, c := range good.Checks {
		if c.Verdict != Pass {
			t.Errorf("%s: %s %s", c.Name, c.Verdict, c.Reason)
		}
	}
	if len(good.Checks) != 8 {
		t.Fatalf("station ran %d checks", len(good.Checks))
	}

	bad := v.VerifyPeer(ctx, "ops.localhost")
	t.Logf("ops: %s %v", bad.Verdict, bad.Failed())
	if bad.OK() || len(bad.Failed()) == 0 || !strings.Contains(bad.Failed()[0], "_ans-badge") {
		t.Fatalf("unregistered ops: %+v", bad)
	}
	if len(events) != len(good.Checks)+len(bad.Checks)+2 {
		t.Fatalf("events %d", len(events))
	}

	// Discovery by tag through the local Finder finds the station.
	// The Finder indexes from the log's feed on a poll, so a fresh
	// registration shows up after a short delay.
	var found []Result
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		if found, err = v.FindByTag(ctx, "local", "uplink-uhf"); err == nil && len(found) > 0 {
			break
		}
	}
	if err != nil || len(found) != 1 || found[0].Host != "gs-blacksburg.localhost" || !found[0].OK() {
		t.Fatalf("FindByTag(uplink-uhf): %v %+v", err, found)
	}
}
