package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arjitsama/overpass/internal/battery"
	"github.com/arjitsama/overpass/internal/errs"
)

func TestRecordCountsAndStamps(t *testing.T) {
	results := []battery.Result{
		{Name: "tamper_mandate", Verdict: battery.Blocked, Expected: errs.MandateRejectedSignature, Observed: errs.MandateRejectedSignature},
		{Name: "replay_booking", Verdict: battery.Blocked, Expected: errs.DPoPRejectedReplay, Observed: errs.DPoPRejectedReplay},
		{Name: "wrong_dpop_key_attack", Verdict: battery.Inconclusive, Detail: "needs a second registered Ops identity"},
	}
	target := Target{StationHost: "gs-blacksburg.example", StationANS: "ans://v0.1.0.gs-blacksburg.example"}
	rec := NewRecord(target, results, time.Date(2026, 9, 20, 5, 30, 0, 0, time.UTC))

	if rec.RanAt != "2026-09-20T05:30:00Z" {
		t.Errorf("ran_at = %q", rec.RanAt)
	}
	// An attack that could not run is never counted as blocked.
	if rec.Blocked != 2 || rec.Inconclusive != 1 || rec.Vulnerable != 0 || rec.Total != 3 {
		t.Errorf("counts = %+v, want 2 blocked / 1 inconclusive / 3 total", rec)
	}

	md := rec.Markdown()
	for _, want := range []string{"Last live run: 2026-09-20T05:30:00Z", "gs-blacksburg.example",
		"2 of 3 blocked", "1 inconclusive", "wrong_dpop_key_attack"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestWriteRecordProducesBothFiles(t *testing.T) {
	base := filepath.Join(t.TempDir(), "battery-live")
	results := []battery.Result{{Name: "tamper_mandate", Verdict: battery.Blocked}}
	if err := writeRecord(base, Target{StationHost: "gs-blacksburg.example"}, results, time.Now()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(base + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var got Record
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("record is not valid JSON: %v", err)
	}
	if got.Total != 1 || got.Target.StationHost != "gs-blacksburg.example" {
		t.Errorf("record = %+v", got)
	}
	if _, err := os.Stat(base + ".md"); err != nil {
		t.Errorf("markdown record not written: %v", err)
	}
}
