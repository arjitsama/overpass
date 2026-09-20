package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/config"
)

func opsAgent() *agent {
	cfg := config.Config{Role: "ops", Host: "ops.example"}
	cfg.ApplyDefaults()
	return &agent{
		cfg: cfg,
		bus: bus.New(bus.DefaultBacklog, bus.DefaultMaxSubs),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		now: time.Now,
	}
}

func TestUIRunDemoPass(t *testing.T) {
	a := opsAgent()
	defer a.bus.Close()
	rec := httptest.NewRecorder()
	a.uiRunDemoPass(rec, httptest.NewRequest(http.MethodPost, "/ui/run-demo-pass", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Replayed   int    `json:"replayed"`
		RecordedAt string `json:"recorded_at"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Replayed < 5 {
		t.Errorf("replayed = %d, want the recorded stream", out.Replayed)
	}
	if out.RecordedAt == "" {
		t.Error("replay must report when the stream was recorded")
	}
	// GET is refused.
	rec = httptest.NewRecorder()
	a.uiRunDemoPass(rec, httptest.NewRequest(http.MethodGet, "/ui/run-demo-pass", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status %d, want 405", rec.Code)
	}
}

func TestUIRunBatteryReturnsResults(t *testing.T) {
	a := opsAgent()
	defer a.bus.Close()
	rec := httptest.NewRecorder()
	a.uiRunBattery(rec, httptest.NewRequest(http.MethodPost, "/ui/run-battery", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out struct {
		Results []map[string]any `json:"results"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Results) < 2 {
		t.Errorf("results = %d, want the recorded battery rows", len(out.Results))
	}
	// Every replayed row is stamped, so the page can label it and no recorded
	// result can be read as a live run.
	for i, r := range out.Results {
		if r["recorded"] != true || r["recorded_at"] == "" {
			t.Errorf("result %d is not stamped as recorded: %v", i, r)
		}
	}
}

func TestUIVerifyStationNeedsConfig(t *testing.T) {
	a := opsAgent() // no webmesh_url / verify_host configured
	defer a.bus.Close()
	rec := httptest.NewRecorder()
	a.uiVerifyStation(rec, httptest.NewRequest(http.MethodPost, "/ui/verify-station", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400 when unconfigured", rec.Code)
	}
}
