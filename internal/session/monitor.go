// Package session runs a pass: the token loop both sides keep on each other
// (master plan 9.5-9.7) and the station's command relay (9.2-9.4, 9.8).
package session

import (
	"context"
	"sync"
	"time"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/verify"
)

// TokenEvery is how often a session fetches the peer's status token.
const TokenEvery = 30 * time.Second

// Monitor keeps one peer's status token under the Phase 3 policy for the
// length of a session. A fetched token that is not ACTIVE, or a newest token
// older than the policy's MaxAge, cuts the session; a fetch error alone only
// raises a warning event, so a short log or DNS outage does not end a pass.
type Monitor struct {
	Keeper  *verify.Keeper
	AgentID string // the peer whose token is watched
	Self    string // this agent, for events
	Subject string // the booking, for events
	Emit    func(bus.Event)
	OnCut   func(code errs.Code, reason string) // called once, on the first cut

	mu     sync.Mutex
	cut    errs.Code
	reason string
	last   time.Time // when a token was last checked
	good   bool      // a token was ever accepted
}

// Tick fetches the peer's token once and applies the policy. It returns ""
// while the session may continue, a SESSION_CUT code once it is cut, or
// SESSION_REJECTED:token_unavailable if no token has ever been fetched: that
// refuses the command but cuts nothing, so one failed fetch at session open
// is not a denial of contact (master plan 9.6).
func (m *Monitor) Tick(ctx context.Context) errs.Code {
	if code, _ := m.Cut(); code != "" {
		return code
	}
	d := m.Keeper.Check(ctx, m.AgentID)
	m.mu.Lock()
	m.last = m.Keeper.Now()
	if d.Allow {
		m.good = true
	}
	m.mu.Unlock()
	switch {
	case d.Allow:
		if d.Warning != "" {
			m.emit("token_warning", "warn", d.Warning)
		}
		return ""
	case d.Kind == verify.DenyUnavailable:
		m.emit("token_warning", "warn", d.Reason)
		return errs.SessionRejectedToken
	}
	code := errs.SessionCutStale
	if d.Kind == verify.DenyRevoked {
		code = errs.SessionCutRevoked
	}
	m.cutWith(code, d.Reason)
	return code
}

// TickIfDue ticks when the last check is at least every old (or none was
// ever accepted), so a peer's token is checked on each command at most once
// per interval (master plan 9.7), besides the background loop.
func (m *Monitor) TickIfDue(ctx context.Context, every time.Duration) errs.Code {
	m.mu.Lock()
	due := !m.good || m.Keeper.Now().Sub(m.last) >= every
	m.mu.Unlock()
	if due {
		return m.Tick(ctx)
	}
	code, _ := m.Cut()
	return code
}

// CutNow ends the session for another reason (window end, operator).
func (m *Monitor) CutNow(code errs.Code, reason string) { m.cutWith(code, reason) }

func (m *Monitor) cutWith(code errs.Code, reason string) {
	m.mu.Lock()
	if m.cut != "" {
		m.mu.Unlock()
		return
	}
	m.cut, m.reason = code, reason
	m.mu.Unlock()
	m.emit("session_cut", "cut", string(code)+": "+reason)
	if m.OnCut != nil {
		m.OnCut(code, reason)
	}
}

// Cut returns the cut code and reason, or "" while the session is up.
func (m *Monitor) Cut() (errs.Code, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cut, m.reason
}

// Run ticks every interval until ctx ends or the session is cut.
func (m *Monitor) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Tick(ctx)
			if code, _ := m.Cut(); code != "" {
				return
			}
		}
	}
}

func (m *Monitor) emit(kind, result, reason string) {
	if m.Emit != nil {
		m.Emit(bus.Event{Agent: m.Self, Kind: kind, Subject: m.Subject, Result: result, Reason: reason,
			Data: map[string]any{"peer_agent_id": m.AgentID}})
	}
}
