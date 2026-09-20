package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arjitsama/overpass/internal/config"
)

func TestUIAgentsSnapshotIsHonestBeforeAnyRun(t *testing.T) {
	a := opsAgent()
	defer a.bus.Close()
	a.cfg.UI.Agents = []config.UIAgent{{Host: "gs-blacksburg.example", Role: "Ground station"}}

	rec := httptest.NewRecorder()
	a.uiAgentsSnapshot(rec, httptest.NewRequest(http.MethodGet, "/ui/agents", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out uiAgents
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// Nothing has been verified yet, so no agent may be claimed as verified —
	// but the honest frame (no trust index, the real access basis) is present.
	if len(out.Agents) != 0 {
		t.Errorf("agents = %d before any verification, want none", len(out.Agents))
	}
	if out.TrustIndex != "not deployed" {
		t.Errorf("trust_index = %q, want %q", out.TrustIndex, "not deployed")
	}
	if out.AccessBasis == "" {
		t.Error("access_basis must say what actually grants uplink")
	}
	if out.CheckedAt != "" {
		t.Errorf("checked_at = %q before any run, want empty", out.CheckedAt)
	}
}

// An agent that is registered but not running is listed and labelled, never
// contacted and never shown as verified.
func TestRefreshAgentsLabelsUndeployed(t *testing.T) {
	a := opsAgent()
	defer a.bus.Close()
	no := false
	a.cfg.UI.Agents = []config.UIAgent{{Host: "gs-awarua.example", Role: "Ground station", Deployed: &no}}

	snap := a.refreshAgents(t.Context())
	if len(snap.Agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(snap.Agents))
	}
	got := snap.Agents[0]
	if got.Deployed || got.Verified || got.Verdict != "not deployed" {
		t.Errorf("undeployed agent = %+v, want a labelled, unverified row", got)
	}
	if len(got.Checks) != 0 {
		t.Errorf("undeployed agent must not carry checks, got %d", len(got.Checks))
	}
}

// The on-demand refresh is rate-limited so a held-down button cannot hammer
// the transparency log.
func TestUIRefreshAgentsIsRateLimited(t *testing.T) {
	a := opsAgent()
	defer a.bus.Close()
	a.cfg.UI.Agents = nil // nothing to verify: the run is instant

	rec := httptest.NewRecorder()
	a.uiRefreshAgents(rec, httptest.NewRequest(http.MethodPost, "/ui/refresh-agents", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("first refresh: status %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	a.uiRefreshAgents(rec, httptest.NewRequest(http.MethodPost, "/ui/refresh-agents", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("second refresh: status %d, want 503", rec.Code)
	}
}

func TestUIBatteryLive(t *testing.T) {
	a := opsAgent()
	defer a.bus.Close()

	// With no record, the route 404s so the page says "no live run recorded"
	// rather than falling back to recorded data.
	rec := httptest.NewRecorder()
	a.uiBatteryLive(rec, httptest.NewRequest(http.MethodGet, "/ui/battery-live", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing record: status %d, want 404", rec.Code)
	}

	path := filepath.Join(t.TempDir(), "battery-live.json")
	body := `{"ran_at":"2026-09-20T05:30:00Z","blocked":21,"total":23,"results":[{"name":"forged_mandate","verdict":"BLOCKED"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a.cfg.UI.BatteryLiveFile = path
	rec = httptest.NewRecorder()
	a.uiBatteryLive(rec, httptest.NewRequest(http.MethodGet, "/ui/battery-live", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		RanAt   string           `json:"ran_at"`
		Blocked int              `json:"blocked"`
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.RanAt == "" || out.Blocked != 21 || len(out.Results) != 1 {
		t.Errorf("battery record round-trip = %+v", out)
	}
}

func TestParseFraudMarkdown(t *testing.T) {
	md := "# GoDaddy fraud battery\n\n" +
		"Status 2026-09-20 05:00: **in progress, no pass claimed.** More prose.\n\n" +
		"| # | their check | run by | their verdict | code |\n" +
		"|---|---|---|---|---|\n" +
		"| 1 | payto_binding_check | their agent | PASS | x402 |\n" +
		"| 2 | card_drift_watch | their agent | INCONCLUSIVE | — |\n"
	status, rows := parseFraudMarkdown(md)
	if status != "Status 2026-09-20 05:00: in progress, no pass claimed. More prose." {
		t.Errorf("status = %q", status)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(rows), rows)
	}
	if rows[0].Check != "payto_binding_check" || rows[0].Verdict != "PASS" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].Verdict != "INCONCLUSIVE" {
		t.Errorf("row 1 = %+v", rows[1])
	}
}

// The real red-team log parses, and its status line still claims no pass.
func TestFraudRedteamDocParses(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "status", "fraud-redteam.md"))
	if err != nil {
		t.Skipf("no fraud-redteam.md: %v", err)
	}
	status, rows := parseFraudMarkdown(string(raw))
	if status == "" {
		t.Error("fraud-redteam.md has no status line")
	}
	if len(rows) == 0 {
		t.Error("fraud-redteam.md results table did not parse")
	}
}

func TestAgentsCacheRateGate(t *testing.T) {
	var c agentsCache
	now := time.Date(2026, 9, 20, 5, 0, 0, 0, time.UTC)
	if !c.begin(now, time.Minute) {
		t.Fatal("first run must be allowed")
	}
	if c.begin(now, time.Minute) {
		t.Error("a second run must not start while one is running")
	}
	c.finish(uiAgents{CheckedAt: "x"})
	if c.begin(now.Add(30*time.Second), time.Minute) {
		t.Error("a run within the rate floor must be refused")
	}
	if !c.begin(now.Add(2*time.Minute), time.Minute) {
		t.Error("a run after the rate floor must be allowed")
	}
}
