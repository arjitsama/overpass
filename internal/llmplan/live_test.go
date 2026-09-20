package llmplan

import (
	"context"
	"os"
	"testing"

	"github.com/arjitsama/overpass/internal/authority"
	"github.com/arjitsama/overpass/internal/passes"
	"github.com/arjitsama/overpass/internal/planner"
	"github.com/arjitsama/overpass/internal/schema"
)

const (
	earlyHost = "gs-early.example"
	lateHost  = "gs-late.example"
)

func passAt(host string, aosOffset, elev int64) passes.Pass {
	return passes.Pass{Station: ansOf(host), Host: host, NoradID: 27844,
		AOS: base.Unix() + aosOffset, LOS: base.Unix() + aosOffset + 480, MaxElevationDeg: elev, DurationS: 480}
}

func quoteAt(host, mode string, aosOffset, elev, cents int64) schema.Quote {
	return schema.Quote{QuoteID: "q-" + host, Station: ansOf(host), NoradID: 27844,
		AOS: base.Unix() + aosOffset, LOS: base.Unix() + aosOffset + 480, MaxElevationDeg: elev, Mode: mode, AmountCents: cents,
		Accepts: []schema.Accept{{Scheme: "exact", Network: "base-sepolia", PayTo: "0xabc", Asset: "USDC", Amount: cents * 10000}},
		Exp:     base.Unix() + 300}
}

// Acceptance 4: with the real client and ANS_LIVE_LLM=1, an anomaly context
// changes the chosen pass versus the greedy plan on a fixture where they should
// differ. Greedy scores by elevation, so it takes the later, higher-elevation
// pass; the anomaly ("prefer any uplink in the next 90 minutes") should take the
// early one. Skipped unless the env var and an API key are both present.
func TestLiveAnomalyChangesPlan(t *testing.T) {
	if os.Getenv("ANS_LIVE_LLM") != "1" {
		t.Skip("set ANS_LIVE_LLM=1 (and ANTHROPIC_API_KEY) to run the live LLM planner test")
	}
	client, ok := NewClient()
	if !ok {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	early := passAt(earlyHost, 600, 30) // 10 minutes out, low elevation
	late := passAt(lateHost, 7200, 70)  // 2 hours out, high elevation
	in := planner.Input{
		Table: []passes.Pass{early, late},
		Stations: []planner.Station{
			{Host: earlyHost, Verified: true, Tier: planner.TierFiduciary},
			{Host: lateHost, Verified: true, Tier: planner.TierFiduciary},
		},
		Quotes: map[string]schema.Quote{
			planner.PriceKey(early): quoteAt(earlyHost, schema.ModeUplink, 600, 30, 500),
			planner.PriceKey(late):  quoteAt(lateHost, schema.ModeUplink, 7200, 70, 500),
		},
		Request: planner.Request{Mode: schema.ModeUplink, GoalS: 300, PointsPerDollar: 1},
	}

	// Greedy takes the later, higher-elevation pass.
	greedy, err := planner.Greedy{}.Plan(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(greedy.Selected) == 0 || greedy.Selected[0].Pass.Host != lateHost {
		t.Fatalf("expected greedy to pick the late high-elevation pass, got %+v", greedy.Selected)
	}

	a := newAuthorityTrust(t, authority.StaticTrust{earlyHost: planner.TierFiduciary, lateHost: planner.TierFiduciary})
	in.Context = "anomaly in progress: prefer any uplink in the next 90 minutes over a higher-elevation pass later"
	p := &Planner{Client: client, Proposer: a, Fallback: planner.Greedy{}}
	llm, err := p.Plan(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if llm.Source != "llm" {
		t.Fatalf("planner fell back to %s; the live model call failed", llm.Source)
	}
	t.Logf("llm explanation: %s", llm.Explanation)
	chose := func(plan planner.Plan, host string) bool {
		for _, c := range plan.Selected {
			if c.Pass.Host == host {
				return true
			}
		}
		return false
	}
	if !chose(llm, earlyHost) {
		t.Errorf("anomaly context did not make the LLM choose the early pass; selected %+v", llm.Selected)
	}
}
