package main

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/verify"
)

// Bounds for the live agent feed. One verification touches DNS, the
// transparency log and the peer itself; it never writes to ANS.
const (
	agentVerifyTimeout = 20 * time.Second
	agentRefreshFloor  = time.Minute // the soonest an on-demand refresh may re-run
)

// uiCheck is one verification check as the dashboard shows it: always by name,
// so a softened check (card_hash: warn) can never read as a bare pass.
type uiCheck struct {
	Name    string `json:"name"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason,omitempty"`
}

// uiAgent is one row of the dashboard's agents table. Every field comes from a
// live VerifyPeer result; there are no scores because production runs no trust
// index (see uiAgents.TrustIndex).
type uiAgent struct {
	Host       string    `json:"host"`
	Role       string    `json:"role"`
	Deployed   bool      `json:"deployed"`
	Verified   bool      `json:"verified"`
	Verdict    string    `json:"verdict"`
	Reason     string    `json:"reason,omitempty"`
	ANSName    string    `json:"ans_name,omitempty"`
	AgentID    string    `json:"agent_id,omitempty"`
	DANE       string    `json:"dane,omitempty"`
	CardSHA256 string    `json:"card_sha256,omitempty"`
	Checks     []uiCheck `json:"checks,omitempty"`
	CheckedAt  string    `json:"checked_at,omitempty"`
}

// uiAgents is the whole snapshot the dashboard renders.
type uiAgents struct {
	CheckedAt     string    `json:"checked_at,omitempty"`
	RefreshEveryS int       `json:"refresh_every_s"`
	TrustIndex    string    `json:"trust_index"`
	AccessBasis   string    `json:"access_basis"`
	Agents        []uiAgent `json:"agents"`
}

// agentsCache holds the last completed snapshot. A refresh runs at most once at
// a time; readers always get the previous snapshot rather than blocking.
type agentsCache struct {
	mu        sync.RWMutex
	snapshot  uiAgents
	running   bool
	lastStart time.Time
}

func (c *agentsCache) get() uiAgents {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshot
}

// begin claims the right to run a refresh. minGap rejects an on-demand refresh
// that follows too closely on the last one.
func (c *agentsCache) begin(now time.Time, minGap time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running || (minGap > 0 && !c.lastStart.IsZero() && now.Sub(c.lastStart) < minGap) {
		return false
	}
	c.running, c.lastStart = true, now
	return true
}

func (c *agentsCache) finish(s uiAgents) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running, c.snapshot = false, s
}

// refreshAgents verifies every configured agent that is deployed and replaces
// the snapshot. Undeployed agents are listed and labelled, never verified: the
// dashboard must not imply an agent is running when it is not.
func (a *agent) refreshAgents(ctx context.Context) uiAgents {
	ui := a.cfg.UI
	out := uiAgents{
		RefreshEveryS: int(ui.RefreshEvery / time.Second),
		TrustIndex:    ui.TrustIndexNote,
		AccessBasis:   ui.AccessBasis,
		Agents:        make([]uiAgent, 0, len(ui.Agents)),
	}
	for _, want := range ui.Agents {
		row := uiAgent{Host: want.Host, Role: want.Role, Deployed: want.IsDeployed()}
		if !row.Deployed {
			row.Verdict = "not deployed"
			out.Agents = append(out.Agents, row)
			continue
		}
		if a.role.peers == nil {
			row.Verdict = "unavailable"
			row.Reason = "no verifier configured"
			out.Agents = append(out.Agents, row)
			continue
		}
		vctx, cancel := context.WithTimeout(ctx, agentVerifyTimeout)
		res := a.role.peers.VerifyPeer(vctx, want.Host)
		cancel()
		out.Agents = append(out.Agents, agentRow(row, res, a.now()))
	}
	out.CheckedAt = a.now().UTC().Format(time.RFC3339)
	return out
}

// agentRow renders one VerifyPeer result the way `bin/agent --verify` prints
// it: the verdict, the DANE outcome by name, and every check with its name.
func agentRow(row uiAgent, res verify.Result, now time.Time) uiAgent {
	row.Verified = res.OK()
	row.Verdict = "FAILED"
	if row.Verified {
		row.Verdict = "VERIFIED"
	}
	row.ANSName, row.AgentID = res.ANSName, res.AgentID
	row.DANE = daneOutcome(res)
	row.CheckedAt = now.UTC().Format(time.RFC3339)
	for _, c := range res.Checks {
		row.Checks = append(row.Checks, uiCheck{Name: c.Name, Verdict: c.Verdict, Reason: c.Reason})
		if c.Name == verify.CheckCardHash && c.Detail != nil {
			row.CardSHA256 = c.Detail["card_sha256"]
		}
		if c.Verdict == verify.Fail && row.Reason == "" {
			row.Reason = c.Name + ": " + c.Reason
		}
	}
	return row
}

// startAgentPoller verifies once at startup and then on a ticker. It is started
// after the listener is up, because ops verifies itself through its own public
// name (nginx routes the SNI straight back to this process).
func (a *agent) startAgentPoller(ctx context.Context) {
	if len(a.cfg.UI.Agents) == 0 {
		return
	}
	go func() {
		a.runAgentRefresh(ctx, 0)
		t := time.NewTicker(a.cfg.UI.RefreshEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.runAgentRefresh(ctx, 0)
			}
		}
	}()
}

// runAgentRefresh refreshes the snapshot and publishes it, so dashboards that
// are already open update without polling. It reports whether it ran.
func (a *agent) runAgentRefresh(ctx context.Context, minGap time.Duration) bool {
	if !a.agents.begin(a.now(), minGap) {
		return false
	}
	snap := a.refreshAgents(ctx)
	a.agents.finish(snap)
	_, _ = a.bus.Publish(bus.Event{Agent: a.cfg.Host, Kind: "agents", Result: "ok",
		Data: map[string]any{
			"checked_at": snap.CheckedAt, "trust_index": snap.TrustIndex,
			"access_basis": snap.AccessBasis, "agents": snap.Agents,
		}})
	return true
}

// uiAgentsSnapshot serves the last completed verification snapshot.
func (a *agent) uiAgentsSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		errs.Write(w, http.StatusMethodNotAllowed, errs.MethodNotAllowed, "use GET")
		return
	}
	snap := a.agents.get()
	if snap.AccessBasis == "" {
		// Nothing has been verified yet: still answer with the honest frame so
		// the page never invents a trust index or an access basis.
		snap = uiAgents{
			RefreshEveryS: int(a.cfg.UI.RefreshEvery / time.Second),
			TrustIndex:    a.cfg.UI.TrustIndexNote,
			AccessBasis:   a.cfg.UI.AccessBasis,
			Agents:        []uiAgent{},
		}
	}
	writeJSON(w, snap)
}

// uiRefreshAgents re-runs verification on demand, rate-limited so a held-down
// button cannot hammer the transparency log.
func (a *agent) uiRefreshAgents(w http.ResponseWriter, r *http.Request) {
	if !uiPost(w, r) {
		return
	}
	if !a.runAgentRefresh(r.Context(), agentRefreshFloor) {
		errs.Write(w, http.StatusServiceUnavailable, errs.Unavailable,
			"a verification run is already in progress or just finished")
		return
	}
	writeJSON(w, a.agents.get())
}
