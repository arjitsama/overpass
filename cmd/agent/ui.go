package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"strings"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/webmesh"
	"github.com/arjitsama/overpass/web"
)

// mountUI adds the dashboard and its three POST routes to the mux. It is called
// only for the Ops role (master plan §12: the dashboard is served by Ops).
func (a *agent) mountUI(mux *http.ServeMux) {
	notFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		errs.Write(w, http.StatusNotFound, errs.NotFound, "no route "+r.URL.Path)
	})
	mux.Handle("/ui/", http.StripPrefix("/ui", web.Handler(notFound)))
	mux.HandleFunc("/ui/run-demo-pass", a.uiRunDemoPass)
	mux.HandleFunc("/ui/verify-station", a.uiVerifyStation)
	mux.HandleFunc("/ui/run-battery", a.uiRunBattery)
	mux.HandleFunc("/ui/simulate-compromise", a.uiSimulateCompromise)
	mux.HandleFunc("/ui/agents", a.uiAgentsSnapshot)
	mux.HandleFunc("/ui/refresh-agents", a.uiRefreshAgents)
	mux.HandleFunc("/ui/battery-live", a.uiBatteryLive)
	mux.HandleFunc("/ui/fraud-redteam", a.uiFraudRedteam)
}

// uiSimulateCompromise is the dashboard's repeatable, resettable session-cut
// test control. It arms/resets the process compromise switch (so a live station
// session cuts) AND streams the cut + replan to the dashboard so the beat is
// visible for every judge. It is a clearly-labelled TEST control, not a real
// revocation (which is terminal); see docs/demo-runbook.md.
func (a *agent) uiSimulateCompromise(w http.ResponseWriter, r *http.Request) {
	if !uiPost(w, r) {
		return
	}
	var body struct {
		On      bool   `json:"on"`
		Station string `json:"station"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	station := body.Station
	if station == "" {
		station = "gs-blacksburg." + hostSuffix(a.cfg.Host)
	}
	if body.On {
		compromiseSwitch.Arm("*")
		a.bus.Publish(bus.Event{Agent: a.cfg.Host, Kind: "session_cut", Subject: station,
			Result: "cut", Reason: "SESSION_CUT:revoked: simulated compromise (test control)",
			Data: map[string]any{"station": station, "test_control": true}})
		a.bus.Publish(bus.Event{Agent: a.cfg.Host, Kind: "replan", Subject: station,
			Result: "ok", Reason: "removed " + station + " after the cut; re-booking elsewhere",
			Data: map[string]any{"removed": station, "test_control": true}})
	} else {
		compromiseSwitch.Reset("*")
		a.bus.Publish(bus.Event{Agent: a.cfg.Host, Kind: "simulate_compromise", Subject: station,
			Result: "reset", Reason: "simulated compromise reset (test control)",
			Data: map[string]any{"station": station, "test_control": true}})
	}
	writeJSON(w, map[string]any{"compromised": body.On, "station": station, "test_control": true})
}

// hostSuffix returns the part of an ops host after the first label, e.g.
// "ops.blacksburgbytes.club" -> "blacksburgbytes.club"; falls back to "localhost".
func hostSuffix(host string) string {
	if i := strings.IndexByte(host, '.'); i >= 0 {
		return host[i+1:]
	}
	return "localhost"
}

// uiEvent is one recorded demo event; its Data drives a dashboard section.
type uiEvent struct {
	Kind    string         `json:"kind"`
	Subject string         `json:"subject"`
	Reason  string         `json:"reason"`
	Data    map[string]any `json:"data"`
}

// recordedAt returns the recording date the fixture declares in its leading
// "recording" event, so every replayed row can be stamped with it.
func recordedAt(evs []uiEvent) string {
	for _, e := range evs {
		if e.Kind == "recording" && e.Data != nil {
			if s, ok := e.Data["recorded_at"].(string); ok {
				return s
			}
		}
	}
	return ""
}

// markRecorded stamps one replayed event's data, so the dashboard can label it
// and no recorded row can be mistaken for a live one.
func markRecorded(d map[string]any, when string) map[string]any {
	out := make(map[string]any, len(d)+2)
	for k, v := range d {
		out[k] = v
	}
	out["recorded"] = true
	if when != "" {
		out["recorded_at"] = when
	}
	return out
}

func uiPost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		errs.Write(w, http.StatusMethodNotAllowed, errs.MethodNotAllowed, "use POST")
		return false
	}
	return true
}

// uiRunDemoPass replays the recorded demo event stream onto the bus so the
// dashboard lights up from recorded data. It is a demo driver, not a live pass
// (a live pass is the ops/authority/station/spacecraft flow); see the status doc.
func (a *agent) uiRunDemoPass(w http.ResponseWriter, r *http.Request) {
	if !uiPost(w, r) {
		return
	}
	var evs []uiEvent
	if err := json.Unmarshal(web.DemoEventsJSON(), &evs); err != nil {
		errs.Write(w, http.StatusInternalServerError, errs.Internal, "demo events unreadable")
		return
	}
	when := recordedAt(evs)
	for _, e := range evs {
		_, _ = a.bus.Publish(bus.Event{Agent: a.cfg.Host, Kind: e.Kind, Subject: e.Subject, Reason: e.Reason,
			Data: markRecorded(e.Data, when)})
	}
	writeJSON(w, map[string]any{"replayed": len(evs), "recorded_at": when})
}

// uiVerifyStation calls GoDaddy's agent (the Phase 3 webmesh client) to verify a
// station, publishes the verdict as an event, and returns it. Host comes from the
// request body or the configured default.
func (a *agent) uiVerifyStation(w http.ResponseWriter, r *http.Request) {
	if !uiPost(w, r) {
		return
	}
	var body struct {
		Host string `json:"host"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	host := body.Host
	if host == "" {
		host = a.cfg.UI.VerifyHost
	}
	if a.cfg.UI.WebmeshURL == "" || host == "" {
		errs.Write(w, http.StatusBadRequest, errs.BadRequest, "no webmesh_url or verify host configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	verdict, err := webmesh.New(a.cfg.UI.WebmeshURL).Verify(ctx, host)
	if err != nil {
		a.bus.Publish(bus.Event{Agent: a.cfg.Host, Kind: "verification", Subject: host, Result: "error", Reason: err.Error(),
			Data: map[string]any{"host": host}})
		errs.Write(w, http.StatusBadGateway, errs.Unavailable, "webmesh verify failed: "+err.Error())
		return
	}
	a.bus.Publish(bus.Event{Agent: a.cfg.Host, Kind: "verification", Subject: host, Result: "ok",
		Data: map[string]any{"host": host, "verdict": verdict}})
	writeJSON(w, map[string]any{"host": host, "verdict": verdict})
}

// uiRunBattery surfaces the recorded battery results on the bus and returns them
// for the dashboard to render (the client then moves focus to the results
// heading). A live run is cmd/battery with its own credentials; see the status doc.
func (a *agent) uiRunBattery(w http.ResponseWriter, r *http.Request) {
	if !uiPost(w, r) {
		return
	}
	var evs []uiEvent
	if err := json.Unmarshal(web.DemoEventsJSON(), &evs); err != nil {
		errs.Write(w, http.StatusInternalServerError, errs.Internal, "demo events unreadable")
		return
	}
	when := recordedAt(evs)
	results := make([]map[string]any, 0)
	for _, e := range evs {
		if e.Kind == "battery" {
			d := markRecorded(e.Data, when)
			a.bus.Publish(bus.Event{Agent: a.cfg.Host, Kind: "battery", Subject: e.Subject, Data: d})
			results = append(results, d)
		}
	}
	writeJSON(w, map[string]any{"results": results, "recorded_at": when})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
