package main

import (
	"testing"

	"github.com/arjitsama/overpass/internal/verify"
)

func TestDaneOutcomeByName(t *testing.T) {
	cases := map[string]string{
		"DANEVerified":  "Verified",
		"DANESkipped":   "Skipped",
		"DANENoRecords": "NoRecords",
		"DANEMismatch":  "Mismatch",
		"":              "Unknown",
	}
	for raw, want := range cases {
		res := verify.Result{Checks: []verify.Check{
			{Name: verify.CheckBadge, Verdict: verify.Pass},
			{Name: verify.CheckTLSA, Detail: map[string]string{"outcome": raw}},
		}}
		if got := daneOutcome(res); got != want {
			t.Errorf("outcome %q -> %q, want %q", raw, got, want)
		}
	}
	// No tlsa check at all -> Unknown.
	if got := daneOutcome(verify.Result{}); got != "Unknown" {
		t.Errorf("no tlsa check -> %q, want Unknown", got)
	}
}
