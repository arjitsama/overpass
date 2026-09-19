package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/arjitsama/overpass/internal/battery"
)

func TestGate(t *testing.T) {
	allBlocked := []battery.Result{{Verdict: battery.Blocked}, {Verdict: battery.Blocked}}
	oneVuln := []battery.Result{{Verdict: battery.Blocked}, {Verdict: battery.Vulnerable}}
	oneInconc := []battery.Result{{Verdict: battery.Blocked}, {Verdict: battery.Inconclusive}}
	cases := []struct {
		name       string
		results    []battery.Result
		expectVuln bool
		want       int
	}{
		{"honest all blocked", allBlocked, false, 0},
		{"honest one vulnerable gates", oneVuln, false, 1},
		{"honest one inconclusive gates", oneInconc, false, 1},
		{"rogue one vulnerable ok", oneVuln, true, 0},
		{"rogue none vulnerable gates", allBlocked, true, 1},
	}
	for _, c := range cases {
		if got := gate(c.results, c.expectVuln); got != c.want {
			t.Errorf("%s: gate = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestRender(t *testing.T) {
	var out bytes.Buffer
	render(&out, []battery.Result{
		{Name: "tamper_mandate", Verdict: battery.Blocked, Observed: "MANDATE_REJECTED:signature"},
		{Name: "replay_booking", Verdict: battery.Vulnerable, Observed: "", Detail: "accepted"},
	})
	s := out.String()
	if !strings.Contains(s, "tamper_mandate") || !strings.Contains(s, "1 BLOCKED, 1 VULNERABLE, 0 INCONCLUSIVE") {
		t.Fatalf("render:\n%s", s)
	}
}

func TestNamesCoverRunOrder(t *testing.T) {
	if len(battery.Names()) != 23 {
		t.Fatalf("expected 23 attacks, got %d: %v", len(battery.Names()), battery.Names())
	}
}
