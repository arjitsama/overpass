// Package authority implements the mission authority's issue_mandate skill
// (master plan 8.3, 8.4): verify the caller is an Ops agent, verify the
// station, read its trust tier, apply the flight rules, sign the mandate.
// Every refusal is POLICY_REFUSED:<rule>.
package authority

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	ansverify "github.com/agentnameservice/ans-sdk-go/verify"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/jose"
	"github.com/arjitsama/overpass/internal/planner"
	"github.com/arjitsama/overpass/internal/schema"
	"github.com/arjitsama/overpass/internal/station"
	"github.com/arjitsama/overpass/internal/store"
	"github.com/arjitsama/overpass/internal/verify"
)

// PeerVerifier is verify.Verifier's VerifyPeer.
type PeerVerifier interface {
	VerifyPeer(ctx context.Context, host string) verify.Result
}

// TrustEval is the truthful trust vector Overpass reads from the trust index for
// one station, plus the two derived facts the flight rules gate on: whether any
// audited pass failed, and how many passes the auditor has recorded. Overpass
// computes its own access tiers from these (OverpassTier); it does not gate on
// the index's own recommendedProfile, which is displayed as the index's verdict.
type TrustEval struct {
	Integrity     int
	Identity      int
	Behavior      int
	Solvency      int
	Safety        int
	AuditFailures bool   // a BEHAVIOR_AUDIT_FAILURE risk factor is present
	AuditedPasses int    // booked passes recorded by pass_delivery
	CertType      string // "none" | "DV" | "OV" | "EV", as measured; displayed, not fabricated

	// RecommendedProfile is the index's OWN tier verdict. Overpass does not gate
	// on it (it gates on the vector above via OverpassTier); it is surfaced only
	// to display the index's verdict beside Overpass's own (master plan §11).
	RecommendedProfile string
}

// TrustSource returns a station's truthful trust evaluation from the index.
type TrustSource interface {
	Evaluate(ctx context.Context, host string) (TrustEval, error)
}

// OverpassTier maps a truthful trust evaluation to an Overpass access tier using
// the flight rules' thresholds (master plan §11, revised: Overpass gates on the
// real vector, not the index's recommendedProfile). Uplink (FIDUCIARY) demands
// integrity, behavior, a minimum audited-pass count, no audit failures, and a
// minimum cert tier; downlink (TRANSACTIONAL) demands integrity and no audit
// failures, with a cold-start behavior of 0 allowed as probation so a new
// station can earn history; everything verified else is READ_ONLY availability.
func OverpassTier(e TrustEval, r config.FlightRules) string {
	if !e.AuditFailures &&
		e.Integrity >= r.UplinkIntegrity() &&
		e.Behavior >= r.UplinkBehavior() &&
		e.AuditedPasses >= r.UplinkPasses() &&
		certRank(e.CertType) >= certRank(r.CertTypeFloor()) {
		return planner.TierFiduciary
	}
	if !e.AuditFailures && e.Integrity >= r.DownlinkIntegrity() {
		return planner.TierTransactional
	}
	return planner.TierReadOnly
}

// certRank orders certificate tiers; an unknown/empty value ranks below DV.
func certRank(t string) int {
	switch t {
	case "EV":
		return 3
	case "OV":
		return 2
	case "DV":
		return 1
	default:
		return 0
	}
}

// StaticTrust is a configured host -> tier map used for tests and for a local
// run without the index. It implements TrustSource by returning a synthetic
// evaluation that OverpassTier maps back to the configured tier.
type StaticTrust map[string]string

// Evaluate implements TrustSource; an unknown host is READ_ONLY.
func (s StaticTrust) Evaluate(_ context.Context, host string) (TrustEval, error) {
	switch s[host] {
	case planner.TierFiduciary:
		return TrustEval{Integrity: 100, Behavior: 100, AuditedPasses: 1 << 20, CertType: "EV"}, nil
	case planner.TierTransactional:
		return TrustEval{Integrity: 100, CertType: "DV"}, nil
	default:
		return TrustEval{}, nil
	}
}

// Authority holds what issue_mandate needs.
type Authority struct {
	ANSName string
	Key     *ecdsa.PrivateKey // identity key: signs mandates; published in the trust card
	Rules   config.FlightRules
	Ops     []string // ANS names allowed to request mandates
	Peers   PeerVerifier
	Trust   TrustSource
	Store   *store.Store
	Caller  station.CallerFunc
	Emit    func(bus.Event)
	Now     func() time.Time
	Log     *slog.Logger
}

type issueArgs struct {
	Skill          string          `json:"skill"`
	Quote          json.RawMessage `json:"quote"`
	CommandClasses []string        `json:"command_classes,omitempty"`
}

// IssueResult carries the signed mandate.
type IssueResult struct {
	Mandate string `json:"mandate"`
}

// IssueMandate signs a mandate for a quote, or refuses with POLICY_REFUSED:<rule>.
func (a *Authority) IssueMandate(ctx context.Context, raw json.RawMessage) (out any, err error) {
	defer func() {
		if r := recover(); r != nil {
			a.Log.Error("issue_mandate panic", "panic", r)
			out, err = nil, errs.New(errs.Internal, "issue_mandate failed")
		}
	}()
	tok, q, err := a.issue(ctx, raw)
	if err != nil {
		var e *errs.Error
		code := errs.Internal
		if errors.As(err, &e) {
			code = e.Code
		}
		a.emit("issue_mandate", q.Station, "refused", code, err)
		return nil, err
	}
	a.emit("issue_mandate", q.Station, "ok", "", nil)
	return IssueResult{Mandate: tok}, nil
}

func (a *Authority) emit(kind, subject, result string, code errs.Code, err error) {
	if a.Emit == nil {
		return
	}
	detail := ""
	var e *errs.Error
	if errors.As(err, &e) {
		detail = e.Detail
	}
	a.Emit(bus.Event{Agent: a.ANSName, Kind: kind, Subject: subject, Result: result, Reason: string(code),
		Data: map[string]any{"detail": detail}})
}

func (a *Authority) issue(ctx context.Context, raw json.RawMessage) (string, schema.Quote, error) {
	var args issueArgs
	if err := jose.StrictJSON(raw, schema.MaxObjectBytes); err != nil {
		return "", schema.Quote{}, errs.New(errs.QuoteParseError, err.Error())
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil {
		return "", schema.Quote{}, errs.New(errs.QuoteParseError, "arguments: "+err.Error())
	}
	q, err := schema.DecodeQuote(args.Quote)
	if err != nil {
		return "", schema.Quote{}, err
	}
	caller, ok := a.Caller(ctx)
	if !ok || !contains(a.Ops, caller.ANSName) {
		return "", q, errs.New(errs.PolicyRefusedCaller, "caller is not a configured Ops agent")
	}
	host, err := stationHost(q.Station)
	if err != nil {
		return "", q, errs.New(errs.PolicyRefusedStation, err.Error())
	}
	if res := a.Peers.VerifyPeer(ctx, host); !res.OK() || res.ANSName != q.Station {
		return "", q, errs.New(errs.PolicyRefusedUnverified, fmt.Sprintf("station %s failed verification: %v", host, res.Failed()))
	}
	classes, err := a.checkPolicy(ctx, q, host, args.CommandClasses)
	if err != nil {
		return "", q, err
	}
	m := schema.Mandate{MandateID: "m-" + randomHex(12), Iss: a.ANSName, Sub: caller.ANSName, Aud: q.Station,
		QuoteID: q.QuoteID, Scope: schema.Scope(q.Mode, q.NoradID), CommandClasses: classes,
		MaxAmountCents: q.AmountCents, Nbf: q.AOS, Exp: q.LOS, JKT: caller.JKT, Nonce: randomB64("", 18)}
	// max_passes_per_day counts mandates issued (an unused mandate still counts).
	if _, err := a.Store.RecordMandate(ctx, m.MandateID, q.NoradID, q.AOS, host, a.Rules.MaxPassesPerDay); err != nil {
		return "", q, err
	}
	tok, err := schema.SignMandate(m, a.Key)
	return tok, q, err
}

// checkPolicy applies the flight rules and returns the command classes the
// mandate will carry.
func (a *Authority) checkPolicy(ctx context.Context, q schema.Quote, host string, requested []string) ([]string, error) {
	now := a.Now().Unix()
	if q.Exp <= now || q.LOS <= now {
		return nil, errs.New(errs.PolicyRefusedWindow, "quote or pass is already over")
	}
	if len(a.Rules.Stations) > 0 && !contains(a.Rules.Stations, host) {
		return nil, errs.New(errs.PolicyRefusedStation, host+" is not an allowed station")
	}
	// The operator allow-list (flight-rules "stations named explicitly") grants
	// the mode by operator policy and skips the trust-vector tier gate. This is
	// NOT a trust score — it is labeled "operator allow-list", never FIDUCIARY.
	// Every other rule below (classes, amount, daily limit) still applies.
	if a.operatorAllows(host, q.Mode) {
		if a.Emit != nil {
			a.Emit(bus.Event{Agent: a.ANSName, Kind: "mandate_basis", Subject: q.Station, Result: "operator_allow",
				Reason: "operator allow-list: flight rules name " + host + " for " + q.Mode})
		}
	} else {
		eval, err := a.Trust.Evaluate(ctx, host)
		if err != nil {
			// Fail closed: with no trustworthy read of the station, refuse rather
			// than sign against a stale or absent tier (master plan §11).
			return nil, errs.New(errs.PolicyRefusedTier, "trust index unavailable: "+err.Error())
		}
		tier := OverpassTier(eval, a.Rules)
		if rank(tier) < rank(a.Rules.MinTier) {
			return nil, errs.New(errs.PolicyRefusedTier, fmt.Sprintf("%s is %s, flight rules need %s", host, tier, a.Rules.MinTier))
		}
		if q.Mode == schema.ModeUplink && tier != planner.TierFiduciary {
			return nil, errs.New(errs.PolicyRefusedTier,
				fmt.Sprintf("uplink needs FIDUCIARY; %s is %s (integrity %d, behavior %d, %d audited passes, audit_failures=%v)",
					host, tier, eval.Integrity, eval.Behavior, eval.AuditedPasses, eval.AuditFailures))
		}
	}
	allowed := a.Rules.CommandClasses[q.Mode]
	classes := requested
	if len(classes) == 0 {
		classes = allowed
	}
	if len(classes) == 0 {
		return nil, errs.New(errs.PolicyRefusedClasses, "no command classes allowed for "+q.Mode)
	}
	for _, c := range classes {
		if !contains(allowed, c) {
			return nil, errs.New(errs.PolicyRefusedClasses, fmt.Sprintf("class %q not allowed for %s", c, q.Mode))
		}
	}
	if a.Rules.MaxCentsPerPass > 0 && q.AmountCents > a.Rules.MaxCentsPerPass {
		return nil, errs.New(errs.PolicyRefusedAmount, fmt.Sprintf("%d cents over the %d per-pass limit", q.AmountCents, a.Rules.MaxCentsPerPass))
	}
	if a.Rules.MaxPassesPerDay <= 0 {
		return nil, errs.New(errs.PolicyRefusedDailyLimit, "flight rules allow no passes")
	}
	return classes, nil
}

// operatorAllows reports whether the flight rules name this host explicitly for
// the mode (an operator allow-list, not a trust score).
func (a *Authority) operatorAllows(host, mode string) bool {
	for _, h := range a.Rules.OperatorAllow[mode] {
		if h == host {
			return true
		}
	}
	return false
}

func rank(tier string) int {
	switch tier {
	case planner.TierFiduciary:
		return 3
	case planner.TierTransactional:
		return 2
	case planner.TierReadOnly:
		return 1
	}
	return 0
}

// stationHost extracts the host from a station's ANS name.
func stationHost(ansName string) (string, error) {
	n, err := ansverify.ParseAnsName(ansName)
	if err != nil {
		return "", fmt.Errorf("quote station %q is not an ANS name", ansName)
	}
	return n.Host, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func randomB64(prefix string, n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
