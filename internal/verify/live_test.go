package verify

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/config"
)

// ProdRootKey is the production transparency log's root key as served at
// https://transparency.ans.godaddy.com/root-keys (read 2026-09-19).
const ProdRootKey = "transparency.ans.godaddy.com+c9e2f584+AjBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABJiE0eriKUOYbYrXerJlCJv6TZGEglLkPOHo+bEieNtPsL2FjuXfRCZbYF3RCwqF/99iDVxIUHJWTcW3KXqbiCU="

// Acceptance 2: read-only live check, skipped unless ANS_LIVE=1. It records
// the Result as a fixture in testdata/webmesh-result.json.
func TestLiveWebmesh(t *testing.T) {
	if os.Getenv("ANS_LIVE") != "1" {
		t.Skip("set ANS_LIVE=1 for the read-only live check against agent.webmesh.ai")
	}
	cfg := config.Config{Environments: map[string]config.Environment{config.ProdEnv: {
		RegistryURL: config.DefaultRegistryURL, LogURL: config.DefaultLogURL, RootKeys: []string{ProdRootKey}}}}
	var events []bus.Event
	v, err := New(cfg, Options{Self: "test", Emit: func(e bus.Event) { events = append(events, e) }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res := v.VerifyPeer(ctx, "agent.webmesh.ai")
	if len(res.Checks) != 8 || res.AgentID == "" {
		t.Fatalf("incomplete result: %+v", res)
	}
	for _, c := range res.Checks {
		t.Logf("%-15s %-4s %s %v", c.Name, c.Verdict, c.Reason, c.Detail)
	}
	raw, _ := json.MarshalIndent(res, "", "  ")
	if err := os.WriteFile("testdata/webmesh-result.json", append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if len(events) != len(res.Checks)+1 {
		t.Fatalf("events %d for %d checks", len(events), len(res.Checks))
	}
}
