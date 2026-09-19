// Package auditor audits a finished pass and probes stations for the checks
// they should enforce (master plan 9.9-9.11, 10). Passive: re-verify the
// pass's identity, mandate and receipt, compare the two chain heads, count
// missing acks, and sign an AuditReport. Active: canary probes a correct
// station rejects; acceptance is CANARY_ACCEPTED.
package auditor

import (
	"context"
	"crypto/ecdsa"
	"time"

	"github.com/arjitsama/overpass/internal/battery"
	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/schema"
	"github.com/arjitsama/overpass/internal/session"
	"github.com/arjitsama/overpass/internal/verify"
)

// PeerVerifier is verify.Verifier's VerifyPeer.
type PeerVerifier interface {
	VerifyPeer(ctx context.Context, host string) verify.Result
}

// TrustSink receives a station's pass_delivery observation (master plan 9.11);
// the trust index consumes it in Phase 8.
type TrustSink interface {
	Post(ctx context.Context, o Observation) error
}

// Observation is one audited pass's outcome for the trust index.
type Observation struct {
	Station       string `json:"station"`
	PassID        string `json:"pass_id"`
	Verdict       string `json:"verdict"`
	AuditFailures int    `json:"audit_failures"`
}

// LogSink is a TrustSink stub that emits the observation as a bus event.
type LogSink struct{ Emit func(bus.Event) }

// Post implements TrustSink.
func (s LogSink) Post(_ context.Context, o Observation) error {
	if s.Emit != nil {
		s.Emit(bus.Event{Agent: o.Station, Kind: "pass_delivery", Subject: o.PassID, Result: o.Verdict,
			Data: map[string]any{"audit_failures": o.AuditFailures}})
	}
	return nil
}

// PassEvidence is what the auditor pulls for a finished pass.
type PassEvidence struct {
	Receipt string           // station-signed booking receipt JWS
	Mandate string           // authority-signed mandate JWS
	Ops     session.Evidence // Ops' chain and acks
	Station session.Evidence // the station's chain and acks
}

// Auditor signs reports with its identity key.
type Auditor struct {
	ANSName       string
	Key           *ecdsa.PrivateKey
	Peers         PeerVerifier
	AuthorityKeys []*ecdsa.PublicKey // to verify mandates
	StationKeys   func(host string) []*ecdsa.PublicKey
	Sink          TrustSink
	Now           func() time.Time
}

func (a *Auditor) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Audit re-verifies a pass and returns the report and its signed JWS.
func (a *Auditor) Audit(ctx context.Context, stationHost, stationANS, passID string, ev PassEvidence) (schema.AuditReport, string, error) {
	rep := schema.AuditReport{PassID: passID, Iss: a.ANSName, Station: stationANS, Iat: a.now().Unix(),
		Checks: []schema.Check{}, Canaries: []schema.Canary{},
		ChainHead: nonEmpty(ev.Ops.Head), StationChainHead: nonEmpty(ev.Station.Head)}
	pass := true
	add := func(name string, ok bool, code errs.Code) {
		res := schema.Pass
		c := schema.Check{Name: name, Result: res}
		if !ok {
			res, pass = schema.Fail, false
			c.Result, c.Code = schema.Fail, string(code)
		}
		rep.Checks = append(rep.Checks, c)
	}

	id := a.Peers.VerifyPeer(ctx, stationHost)
	add("identity", id.OK() && id.ANSName == stationANS, errs.CardRejectedSignature)

	receipt, rerr := schema.VerifyBookingReceipt(ev.Receipt, a.StationKeys(stationHost))
	add("receipt", rerr == nil, errs.ReceiptRejectedSignature)

	mandate, merr := schema.VerifyMandate(ev.Mandate, a.AuthorityKeys)
	add("mandate", merr == nil, errs.MandateRejectedSignature)

	add("dpop_binding", merr == nil && rerr == nil && mandate.JKT != "" && receipt.MandateID == mandate.MandateID, errs.DPoPRejectedKey)

	headsMatch := ev.Ops.Head != "" && ev.Ops.Head == ev.Station.Head
	add("chain_match", headsMatch, errs.ChainMismatch)

	missing := countMissing(ev)
	add("acks", missing == 0, errs.AckMissing)
	rep.MissingAcks = int64(missing)

	rep.Verdict = schema.Fail
	if pass {
		rep.Verdict = schema.Pass
	}
	if err := rep.Validate(); err != nil {
		return schema.AuditReport{}, "", err
	}
	tok, err := schema.SignAuditReport(rep, a.Key)
	if err != nil {
		return schema.AuditReport{}, "", err
	}
	if a.Sink != nil {
		fails := 0
		for _, c := range rep.Checks {
			if c.Result == schema.Fail {
				fails++
			}
		}
		_ = a.Sink.Post(ctx, Observation{Station: stationANS, PassID: passID, Verdict: rep.Verdict, AuditFailures: fails})
	}
	return rep, tok, nil
}

// countMissing counts commands the station recorded without an ack, plus any
// mismatch between the sides' record counts.
func countMissing(ev PassEvidence) int {
	acked := map[int64]bool{}
	for _, a := range ev.Station.Acks {
		acked[a.Counter] = true
	}
	missing := 0
	for _, r := range ev.Station.Records {
		if !acked[r.Counter] {
			missing++
		}
	}
	if len(ev.Ops.Records) != len(ev.Station.Records) {
		missing += abs(len(ev.Ops.Records) - len(ev.Station.Records))
	}
	return missing
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Canary probes a station with the two bad bookings a correct station must
// reject, records CANARY_ACCEPTED for any it accepts, signs a report, and
// posts an observation whose audit_failures counts the acceptances.
func (a *Auditor) Canary(ctx context.Context, stationHost, stationANS string, b *battery.Battery) (schema.AuditReport, string, error) {
	return a.canaryReport(ctx, "canary-"+stationHost, stationANS, b.Canary(ctx))
}

// canaryReport turns probe results into a signed AuditReport and posts an
// observation whose audit_failures counts the acceptances.
func (a *Auditor) canaryReport(ctx context.Context, passID, stationANS string, probes []battery.Result) (schema.AuditReport, string, error) {
	rep := schema.AuditReport{PassID: passID, Iss: a.ANSName, Station: stationANS, Iat: a.now().Unix(),
		Checks: []schema.Check{}, Canaries: []schema.Canary{},
		ChainHead: zeroHead, StationChainHead: zeroHead}
	fails := 0
	for _, r := range probes {
		c := schema.Canary{Probe: r.Name, Result: schema.Rejected, Code: string(r.Observed)}
		// CANARY_ACCEPTED only when the station actually accepted the bad
		// booking (no rejection code); a rejection with an unexpected code is
		// still a rejection.
		if r.Verdict == battery.Vulnerable && r.Observed == "" {
			c.Result, c.Code = schema.Accepted, string(errs.CanaryAccepted)
			fails++
		}
		rep.Canaries = append(rep.Canaries, c)
	}
	rep.Verdict = schema.Pass
	if fails > 0 {
		rep.Verdict = schema.Fail
	}
	if err := rep.Validate(); err != nil {
		return schema.AuditReport{}, "", err
	}
	tok, err := schema.SignAuditReport(rep, a.Key)
	if err != nil {
		return schema.AuditReport{}, "", err
	}
	if a.Sink != nil {
		_ = a.Sink.Post(ctx, Observation{Station: stationANS, PassID: rep.PassID, Verdict: rep.Verdict, AuditFailures: fails})
	}
	return rep, tok, nil
}

const zeroHead = "0000000000000000000000000000000000000000000000000000000000000000"

func nonEmpty(head string) string {
	if head == "" {
		return zeroHead
	}
	return head
}
