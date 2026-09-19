package trust

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agentnameservice/agent-trust-discovery/overpassapi"

	"github.com/arjitsama/overpass/internal/auditor"
	"github.com/arjitsama/overpass/internal/authority"
	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/schema"
)

// startIndex boots the forked trust index in-process over httptest and returns a
// client pointed at it. Admin auth is off for the test (a real deploy sets it).
func startIndex(t *testing.T) *Client {
	t.Helper()
	const dir = "../../third_party/agent-trust-discovery/config"
	handler, closeDB, err := overpassapi.Start(context.Background(), t.TempDir()+"/trust.db", dir, "", nil)
	if err != nil {
		t.Fatalf("build index: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(func() { srv.Close(); _ = closeDB() })
	// A monotonically increasing clock: the index keeps the latest observation
	// per (agent, signal) by observedAt, so distinct timestamps let repeated
	// pass_delivery posts overwrite rather than tie and keep the first.
	var n int64
	return &Client{BaseURL: srv.URL, HTTP: srv.Client(), Now: func() time.Time { n++; return time.Unix(1_700_000_000+n, 0) }}
}

// healthyIntegrity imports the observations a prober/hydrator would record for a
// well-run station (the §5.1 worked example), yielding integrity ~89 and a real
// DV identity of 40. These are TEST FIXTURES standing in for the production
// prober; the demo seed script never writes them (docs/status/phase-8.md).
func healthyIntegrity(t *testing.T, c *Client, id string) {
	t.Helper()
	ctx := context.Background()
	fp := "SHA256:" + strings.Repeat("a", 64)
	fpMatched := json.RawMessage(`{"expected":"` + fp + `","observed":"` + fp + `","matched":true,"expectedSource":"tl_attestation"}`)
	dnsMatched := json.RawMessage(`{"expected":"v=ans1","observed":"v=ans1","matched":true,"expectedSource":"tl_attestation"}`)
	obs := []struct {
		sig string
		val json.RawMessage
	}{
		{"certtype", json.RawMessage(`{"type":"DV"}`)},
		{"dnssecurity", json.RawMessage(`{"dnssec":true,"caa":true}`)},
		{"versionstability", json.RawMessage(`{"versionChanges30d":2}`)},
		{"certfingerprint.server", fpMatched},
		{"certfingerprint.identity", fpMatched},
		{"dnsrecord.ans", dnsMatched},
		{"dnsrecord.ans-badge", dnsMatched},
	}
	for _, o := range obs {
		if err := c.ImportObservation(ctx, id, o.sig, o.val, "https://auditor/evidence"); err != nil {
			t.Fatalf("import %s: %v", o.sig, err)
		}
	}
}

func importAgent(t *testing.T, c *Client, id string) {
	t.Helper()
	old := time.Unix(1_700_000_000, 0).AddDate(0, 0, -163).UTC().Format(time.RFC3339)
	now := time.Unix(1_700_000_000, 0).UTC().Format(time.RFC3339)
	if err := c.ImportAgents(context.Background(), []Agent{{
		AgentID: id, DNSName: id + ".example", DisplayName: id, Status: "ACTIVE", FirstSeen: old, LastUpdated: now,
	}}); err != nil {
		t.Fatalf("import agent %s: %v", id, err)
	}
}

// Acceptance 2 (phase 8, revised): a station with earned history is uplink-
// eligible under Overpass policy; the lookalike, identical but with no audited
// passes, is downlink-probation only and an uplink mandate for it is refused.
// The evaluation shows five dimensions with solvency+safety 0 and a real identity.
func TestTierEligibility(t *testing.T) {
	c := startIndex(t)
	ctx := context.Background()
	var rules config.FlightRules // defaults

	// Honest: healthy integrity + three delivered passes.
	importAgent(t, c, "honest")
	healthyIntegrity(t, c, "honest")
	if err := c.ImportObservation(ctx, "honest", "pass_delivery",
		json.RawMessage(`{"booked":3,"delivered":3,"auditFailures":0}`), "https://auditor/pass"); err != nil {
		t.Fatal(err)
	}
	// Lookalike: same identity/integrity, no audited passes.
	importAgent(t, c, "lookalike")
	healthyIntegrity(t, c, "lookalike")

	honest, err := c.Evaluate(ctx, "honest")
	if err != nil {
		t.Fatal(err)
	}
	if honest.Integrity < 80 || honest.Behavior != 100 || honest.AuditedPasses != 3 || honest.AuditFailures {
		t.Fatalf("honest eval = %+v", honest)
	}
	if honest.Identity != 40 || honest.CertType != "DV" {
		t.Errorf("identity shown as measured: got %d/%s, want 40/DV", honest.Identity, honest.CertType)
	}
	if honest.Solvency != 0 || honest.Safety != 0 {
		t.Errorf("solvency/safety must be 0 (no signal): %+v", honest)
	}
	if got := authority.OverpassTier(honest, rules); got != "FIDUCIARY" {
		t.Errorf("honest tier = %s, want FIDUCIARY (uplink-eligible)", got)
	}

	look, err := c.Evaluate(ctx, "lookalike")
	if err != nil {
		t.Fatal(err)
	}
	if look.Behavior != 0 || look.AuditedPasses != 0 {
		t.Fatalf("lookalike eval = %+v", look)
	}
	if got := authority.OverpassTier(look, rules); got != "TRANSACTIONAL" {
		t.Errorf("lookalike tier = %s, want TRANSACTIONAL (downlink-probation, no uplink)", got)
	}
}

// Acceptance 3 (phase 8, revised): after the auditor posts a CANARY_ACCEPTED
// (an audit failure), the rogue's behavior is capped at 40 and it loses uplink.
func TestCanaryDropsUplink(t *testing.T) {
	c := startIndex(t)
	ctx := context.Background()
	var rules config.FlightRules

	importAgent(t, c, "rogue")
	healthyIntegrity(t, c, "rogue")
	if err := c.ImportObservation(ctx, "rogue", "pass_delivery",
		json.RawMessage(`{"booked":3,"delivered":3,"auditFailures":0}`), "https://auditor/pass"); err != nil {
		t.Fatal(err)
	}
	before, _ := c.Evaluate(ctx, "rogue")
	if authority.OverpassTier(before, rules) != "FIDUCIARY" {
		t.Fatalf("rogue should start uplink-eligible: %+v", before)
	}

	// The auditor catches a canary acceptance and reports a failed pass.
	if err := c.Post(ctx, auditor.Observation{Station: "rogue", PassID: "canary-1", Verdict: schema.Fail, AuditFailures: 1}); err != nil {
		t.Fatal(err)
	}
	after, _ := c.Evaluate(ctx, "rogue")
	if !after.AuditFailures || after.Behavior > 40 {
		t.Fatalf("rogue behavior should be capped with an audit failure: %+v", after)
	}
	if got := authority.OverpassTier(after, rules); got == "FIDUCIARY" {
		t.Errorf("rogue kept uplink after CANARY_ACCEPTED: tier %s", got)
	}
}

// Acceptance 4 (phase 8): an observation for an unknown agent surfaces the index's
// 422 body rather than being swallowed.
func TestUnknownAgentSurfaces422(t *testing.T) {
	c := startIndex(t)
	err := c.ImportObservation(context.Background(), "ghost", "pass_delivery",
		json.RawMessage(`{"booked":1,"delivered":1,"auditFailures":0}`), "")
	if err == nil {
		t.Fatal("want an error for an unknown agent")
	}
	if !strings.Contains(err.Error(), "422") || !strings.Contains(err.Error(), "AGENT_NOT_FOUND") {
		t.Errorf("error should surface the 422 body: %v", err)
	}
}

// Post accumulates warm-up passes into a genuine history via read-modify-write.
func TestPostAccumulates(t *testing.T) {
	c := startIndex(t)
	ctx := context.Background()
	importAgent(t, c, "warm")
	for i := 0; i < 3; i++ {
		if err := c.Post(ctx, auditor.Observation{Station: "warm", PassID: "p", Verdict: schema.Pass}); err != nil {
			t.Fatal(err)
		}
	}
	e, _ := c.Evaluate(ctx, "warm")
	if e.AuditedPasses != 3 || e.Behavior != 100 || e.AuditFailures {
		t.Fatalf("after three passes: %+v", e)
	}
}
