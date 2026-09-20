package main

import (
	"encoding/json"
	"net/http"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/session"
)

// compromiseSwitch is the process-wide demo test control. A station's session
// keeper consults it (cmd/agent/session.go); when armed, the watched peer reads
// as revoked on the next check and the session is cut. It is never armed unless
// an operator arms it via /control/compromise, which is only mounted when the
// config sets test_controls. Real ANS revocation is terminal, so this exists to
// make the session-cut beat repeatable for every judge (see docs/demo-runbook.md).
var compromiseSwitch = session.NewCompromise()

// mountControls adds the demo-only /control/compromise route (test_controls).
func (a *agent) mountControls(mux *http.ServeMux) {
	mux.HandleFunc("/control/compromise", a.controlCompromise)
}

// controlCompromise arms or resets the simulated compromise for all peers.
//
//	POST /control/compromise   {"on": true}    # arm: next check cuts the session
//	POST /control/compromise   {"on": false}   # reset
func (a *agent) controlCompromise(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		errs.Write(w, http.StatusMethodNotAllowed, errs.MethodNotAllowed, "use POST")
		return
	}
	var body struct {
		On bool `json:"on"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
	if body.On {
		compromiseSwitch.Arm("*")
	} else {
		compromiseSwitch.Reset("*")
	}
	a.publishCompromise(body.On)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"compromised": body.On, "test_control": true})
}

func (a *agent) publishCompromise(on bool) {
	result, reason := "armed", "simulated compromise armed (test control)"
	if !on {
		result, reason = "reset", "simulated compromise reset (test control)"
	}
	_, _ = a.bus.Publish(bus.Event{Agent: a.cfg.Host, Kind: "simulate_compromise", Subject: a.cfg.Host,
		Result: result, Reason: reason})
}
