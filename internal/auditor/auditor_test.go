package auditor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"
	"time"

	"github.com/arjitsama/overpass/internal/battery"
	"github.com/arjitsama/overpass/internal/chain"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/schema"
	"github.com/arjitsama/overpass/internal/session"
	"github.com/arjitsama/overpass/internal/verify"
)

const (
	stationANS  = "ans://v0.1.0.gs-blacksburg.localhost"
	stationHost = "gs-blacksburg.localhost"
)

var now = time.Unix(1_790_000_000, 0)

func key(t *testing.T) *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

type fakePeers struct{ ok bool }

func (f fakePeers) VerifyPeer(_ context.Context, host string) verify.Result {
	if f.ok {
		return verify.Result{Host: host, ANSName: "ans://v0.1.0." + host, Verdict: verify.Pass}
	}
	return verify.Result{Host: host, Verdict: verify.Fail}
}

// buildChain returns a chain of n commands and its Acks.
func buildChain(t *testing.T, n int) (*chain.Chain, []schema.Ack) {
	c := chain.New()
	var acks []schema.Ack
	for i := int64(1); i <= int64(n); i++ {
		if _, err := c.Append(i, "m-1", "telemetry", chain.CmdSHA256("cmd")); err != nil {
			t.Fatal(err)
		}
		acks = append(acks, schema.Ack{Counter: i, Result: schema.AckAccepted, TelemetrySHA256: chain.CmdSHA256("t")})
	}
	return c, acks
}

func newAuditor(t *testing.T, peersOK bool, authKey, stationKey *ecdsa.PrivateKey, sink TrustSink) *Auditor {
	return &Auditor{ANSName: "ans://v0.1.0.auditor.localhost", Key: key(t), Peers: fakePeers{ok: peersOK},
		AuthorityKeys: []*ecdsa.PublicKey{&authKey.PublicKey},
		StationKeys:   func(string) []*ecdsa.PublicKey { return []*ecdsa.PublicKey{&stationKey.PublicKey} },
		Sink:          sink, Now: func() time.Time { return now }}
}

func cleanPass(t *testing.T, authKey, stationKey *ecdsa.PrivateKey) (PassEvidence, string) {
	m := schema.Mandate{MandateID: "m-1", Iss: "ans://v0.1.0.authority.localhost", Sub: "ans://v0.1.0.ops.localhost",
		Aud: stationANS, QuoteID: "q-1", Scope: schema.Scope(schema.ModeUplink, 27844), CommandClasses: []string{"telemetry"},
		MaxAmountCents: 1200, Nbf: now.Unix(), Exp: now.Unix() + 480, JKT: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Nonce: "bm9uY2Utbm9uY2Utbm9uY2U"}
	mtok, err := schema.SignMandate(m, authKey)
	if err != nil {
		t.Fatal(err)
	}
	rcpt, err := schema.SignBookingReceipt(schema.BookingReceipt{BookingID: "b-1", Station: stationANS, MandateID: "m-1",
		QuoteID: "q-1", NoradID: 27844, Nbf: m.Nbf, Exp: m.Exp, AmountCents: 1200, Iat: now.Unix()}, stationKey)
	if err != nil {
		t.Fatal(err)
	}
	oc, oa := buildChain(t, 3)
	sc, sa := buildChain(t, 3)
	return PassEvidence{Receipt: rcpt, Mandate: mtok,
		Ops:     session.Evidence{BookingID: "b-1", Head: oc.Head(), Records: oc.Records(), Acks: oa},
		Station: session.Evidence{BookingID: "b-1", Head: sc.Head(), Records: sc.Records(), Acks: sa}}, mtok
}

// Acceptance 6: a clean pass produces verdict pass with a chain head equal on
// both sides.
func TestAuditCleanPass(t *testing.T) {
	authKey, stationKey := key(t), key(t)
	var posted []Observation
	a := newAuditor(t, true, authKey, stationKey, sinkFunc(func(o Observation) { posted = append(posted, o) }))
	ev, _ := cleanPass(t, authKey, stationKey)
	rep, tok, err := a.Audit(context.Background(), stationHost, stationANS, "b-1", ev)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verdict != schema.Pass || rep.ChainHead != rep.StationChainHead || rep.MissingAcks != 0 {
		t.Fatalf("report %+v", rep)
	}
	if _, err := schema.VerifyAuditReport(tok, []*ecdsa.PublicKey{&a.Key.PublicKey}); err != nil {
		t.Fatalf("report does not verify: %v", err)
	}
	if len(posted) != 1 || posted[0].Verdict != schema.Pass || posted[0].AuditFailures != 0 {
		t.Fatalf("observation %+v", posted)
	}
}

// A chain mismatch or missing acks fails the audit with the right check codes.
func TestAuditCatchesTampering(t *testing.T) {
	authKey, stationKey := key(t), key(t)
	a := newAuditor(t, true, authKey, stationKey, nil)
	ev, _ := cleanPass(t, authKey, stationKey)
	ev.Station.Head = "0000000000000000000000000000000000000000000000000000000000000000"
	rep, _, err := a.Audit(context.Background(), stationHost, stationANS, "b-1", ev)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verdict != schema.Fail || !hasFail(rep, "chain_match", errs.ChainMismatch) {
		t.Fatalf("report %+v", rep)
	}

	ev2, _ := cleanPass(t, authKey, stationKey)
	ev2.Station.Acks = ev2.Station.Acks[:1] // two commands went unacked
	rep2, _, _ := a.Audit(context.Background(), stationHost, stationANS, "b-1", ev2)
	if rep2.Verdict != schema.Fail || rep2.MissingAcks != 2 || !hasFail(rep2, "acks", errs.AckMissing) {
		t.Fatalf("report %+v", rep2)
	}
}

func hasFail(rep schema.AuditReport, name string, code errs.Code) bool {
	for _, c := range rep.Checks {
		if c.Name == name {
			return c.Result == schema.Fail && c.Code == string(code)
		}
	}
	return false
}

// Acceptance 5 (report side): a station that accepts a canary probe yields a
// report with CANARY_ACCEPTED and verdict fail; posts audit_failures > 0.
func TestCanaryReport(t *testing.T) {
	a := newAuditor(t, true, key(t), key(t), nil)
	rep, tok, err := a.canaryReport(context.Background(), "canary-gs", stationANS, []battery.Result{
		{Name: "corrupt_jws_attack", Verdict: battery.Blocked, Observed: errs.MandateRejectedSignature},
		{Name: "wrong_dpop_key_attack", Verdict: battery.Vulnerable, Observed: ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verdict != schema.Fail || len(rep.Canaries) != 2 {
		t.Fatalf("report %+v", rep)
	}
	if rep.Canaries[0].Result != schema.Rejected || rep.Canaries[1].Result != schema.Accepted ||
		rep.Canaries[1].Code != string(errs.CanaryAccepted) {
		t.Fatalf("canaries %+v", rep.Canaries)
	}
	if _, err := schema.VerifyAuditReport(tok, []*ecdsa.PublicKey{&a.Key.PublicKey}); err != nil {
		t.Fatal(err)
	}
	// An honest station: both rejected, verdict pass.
	rep2, _, _ := a.canaryReport(context.Background(), "canary-gs", stationANS, []battery.Result{
		{Name: "corrupt_jws_attack", Verdict: battery.Blocked, Observed: errs.MandateRejectedSignature},
		{Name: "wrong_dpop_key_attack", Verdict: battery.Blocked, Observed: errs.DPoPRejectedKey},
	})
	if rep2.Verdict != schema.Pass {
		t.Fatalf("honest canary verdict %s", rep2.Verdict)
	}
}

type sinkFunc func(Observation)

func (f sinkFunc) Post(_ context.Context, o Observation) error { f(o); return nil }
