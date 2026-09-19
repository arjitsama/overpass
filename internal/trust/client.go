// Package trust is Overpass's client for the forked agent-trust-discovery index
// (third_party/agent-trust-discovery). It implements authority.TrustSource (read
// a station's truthful trust vector) and auditor.TrustSink (record an audited
// pass as a pass_delivery observation), and it seeds/imports agents. It never
// fabricates identity or integrity: the only observation it writes is the
// behavior signal the auditor genuinely produces (master plan §11, revised).
package trust

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arjitsama/overpass/internal/auditor"
	"github.com/arjitsama/overpass/internal/authority"
	"github.com/arjitsama/overpass/internal/schema"
)

// signalPassDelivery is the behavior signal id our fork adds (PATCHES.md).
const signalPassDelivery = "pass_delivery"

// riskAuditFailure is the risk code the pass_delivery signal emits on any audit
// failure; its presence in riskFactors means at least one audited pass failed.
const riskAuditFailure = "BEHAVIOR_AUDIT_FAILURE"

// HTTPDoer is the subset of *http.Client the client uses.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client talks to one trust index.
type Client struct {
	BaseURL  string            // e.g. http://127.0.0.1:8080
	AdminKey string            // bearer for /v1/internal/* (empty when the index runs with requireKey false)
	HTTP     HTTPDoer          // defaults to a 10s http.Client
	Agents   map[string]string // station host -> agentId; identity for the index
	Now      func() time.Time

	mu    sync.Mutex
	tally map[string]passTally // per-agent running counts for read-modify-write posts
}

type passTally struct {
	booked, delivered, auditFailures int
}

func (c *Client) http() HTTPDoer {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// agentID maps a station host to its index agentId; falls back to the host.
func (c *Client) agentID(host string) string {
	if id, ok := c.Agents[host]; ok {
		return id
	}
	return host
}

// --- wire shapes (subset of the index's DTOs) -------------------------------

type detailResponse struct {
	TrustEvaluation struct {
		TrustVector struct {
			Integrity int `json:"integrity"`
			Identity  int `json:"identity"`
			Solvency  int `json:"solvency"`
			Behavior  int `json:"behavior"`
			Safety    int `json:"safety"`
		} `json:"trustVector"`
		RecommendedProfile string   `json:"recommendedProfile"`
		RiskFactors        []string `json:"riskFactors"`
		Dimensions         []struct {
			Dimension    string `json:"dimension"`
			SignalScores []struct {
				SignalID    string `json:"signalId"`
				RawScore    int    `json:"rawScore"`
				Explanation string `json:"explanation"`
			} `json:"signalScores"`
		} `json:"dimensions"`
	} `json:"trustEvaluation"`
}

// bookedRE and failRE parse the pass_delivery explanation our fork emits
// ("delivered D of B booked; raw R" / "... with F audit failure(s); capped at R").
// The format is a contract with third_party/.../passdelivery.go, locked by a test.
var (
	deliveredRE = regexp.MustCompile(`delivered (\d+) of`)
	bookedRE    = regexp.MustCompile(`of (\d+) booked`)
	failRE      = regexp.MustCompile(`with (\d+) audit failure`)
)

// Evaluate reads a station's truthful trust evaluation (authority.TrustSource).
func (c *Client) Evaluate(ctx context.Context, host string) (authority.TrustEval, error) {
	id := c.agentID(host)
	var d detailResponse
	if err := c.get(ctx, "/v1/ans/registered-agents/"+id, &d); err != nil {
		return authority.TrustEval{}, err
	}
	te := d.TrustEvaluation
	eval := authority.TrustEval{
		Integrity:          te.TrustVector.Integrity,
		Identity:           te.TrustVector.Identity,
		Behavior:           te.TrustVector.Behavior,
		Solvency:           te.TrustVector.Solvency,
		Safety:             te.TrustVector.Safety,
		CertType:           certTypeFromScore(te.TrustVector.Identity),
		RecommendedProfile: te.RecommendedProfile,
	}
	for _, r := range te.RiskFactors {
		if r == riskAuditFailure {
			eval.AuditFailures = true
		}
	}
	// Audited-pass count comes from the pass_delivery signal's explanation.
	for _, dim := range te.Dimensions {
		for _, s := range dim.SignalScores {
			if s.SignalID == signalPassDelivery {
				if m := bookedRE.FindStringSubmatch(s.Explanation); m != nil {
					eval.AuditedPasses, _ = strconv.Atoi(m[1])
				}
			}
		}
	}
	return eval, nil
}

// certTypeFromScore reports the cert tier the index's certtype signal measured,
// inverting the fixed DV=40/OV=70/EV=100 mapping. Displayed, never fabricated.
func certTypeFromScore(identity int) string {
	switch {
	case identity >= 100:
		return "EV"
	case identity >= 70:
		return "OV"
	case identity >= 40:
		return "DV"
	default:
		return "none"
	}
}

// Post records one audited pass as a cumulative pass_delivery observation
// (auditor.TrustSink). It reads the current counts, adds this pass, and writes
// the new totals, so warm-up and live passes accumulate into a real history and
// a survived restart keeps the index as the source of truth.
func (c *Client) Post(ctx context.Context, o auditor.Observation) error {
	id := c.agentID(o.Station)
	cur, err := c.currentTally(ctx, id)
	if err != nil {
		return err
	}
	cur.booked++
	if o.Verdict == schema.Pass {
		cur.delivered++
	}
	cur.auditFailures += o.AuditFailures

	c.mu.Lock()
	if c.tally == nil {
		c.tally = map[string]passTally{}
	}
	c.tally[id] = cur
	c.mu.Unlock()

	value, _ := json.Marshal(map[string]int{
		"booked": cur.booked, "delivered": cur.delivered, "auditFailures": cur.auditFailures})
	return c.ImportObservation(ctx, id, signalPassDelivery, value, o.PassID)
}

// currentTally returns the running counts for an agent, preferring the in-process
// tally and falling back to what the index currently holds (parsed from the
// pass_delivery explanation). A not-yet-seen agent starts at zero.
func (c *Client) currentTally(ctx context.Context, id string) (passTally, error) {
	c.mu.Lock()
	if t, ok := c.tally[id]; ok {
		c.mu.Unlock()
		return t, nil
	}
	c.mu.Unlock()

	var d detailResponse
	if err := c.get(ctx, "/v1/ans/registered-agents/"+id, &d); err != nil {
		return passTally{}, err
	}
	var t passTally
	for _, dim := range d.TrustEvaluation.Dimensions {
		for _, s := range dim.SignalScores {
			if s.SignalID != signalPassDelivery {
				continue
			}
			if m := bookedRE.FindStringSubmatch(s.Explanation); m != nil {
				t.booked, _ = strconv.Atoi(m[1])
			}
			if m := deliveredRE.FindStringSubmatch(s.Explanation); m != nil {
				t.delivered, _ = strconv.Atoi(m[1])
			}
			if m := failRE.FindStringSubmatch(s.Explanation); m != nil {
				t.auditFailures, _ = strconv.Atoi(m[1])
			}
		}
	}
	return t, nil
}

// --- import helpers ----------------------------------------------------------

// Agent is the subset of the index's agent record the seed writes.
type Agent struct {
	AgentID     string `json:"agentId"`
	DNSName     string `json:"dnsName"`
	DisplayName string `json:"displayName"`
	Status      string `json:"status"`
	FirstSeen   string `json:"firstSeen"`
	LastUpdated string `json:"lastUpdated"`
}

// ImportAgents registers agents so observations can be attached to them.
func (c *Client) ImportAgents(ctx context.Context, agents []Agent) error {
	body, _ := json.Marshal(map[string][]Agent{"agents": agents})
	return c.postAdmin(ctx, "/v1/internal/agents/import", body)
}

// Provenance source markers. The index's observation provenance is {aimId,
// evidenceUrl} (no free "source" field), so Overpass records who produced an
// observation in aimId: the auditor for earned history, "overpass-seed" for a
// transparently-marked baseline (master plan §11: mark seed data as seed data).
const (
	SourceAuditor = "overpass-auditor"
	SourceSeed    = "overpass-seed"
)

// ImportObservation writes one observation, tagged with our auditor provenance.
func (c *Client) ImportObservation(ctx context.Context, agentID, signalID string, value json.RawMessage, evidence string) error {
	return c.importObs(ctx, agentID, signalID, value, SourceAuditor, evidence)
}

// SeedObservation writes one baseline observation, marked as seed data so the
// UI and auditors can tell it from an earned measurement.
func (c *Client) SeedObservation(ctx context.Context, agentID, signalID string, value json.RawMessage) error {
	return c.importObs(ctx, agentID, signalID, value, SourceSeed, "")
}

func (c *Client) importObs(ctx context.Context, agentID, signalID string, value json.RawMessage, aimID, evidence string) error {
	obs := map[string]any{
		"agentId":    agentID,
		"signalId":   signalID,
		"observedAt": c.now().UTC().Format(time.RFC3339),
		"value":      value,
		"provenance": map[string]string{"aimId": aimID, "evidenceUrl": evidence},
	}
	body, _ := json.Marshal(map[string][]any{"observations": {obs}})
	return c.postAdmin(ctx, "/v1/internal/observations/import", body)
}

// --- transport ---------------------------------------------------------------

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("trust index: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("trust index GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(raw)))
	}
	return json.Unmarshal(raw, out)
}

// postAdmin posts to an admin route and, on any non-200, surfaces the response
// body (e.g. the 422 AGENT_NOT_FOUND payload) rather than swallowing it.
func (c *Client) postAdmin(ctx context.Context, path string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.AdminKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.AdminKey)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("trust index: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("trust index POST %s: %s: %s", path, resp.Status, strings.TrimSpace(string(raw)))
	}
	return nil
}
