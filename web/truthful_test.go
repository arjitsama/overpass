package web

import (
	"encoding/json"
	"strings"
	"testing"
)

// The dashboard may show only what is true. The recorded fixture is replayed
// into the live page, so anything it carries is something a judge will read as
// real: hosts we do not own, a trust tier or an identity score would all be a
// claim we cannot support. Production runs no trust index (deploy/prod/
// authority-tonight.yaml sets trust_index.url to ""), so no score may appear.
func TestFixtureIsTruthful(t *testing.T) {
	raw := string(DemoEventsJSON())
	for _, bad := range []string{"example.com", "FIDUCIARY", "READ_ONLY", "TRANSACTIONAL", `"identity":`, `"tier":`} {
		if strings.Contains(raw, bad) {
			t.Errorf("recorded fixture contains %q; it is replayed as if shown live", bad)
		}
	}
	if !strings.Contains(raw, "blacksburgbytes.club") {
		t.Error("recorded fixture must use the real production hostnames")
	}
}

// Every replayed row is labelled with the date it was recorded, so recorded
// data can never be mistaken for a live run.
func TestFixtureDeclaresRecordingDate(t *testing.T) {
	var evs []struct {
		Kind string         `json:"kind"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(DemoEventsJSON(), &evs); err != nil {
		t.Fatalf("fixture is not valid JSON: %v", err)
	}
	if len(evs) == 0 || evs[0].Kind != "recording" {
		t.Fatal("fixture must open with a recording event carrying recorded_at")
	}
	when, _ := evs[0].Data["recorded_at"].(string)
	if when == "" {
		t.Fatal("recording event has no recorded_at")
	}
}
