package planner

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/passes"
	"github.com/arjitsama/overpass/internal/schema"
)

// realTable is the golden 24 h table (27844 over the three sites), plus a
// lookalike station that mirrors Svalbard's passes.
func realTable(t *testing.T) []passes.Pass {
	t.Helper()
	tle, err := passes.LoadTLE("../passes/testdata/27844.tle")
	if err != nil {
		t.Fatal(err)
	}
	table, err := passes.Table(tle, passes.DefaultSites, time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range table {
		if p.Host == "gs-svalbard" {
			q := p
			q.Station, q.Host = "Svalbard (lookalike)", "gs-svalbard-eu"
			table = append(table, q)
		}
	}
	return table
}

// Prices: 20 cents per pass-second, lookalike undercuts by half.
func prices(table []passes.Pass) map[string]int64 {
	out := map[string]int64{}
	for _, p := range table {
		c := 20 * p.DurationS
		if p.Host == "gs-svalbard-eu" {
			c /= 2
		}
		out[PriceKey(p)] = c
	}
	return out
}

func stations() []Station {
	return []Station{
		{Host: "gs-blacksburg", Verified: true, Tier: TierFiduciary},
		{Host: "gs-svalbard", Verified: true, Tier: TierFiduciary, PriorityBonus: 5},
		{Host: "gs-awarua", Verified: false, VerifyReason: "no _ans-badge TXT record", Tier: TierFiduciary},
		{Host: "gs-svalbard-eu", Verified: true, Tier: TierReadOnly},
	}
}

func uplink() Request {
	return Request{Mode: schema.ModeUplink, GoalS: 40 * 60, PointsPerDollar: 1}
}

// Acceptance 3: no overlaps; never an unverified or under-tier station; the
// same plan whatever the input order.
func TestPlannerRules(t *testing.T) {
	table := realTable(t)
	plan := mustSchedule(t, table, stations(), prices(table), uplink())
	if len(plan.Selected) == 0 || !plan.GoalMet {
		t.Fatalf("plan %+v", plan)
	}
	for i, a := range plan.Selected {
		if a.Pass.Host == "gs-awarua" || a.Pass.Host == "gs-svalbard-eu" {
			t.Errorf("selected ineligible %s", a.Pass.Host)
		}
		for _, b := range plan.Selected[i+1:] {
			if a.Pass.Overlaps(b.Pass) {
				t.Errorf("overlap %+v %+v", a.Pass, b.Pass)
			}
		}
	}
	reasons := map[string]string{}
	for _, c := range plan.Others {
		reasons[c.Pass.Host] = c.Reason
	}
	if !strings.Contains(reasons["gs-awarua"], "not verified") || !strings.Contains(reasons["gs-svalbard-eu"], "tier READ_ONLY does not allow uplink") {
		t.Fatalf("reasons %v", reasons)
	}
	for _, c := range plan.Others {
		if c.Code == "" {
			t.Fatalf("unselected pass without a code: %+v", c)
		}
		if c.Pass.Host == "gs-svalbard-eu" && c.Code != errs.PolicyRefusedTier {
			t.Fatalf("lookalike code %s", c.Code)
		}
	}
	if len(plan.Selected)+len(plan.Others) != len(table) {
		t.Fatalf("passes lost: %d + %d != %d", len(plan.Selected), len(plan.Others), len(table))
	}

	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20; i++ {
		shuffled := append([]passes.Pass(nil), table...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		st := stations()
		rng.Shuffle(len(st), func(a, b int) { st[a], st[b] = st[b], st[a] })
		again := mustSchedule(t, shuffled, st, prices(table), uplink())
		if !reflect.DeepEqual(again.Selected, plan.Selected) || !reflect.DeepEqual(again.Others, plan.Others) {
			t.Fatal("plan depends on input order")
		}
	}
}

// Downlink admits TRANSACTIONAL; the cheaper lookalike still cannot take uplink.
func TestTierByMode(t *testing.T) {
	table := realTable(t)
	st := stations()
	st[3].Tier = TierTransactional
	down := mustSchedule(t, table, st, prices(table), Request{Mode: schema.ModeDownlink, GoalS: 1 << 40, PointsPerDollar: 1})
	up := mustSchedule(t, table, st, prices(table), Request{Mode: schema.ModeUplink, GoalS: 1 << 40, PointsPerDollar: 1})
	has := func(p Plan, host string) bool {
		for _, c := range p.Selected {
			if c.Pass.Host == host {
				return true
			}
		}
		return false
	}
	if !has(down, "gs-svalbard-eu") || has(up, "gs-svalbard-eu") {
		t.Fatalf("downlink lookalike %v, uplink lookalike %v", has(down, "gs-svalbard-eu"), has(up, "gs-svalbard-eu"))
	}
}

func TestScoreFormula(t *testing.T) {
	p := passes.Pass{Host: "gs-x", AOS: 1000, LOS: 1600, DurationS: 600, MaxElevationDeg: 45}
	plan := mustSchedule(t, []passes.Pass{p}, []Station{{Host: "gs-x", Verified: true, Tier: TierFiduciary, PriorityBonus: 3}},
		map[string]int64{PriceKey(p): 1250}, Request{Mode: schema.ModeUplink, GoalS: 1, PointsPerDollar: 2})
	if len(plan.Selected) != 1 || plan.Selected[0].Score != 100*45+100*3-2*1250 {
		t.Fatalf("%+v", plan.Selected)
	}
	unpriced := mustSchedule(t, []passes.Pass{p}, []Station{{Host: "gs-x", Verified: true, Tier: TierFiduciary}}, nil, uplink())
	if len(unpriced.Selected) != 0 || unpriced.Others[0].Code != errs.PlanNoQuote {
		t.Fatalf("%+v", unpriced)
	}
	for _, bad := range []int64{-1, MaxPriceCents + 1} {
		hostile := mustSchedule(t, []passes.Pass{p}, []Station{{Host: "gs-x", Verified: true, Tier: TierFiduciary}},
			map[string]int64{PriceKey(p): bad}, Request{Mode: schema.ModeUplink, GoalS: 1, PointsPerDollar: MaxPointsPerDollar})
		if len(hostile.Selected) != 0 || hostile.Others[0].Code != errs.PlanBadPrice {
			t.Fatalf("price %d: %+v", bad, hostile)
		}
	}
}

func mustSchedule(t *testing.T, table []passes.Pass, st []Station, pr map[string]int64, req Request) Plan {
	t.Helper()
	p, err := Schedule(table, st, pr, req)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScheduleRejectsBadInput(t *testing.T) {
	ok := []Station{{Host: "gs-x", Verified: true, Tier: TierFiduciary}}
	for name, c := range map[string]struct {
		st  []Station
		req Request
	}{
		"bogus mode":            {ok, Request{Mode: "bogus", GoalS: 1}},
		"zero goal":             {ok, Request{Mode: schema.ModeUplink}},
		"negative price weight": {ok, Request{Mode: schema.ModeUplink, GoalS: 1, PointsPerDollar: -1}},
		"duplicate host":        {append(ok, ok[0]), uplink()},
		"empty host":            {[]Station{{}}, uplink()},
	} {
		if _, err := Schedule(nil, c.st, nil, c.req); !errs.Is(err, errs.BadRequest) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := Replan(Plan{}, "x", nil); err == nil {
		t.Error("replan of zero plan accepted")
	}
}

// Acceptance 4: Replan removes a station and emits a diff.
func TestReplanDiff(t *testing.T) {
	table := realTable(t)
	plan := mustSchedule(t, table, stations(), prices(table), uplink())
	var sv []passes.Pass
	for _, c := range plan.Selected {
		if c.Pass.Host == "gs-svalbard" {
			sv = append(sv, c.Pass)
		}
	}
	if len(sv) == 0 {
		t.Fatal("test needs Svalbard in the first plan")
	}
	var events []bus.Event
	next, d, err := Replan(plan, "gs-svalbard", func(e bus.Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range next.Selected {
		if c.Pass.Host == "gs-svalbard" {
			t.Fatalf("removed station still selected: %+v", c.Pass)
		}
	}
	if len(d.Dropped) != len(sv) || d.RemovedHost != "gs-svalbard" || d.BeforeS != plan.ContactS || d.AfterS != next.ContactS {
		t.Fatalf("diff %+v", d)
	}
	if len(d.Kept)+len(d.Dropped) != len(plan.Selected) || len(d.Kept)+len(d.Added) != len(next.Selected) {
		t.Fatalf("diff does not add up: %+v", d)
	}
	if len(events) != 1 || events[0].Kind != "replan" || events[0].Subject != "gs-svalbard" ||
		events[0].Data["before"] == nil || events[0].Data["after"] == nil {
		t.Fatalf("events %+v", events)
	}
	for _, c := range next.Others {
		if c.Pass.Host == "gs-svalbard" && c.Code != errs.PlanExcluded {
			t.Fatalf("reason %q", c.Reason)
		}
	}
	// A second replan keeps the first exclusion.
	third, _, _ := Replan(next, "gs-blacksburg", nil)
	for _, c := range third.Selected {
		if c.Pass.Host == "gs-svalbard" || c.Pass.Host == "gs-blacksburg" {
			t.Fatalf("excluded station back: %+v", c.Pass)
		}
	}
}
