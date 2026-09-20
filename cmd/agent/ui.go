package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

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
}

// uiEvent is one recorded demo event; its Data drives a dashboard section.
type uiEvent struct {
	Kind    string         `json:"kind"`
	Subject string         `json:"subject"`
	Reason  string         `json:"reason"`
	Data    map[string]any `json:"data"`
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
	for _, e := range evs {
		_, _ = a.bus.Publish(bus.Event{Agent: a.cfg.Host, Kind: e.Kind, Subject: e.Subject, Reason: e.Reason, Data: e.Data})
	}
	writeJSON(w, map[string]any{"replayed": len(evs)})
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
	results := make([]map[string]any, 0)
	for _, e := range evs {
		if e.Kind == "battery" {
			a.bus.Publish(bus.Event{Agent: a.cfg.Host, Kind: "battery", Subject: e.Subject, Data: e.Data})
			results = append(results, e.Data)
		}
	}
	writeJSON(w, map[string]any{"results": results})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
