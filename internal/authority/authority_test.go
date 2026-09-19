package authority

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/jose"
	"github.com/arjitsama/overpass/internal/planner"
	"github.com/arjitsama/overpass/internal/schema"
	"github.com/arjitsama/overpass/internal/station"
	"github.com/arjitsama/overpass/internal/store"
	"github.com/arjitsama/overpass/internal/verify"
)

const (
	authName = "ans://v0.1.0.authority.example"
	opsName  = "ans://v0.1.0.ops.example"
)

var now = time.Unix(1_790_000_000, 0)

type ctxKey struct{}

// fakePeers verifies every station except those listed as failing.
type fakePeers struct{ fail map[string]bool }

func (f fakePeers) VerifyPeer(_ context.Context, host string) verify.Result {
	if f.fail[host] {
		return verify.Result{Host: host, Verdict: verify.Fail, Checks: []verify.Check{{Name: "registered", Verdict: verify.Fail, Reason: "no badge"}}}
	}
	return verify.Result{Host: host, ANSName: "ans://v0.1.0." + host, Verdict: verify.Pass}
}

type fixture struct {
	a     *Authority
	key   *ecdsa.PrivateKey
	jkt   string
	ctx   context.Context
	peers fakePeers
}

func newFixture(t *testing.T) *fixture {
	db, err := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	opsKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	jkt, _ := jose.Thumbprint(&opsKey.PublicKey)
	f := &fixture{key: key, jkt: jkt, peers: fakePeers{fail: map[string]bool{}}}
	f.ctx = context.WithValue(context.Background(), ctxKey{}, station.Caller{ANSName: opsName, JKT: jkt})
	f.a = &Authority{ANSName: authName, Key: key, Ops: []string{opsName}, Peers: f.peers, Store: db,
		Rules: config.FlightRules{MinTier: planner.TierTransactional, MaxCentsPerPass: 5000, MaxPassesPerDay: 3,
			CommandClasses: map[string][]string{"uplink": {"telemetry", "attitude"}, "downlink": {"telemetry"}}},
		Trust: StaticTrust{"gs-blacksburg.example": planner.TierFiduciary, "gs-svalbard-eu.example": planner.TierReadOnly,
			"gs-awarua.example": planner.TierTransactional},
		Caller: func(ctx context.Context) (station.Caller, bool) {
			c, ok := ctx.Value(ctxKey{}).(station.Caller)
			return c, ok
		},
		Now: func() time.Time { return now }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return f
}

func quote(host, mode string, cents int64) schema.Quote {
	return schema.Quote{QuoteID: "q-1", Station: "ans://v0.1.0." + host, NoradID: 27844, AOS: now.Unix() + 3600,
		LOS: now.Unix() + 4080, MaxElevationDeg: 40, Mode: mode, AmountCents: cents,
		Accepts: []schema.Accept{{Scheme: "exact", Network: "base-sepolia", PayTo: "0xabc", Asset: "USDC", Amount: cents * 10000}},
		Exp:     now.Unix() + 600}
}

func (f *fixture) issue(ctx context.Context, q schema.Quote, classes ...string) (string, error) {
	args, _ := json.Marshal(map[string]any{"skill": "issue_mandate", "quote": q, "command_classes": classes})
	out, err := f.a.IssueMandate(ctx, args)
	if err != nil {
		return "", err
	}
	return out.(IssueResult).Mandate, nil
}

func TestIssuesAndSignsMandate(t *testing.T) {
	f := newFixture(t)
	q := quote("gs-blacksburg.example", schema.ModeUplink, 1200)
	tok, err := f.issue(f.ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	m, err := schema.VerifyMandate(tok, []*ecdsa.PublicKey{&f.key.PublicKey})
	if err != nil {
		t.Fatal(err)
	}
	if m.Iss != authName || m.Sub != opsName || m.Aud != q.Station || m.QuoteID != q.QuoteID || m.JKT != f.jkt ||
		m.Nbf != q.AOS || m.Exp != q.LOS || m.MaxAmountCents != 1200 || m.Scope != "pass:uplink:27844" ||
		len(m.CommandClasses) != 2 {
		t.Fatalf("mandate %+v", m)
	}
}

// Acceptance 5, and one refusal per flight rule.
func TestPolicyRefusals(t *testing.T) {
	f := newFixture(t)
	other := context.WithValue(context.Background(), ctxKey{}, station.Caller{ANSName: "ans://v0.1.0.stranger.example", JKT: f.jkt})
	f.peers.fail["gs-evil.example"] = true
	f.a.Trust.(StaticTrust)["gs-evil.example"] = planner.TierFiduciary
	past := quote("gs-blacksburg.example", schema.ModeUplink, 1200)
	past.Exp = now.Unix() - 1
	cases := []struct {
		name    string
		ctx     context.Context
		q       schema.Quote
		classes []string
		code    errs.Code
	}{
		{"READ_ONLY station, uplink", f.ctx, quote("gs-svalbard-eu.example", schema.ModeUplink, 1200), nil, errs.PolicyRefusedTier},
		{"TRANSACTIONAL station, uplink", f.ctx, quote("gs-awarua.example", schema.ModeUplink, 1200), nil, errs.PolicyRefusedTier},
		{"READ_ONLY station, downlink below min tier", f.ctx, quote("gs-svalbard-eu.example", schema.ModeDownlink, 1200), nil, errs.PolicyRefusedTier},
		{"caller not an ops agent", other, quote("gs-blacksburg.example", schema.ModeUplink, 1200), nil, errs.PolicyRefusedCaller},
		{"station fails verification", f.ctx, quote("gs-evil.example", schema.ModeUplink, 1200), nil, errs.PolicyRefusedUnverified},
		{"class not allowed", f.ctx, quote("gs-blacksburg.example", schema.ModeDownlink, 1200), []string{"attitude"}, errs.PolicyRefusedClasses},
		{"over the per-pass limit", f.ctx, quote("gs-blacksburg.example", schema.ModeUplink, 5001), nil, errs.PolicyRefusedAmount},
		{"expired quote", f.ctx, past, nil, errs.PolicyRefusedWindow},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.issue(c.ctx, c.q, c.classes...)
			if !errs.Is(err, c.code) {
				t.Fatalf("err = %v, want %s", err, c.code)
			}
		})
	}
	// Downlink on a TRANSACTIONAL station is fine.
	if _, err := f.issue(f.ctx, quote("gs-awarua.example", schema.ModeDownlink, 1200)); err != nil {
		t.Fatalf("downlink on TRANSACTIONAL: %v", err)
	}
}

func TestDailyLimitAndStationList(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 3; i++ {
		if _, err := f.issue(f.ctx, quote("gs-blacksburg.example", schema.ModeUplink, 1200)); err != nil {
			t.Fatalf("mandate %d: %v", i, err)
		}
	}
	if _, err := f.issue(f.ctx, quote("gs-blacksburg.example", schema.ModeUplink, 1200)); !errs.Is(err, errs.PolicyRefusedDailyLimit) {
		t.Fatalf("fourth: %v", err)
	}
	f2 := newFixture(t)
	f2.a.Rules.Stations = []string{"gs-awarua.example"}
	if _, err := f2.issue(f2.ctx, quote("gs-blacksburg.example", schema.ModeDownlink, 1200)); !errs.Is(err, errs.PolicyRefusedStation) {
		t.Fatalf("station list: %v", err)
	}
}

func TestHostileArgs(t *testing.T) {
	f := newFixture(t)
	for _, in := range []string{``, `{}`, `{"skill":"issue_mandate","quote":1}`, `{"skill":"issue_mandate","quote":{"quote_id":"x"}}`,
		`{"skill":"issue_mandate","quote":{},"x":1}`} {
		_, err := f.a.IssueMandate(f.ctx, json.RawMessage(in))
		var e *errs.Error
		if !errs.As(err, &e) {
			t.Errorf("%q: %v", in, err)
		}
	}
}
