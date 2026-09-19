package ops

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/agentnameservice/ans-sdk-go/verify/scitt"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/jose"
	"github.com/arjitsama/overpass/internal/schema"
	"github.com/arjitsama/overpass/internal/session"
	"github.com/arjitsama/overpass/internal/spacecraft"
	"github.com/arjitsama/overpass/internal/station"
	"github.com/arjitsama/overpass/internal/store"
	"github.com/arjitsama/overpass/internal/verify"
)

const norad = int64(27844)

// clock is a fake clock the whole pass shares.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }
func (c *clock) set(t time.Time)     { c.mu.Lock(); c.t = t; c.mu.Unlock() }

// tokens is the test-only token source: it can flip to revoked or start failing.
type tokens struct {
	mu    sync.Mutex
	state string // "active", "revoked" or "fail"
	clk   *clock
}

func (f *tokens) set(s string) { f.mu.Lock(); f.state = s; f.mu.Unlock() }

func (f *tokens) fetch(_ context.Context, agentID string) (*verify.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch f.state {
	case "fail":
		return nil, errors.New("log unreachable")
	case "revoked":
		return &verify.Token{AgentID: agentID, Status: scitt.StatusRevoked, Iat: f.clk.now()}, nil
	}
	return &verify.Token{AgentID: agentID, Status: scitt.StatusActive, Iat: f.clk.now()}, nil
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func openStore(t *testing.T, name string) *store.Store {
	s, err := store.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

type world struct {
	clk       *clock
	nbf, exp  int64
	opsKey    *ecdsa.PrivateKey
	sc        *spacecraft.Spacecraft
	mgr       *session.Manager
	pass      *Pass
	opsTok    *tokens // the Ops agent's token, watched by the station
	gsTok     *tokens // the station's token, watched by Ops
	events    []bus.Event
	evMu      sync.Mutex
	replanned []string
}

func (w *world) emit(e bus.Event) { w.evMu.Lock(); w.events = append(w.events, e); w.evMu.Unlock() }

func (w *world) kinds() map[string]int {
	w.evMu.Lock()
	defer w.evMu.Unlock()
	out := map[string]int{}
	for _, e := range w.events {
		out[e.Kind]++
	}
	return out
}

// direct relays through the station Manager in-process.
type direct struct{ m *session.Manager }

func (d direct) Relay(ctx context.Context, bookingID, token string) (schema.Ack, error) {
	return d.m.Relay(ctx, bookingID, token)
}

func newWorld(t *testing.T) *world {
	t.Helper()
	start := time.Unix(1_790_000_000, 0)
	w := &world{clk: &clock{t: start}, nbf: start.Unix() + 120, exp: start.Unix() + 600, opsKey: newKey(t)}
	w.opsTok, w.gsTok = &tokens{state: "active", clk: w.clk}, &tokens{state: "active", clk: w.clk}
	jkt, _ := jose.Thumbprint(&w.opsKey.PublicKey)
	authKey := newKey(t)
	m := schema.Mandate{MandateID: "m-pass", Iss: "ans://v0.1.0.authority.example", Sub: "ans://v0.1.0.ops.example",
		Aud: "ans://v0.1.0.gs.example", QuoteID: "q-pass", Scope: schema.Scope(schema.ModeUplink, norad),
		CommandClasses: []string{"telemetry", "attitude"}, MaxAmountCents: 1000, Nbf: w.nbf, Exp: w.exp,
		JKT: jkt, Nonce: "bm9uY2Utbm9uY2Utbm9uY2Ux"}
	mtok, _ := schema.SignMandate(m, authKey)
	gsStore := openStore(t, "gs.db")
	if err := gsStore.Book(context.Background(), store.Booking{BookingID: "b-pass", QuoteID: m.QuoteID, MandateID: m.MandateID,
		Iss: m.Iss, Nonce: m.Nonce, NoradID: norad, Nbf: m.Nbf, Exp: m.Exp, Receipt: "r", Mandate: mtok}); err != nil {
		t.Fatal(err)
	}
	sc, err := spacecraft.New(context.Background(), norad, []*ecdsa.PublicKey{&w.opsKey.PublicKey}, openStore(t, "sc.db"))
	if err != nil {
		t.Fatal(err)
	}
	w.sc = sc
	policy := verify.Policy{MaxAge: verify.DefaultMaxAge}
	stationKeeper := verify.NewKeeper(policy, w.opsTok.fetch)
	stationKeeper.Now = w.clk.now
	opsCaller := func(context.Context) (station.Caller, bool) {
		return station.Caller{ANSName: m.Sub, AgentID: "ops-agent", JKT: jkt}, true
	}
	w.mgr = &session.Manager{ANSName: m.Aud, Store: gsStore, Uplink: sc, Caller: opsCaller, Keeper: stationKeeper,
		Emit: w.emit, Now: w.clk.now}
	opsKeeper := verify.NewKeeper(policy, w.gsTok.fetch)
	opsKeeper.Now = w.clk.now
	w.pass = &Pass{BookingID: "b-pass", MandateID: m.MandateID, StationHost: "gs.example",
		Commander: &Commander{NoradID: norad, Key: w.opsKey, Store: openStore(t, "ops.db"), Now: w.clk.now},
		Station:   direct{w.mgr},
		Monitor:   &session.Monitor{Keeper: opsKeeper, AgentID: "gs-agent", Self: "ops", Subject: "b-pass", Emit: w.emit},
		Replan:    func(h string) { w.replanned = append(w.replanned, h) },
		Emit:      w.emit, Self: "ops"}
	if err := w.pass.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *world) inWindow() { w.clk.set(time.Unix(w.nbf+10, 0)) }

func wantCode(t *testing.T, err error, code errs.Code) {
	t.Helper()
	if !errs.Is(err, code) {
		t.Fatalf("err = %v, want %s", err, code)
	}
}

var telemetry = json.RawMessage(`{"op":"dump"}`)

// Acceptance 1: before and after the window -> WINDOW_CLOSED; inside -> acked.
func TestWindow(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.clk.set(time.Unix(w.nbf-31, 0))
	_, err := w.pass.Send(ctx, "telemetry", telemetry)
	wantCode(t, err, errs.WindowClosed)
	w.clk.set(time.Unix(w.nbf-29, 0)) // the 30 s slack before nbf
	if ack, err := w.pass.Send(ctx, "telemetry", telemetry); err != nil || ack.Result != schema.AckAccepted || ack.TelemetrySHA256 == "" {
		t.Fatalf("inside the slack: %+v %v", ack, err)
	}
	w.inWindow()
	if ack, err := w.pass.Send(ctx, "attitude", json.RawMessage(`{"op":"set_mode","mode":"safe"}`)); err != nil || ack.Result != schema.AckAccepted {
		t.Fatalf("inside: %+v %v", ack, err)
	}
	if w.sc.State().Mode != "safe" {
		t.Fatal("command not applied")
	}
	w.clk.set(time.Unix(w.exp+31, 0))
	_, err = w.pass.Send(ctx, "telemetry", telemetry)
	wantCode(t, err, errs.WindowClosed)
	if w.kinds()["window_closed"] != 1 {
		t.Fatalf("events %v", w.kinds())
	}
}

// Acceptance 2: a command the station invents is refused by the spacecraft;
// a replayed command is refused on counter.
func TestStationCannotForgeOrReplay(t *testing.T) {
	w := newWorld(t)
	w.inWindow()
	ctx := context.Background()
	forged, _ := schema.SignCommand(schema.Command{NoradID: norad, Counter: 999, MandateID: "m-pass", Class: "telemetry",
		Body: telemetry, IssuedAt: w.clk.now().Unix()}, newKey(t))
	ack, err := w.mgr.Relay(ctx, "b-pass", forged)
	if err != nil || ack.Result != schema.AckRejected || ack.Reason != string(errs.CommandRejectedSignature) {
		t.Fatalf("forged: %+v %v", ack, err)
	}
	tok, _, _ := w.pass.Commander.Build(ctx, "m-pass", "telemetry", telemetry)
	if ack, _ := w.sc.Uplink(ctx, tok); ack.Result != schema.AckAccepted {
		t.Fatalf("first: %+v", ack)
	}
	if ack, _ := w.sc.Uplink(ctx, tok); ack.Result != schema.AckRejected || ack.Reason != string(errs.CommandRejectedCounter) {
		t.Fatalf("replay: %+v", ack)
	}
	other, _ := schema.SignCommand(schema.Command{NoradID: 1, Counter: 5000, MandateID: "m-pass", Class: "telemetry",
		Body: telemetry, IssuedAt: 1}, w.opsKey)
	if ack, _ := w.sc.Uplink(ctx, other); ack.Reason != string(errs.CommandRejectedNorad) {
		t.Fatalf("wrong satellite: %+v", ack)
	}
}

// dropping is a station that silently drops one command: it neither relays
// nor records it.
type dropping struct {
	m    *session.Manager
	drop int
	n    int
}

func (d *dropping) Relay(ctx context.Context, bookingID, token string) (schema.Ack, error) {
	d.n++
	if d.n == d.drop {
		return schema.Ack{}, errors.New("timeout")
	}
	return d.m.Relay(ctx, bookingID, token)
}

// Acceptance 3: clean pass -> byte-identical heads; a dropped relay ->
// suspected_drop on the Ops side and heads that differ.
func TestChainsMatchAndDrop(t *testing.T) {
	w := newWorld(t)
	w.inWindow()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := w.pass.Send(ctx, "telemetry", telemetry); err != nil {
			t.Fatal(err)
		}
	}
	opsEv := w.pass.Evidence()
	gsEv, _ := w.mgr.Evidence("b-pass", "ans://v0.1.0.ops.example")
	if opsEv.Head != gsEv.Head || len(opsEv.Records) != 3 || len(opsEv.MissingAcks)+len(gsEv.MissingAcks) != 0 {
		t.Fatalf("clean pass: ops %s gs %s", opsEv.Head, gsEv.Head)
	}
	raw1, _ := json.Marshal(opsEv.Records)
	raw2, _ := json.Marshal(gsEv.Records)
	if string(raw1) != string(raw2) {
		t.Fatal("records differ")
	}

	w2 := newWorld(t)
	w2.inWindow()
	w2.pass.Station = &dropping{m: w2.mgr, drop: 2}
	for i := 0; i < 3; i++ {
		_, _ = w2.pass.Send(ctx, "telemetry", telemetry)
	}
	ops2 := w2.pass.Evidence()
	gs2, _ := w2.mgr.Evidence("b-pass", "ans://v0.1.0.ops.example")
	if len(ops2.MissingAcks) != 1 || ops2.MissingAcks[0] != 2 || ops2.Head == gs2.Head || w2.kinds()["suspected_drop"] != 1 {
		t.Fatalf("drop: ops %+v gs %+v events %v", ops2, gs2, w2.kinds())
	}
}

// Acceptance 4: the token flips to revoked -> the session is cut at the next
// fetch and a replan follows. Both directions.
func TestRevokedCuts(t *testing.T) {
	w := newWorld(t)
	w.inWindow()
	ctx := context.Background()
	w.pass.Enqueue("telemetry", telemetry)
	w.gsTok.set("revoked")
	w.clk.add(session.TokenEvery)
	if code := w.pass.Monitor.Tick(ctx); code != errs.SessionCutRevoked {
		t.Fatalf("ops tick: %s", code)
	}
	if len(w.replanned) != 1 || w.replanned[0] != "gs.example" || w.kinds()["session_cut"] != 1 {
		t.Fatalf("replan %v events %v", w.replanned, w.kinds())
	}
	if acks, _ := w.pass.Flush(ctx); len(acks) != 0 {
		t.Fatal("queued command sent after cut")
	}
	_, err := w.pass.Send(ctx, "telemetry", telemetry)
	wantCode(t, err, errs.SessionCutRevoked)

	// Station side: the Ops agent is revoked.
	w2 := newWorld(t)
	w2.inWindow()
	if _, err := w2.pass.Send(ctx, "telemetry", telemetry); err != nil {
		t.Fatal(err)
	}
	w2.opsTok.set("revoked")
	w2.clk.add(session.TokenEvery)
	if code := w2.mgr.Tick(ctx, "b-pass"); code != errs.SessionCutRevoked {
		t.Fatalf("station tick: %s", code)
	}
	_, err = w2.pass.Send(ctx, "telemetry", telemetry)
	wantCode(t, err, errs.SessionCutRevoked)
}

// Acceptance 5: 20 s of fetch failures -> warnings, session up, commands flow.
func TestOutageWarns(t *testing.T) {
	w := newWorld(t)
	w.inWindow()
	ctx := context.Background()
	if _, err := w.pass.Send(ctx, "telemetry", telemetry); err != nil {
		t.Fatal(err)
	}
	w.gsTok.set("fail")
	w.opsTok.set("fail")
	for elapsed := time.Duration(0); elapsed < 20*time.Second; elapsed += 10 * time.Second {
		w.clk.add(10 * time.Second)
		if code := w.pass.Monitor.Tick(ctx); code != "" {
			t.Fatalf("ops cut during outage: %s", code)
		}
		if code := w.mgr.Tick(ctx, "b-pass"); code != "" {
			t.Fatalf("station cut during outage: %s", code)
		}
		if _, err := w.pass.Send(ctx, "telemetry", telemetry); err != nil {
			t.Fatalf("command during outage: %v", err)
		}
	}
	if w.kinds()["token_warning"] < 2 || w.kinds()["session_cut"] != 0 {
		t.Fatalf("events %v", w.kinds())
	}
}

// Acceptance 6: failures past the max age -> cut with token_stale.
func TestOutagePastMaxAge(t *testing.T) {
	w := newWorld(t)
	w.inWindow()
	ctx := context.Background()
	if code := w.pass.Monitor.Tick(ctx); code != "" { // a fresh token right before the outage
		t.Fatal(code)
	}
	w.gsTok.set("fail")
	for i := 0; i < 21; i++ { // 21 x 30 s = 10.5 min
		w.clk.add(session.TokenEvery)
		if code := w.pass.Monitor.Tick(ctx); code != "" {
			if code != errs.SessionCutStale || i < 19 {
				t.Fatalf("tick %d: %s", i, code)
			}
			if len(w.replanned) != 1 {
				t.Fatal("no replan after stale cut")
			}
			return
		}
	}
	t.Fatal("never cut")
}

// Acceptance 7: a class outside the mandate is CLASS_REJECTED.
func TestClassRejected(t *testing.T) {
	w := newWorld(t)
	w.inWindow()
	_, err := w.pass.Send(context.Background(), "reboot", json.RawMessage(`{"op":"reboot"}`))
	wantCode(t, err, errs.ClassRejected)
}

// The Ops counter survives a restart: a new Commander on the same store
// never reuses a counter.
func TestCounterSurvivesRestart(t *testing.T) {
	st := openStore(t, "ops.db")
	k := newKey(t)
	c1 := &Commander{NoradID: norad, Key: k, Store: st, Now: time.Now}
	_, a, _ := c1.Build(context.Background(), "m", "telemetry", telemetry)
	c2 := &Commander{NoradID: norad, Key: k, Store: st, Now: time.Now}
	_, b, _ := c2.Build(context.Background(), "m", "telemetry", telemetry)
	if b.Counter != a.Counter+1 {
		t.Fatalf("%d then %d", a.Counter, b.Counter)
	}
}

// Review H1: commands the station refused are chained by neither side, so an
// honest station's evidence matches Ops'.
func TestRefusedCommandsNotChained(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.clk.set(time.Unix(w.nbf-60, 0))
	_, err := w.pass.Send(ctx, "telemetry", telemetry)
	wantCode(t, err, errs.WindowClosed)
	w.inWindow()
	_, err = w.pass.Send(ctx, "reboot", telemetry)
	wantCode(t, err, errs.ClassRejected)
	if _, err := w.pass.Send(ctx, "telemetry", telemetry); err != nil {
		t.Fatal(err)
	}
	ops := w.pass.Evidence()
	gs, _ := w.mgr.Evidence("b-pass", "ans://v0.1.0.ops.example")
	if ops.Head != gs.Head || len(ops.Records) != 1 || len(ops.MissingAcks) != 0 || w.kinds()["suspected_drop"] != 0 {
		t.Fatalf("ops %+v gs %+v events %v", ops, gs, w.kinds())
	}
}

// Review H2: a failed token fetch when the session opens refuses the command
// but cuts nothing; the next command, with the log back, goes through.
func TestOpenFetchFailureDoesNotCut(t *testing.T) {
	w := newWorld(t)
	w.inWindow()
	ctx := context.Background()
	w.opsTok.set("fail")
	_, err := w.pass.Send(ctx, "telemetry", telemetry)
	wantCode(t, err, errs.SessionRejectedToken)
	w.opsTok.set("active")
	if _, err := w.pass.Send(ctx, "telemetry", telemetry); err != nil {
		t.Fatalf("after the log recovered: %v", err)
	}
	if w.kinds()["session_cut"] != 0 {
		t.Fatalf("events %v", w.kinds())
	}
	// Ops side: a failed first fetch at Start is an error, not a cut.
	w2 := newWorld(t)
	w2.gsTok.set("fail")
	w2.pass.Monitor = &session.Monitor{Keeper: verify.NewKeeper(verify.Policy{MaxAge: verify.DefaultMaxAge}, w2.gsTok.fetch), AgentID: "gs-agent"}
	if err := w2.pass.Start(ctx); !errs.Is(err, errs.SessionRejectedToken) {
		t.Fatalf("start: %v", err)
	}
	if code, _ := w2.pass.Monitor.Cut(); code != "" {
		t.Fatalf("cut at open: %s", code)
	}
}

// Acceptance 6 on the station side: the Ops token fails past the max age.
func TestStationStaleOpsToken(t *testing.T) {
	w := newWorld(t)
	w.inWindow()
	ctx := context.Background()
	if _, err := w.pass.Send(ctx, "telemetry", telemetry); err != nil {
		t.Fatal(err)
	}
	w.opsTok.set("fail")
	for i := 0; i < 21; i++ {
		w.clk.add(session.TokenEvery)
		if code := w.mgr.Tick(ctx, "b-pass"); code != "" {
			if code != errs.SessionCutStale || i < 19 {
				t.Fatalf("tick %d: %s", i, code)
			}
			if ev, _ := w.mgr.Evidence("b-pass", "ans://v0.1.0.ops.example"); ev.Cut != string(errs.SessionCutStale) {
				t.Fatalf("evidence cut %q", ev.Cut)
			}
			return
		}
	}
	t.Fatal("never cut")
}

// Acceptance 2 through the station: a replayed command is refused on counter
// before it is relayed.
func TestStationRefusesReplay(t *testing.T) {
	w := newWorld(t)
	w.inWindow()
	ctx := context.Background()
	tok, _, _ := w.pass.Commander.Build(ctx, "m-pass", "telemetry", telemetry)
	if _, err := w.mgr.Relay(ctx, "b-pass", tok); err != nil {
		t.Fatal(err)
	}
	_, err := w.mgr.Relay(ctx, "b-pass", tok)
	wantCode(t, err, errs.CommandRejectedCounter)
}

// Acceptance 4 with the real background loop: revocation cuts within one
// interval, with no command needed.
func TestLoopCutsWithinInterval(t *testing.T) {
	w := newWorld(t)
	w.mgr.Every = 20 * time.Millisecond
	t.Cleanup(w.mgr.Close)
	w.inWindow()
	ctx := context.Background()
	if _, err := w.pass.Send(ctx, "telemetry", telemetry); err != nil {
		t.Fatal(err)
	}
	w.opsTok.set("revoked")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ev, _ := w.mgr.Evidence("b-pass", "ans://v0.1.0.ops.example"); ev.Cut == string(errs.SessionCutRevoked) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("loop did not cut")
}

// Only the booking's Ops agent or an auditor reads its evidence.
func TestEvidenceAccess(t *testing.T) {
	w := newWorld(t)
	w.inWindow()
	_, _ = w.pass.Send(context.Background(), "telemetry", telemetry)
	if _, err := w.mgr.Evidence("b-pass", "ans://v0.1.0.stranger.example"); !errs.Is(err, errs.SessionRejectedCaller) {
		t.Fatalf("stranger: %v", err)
	}
	w.mgr.Auditors = []string{"ans://v0.1.0.auditor.example"}
	if _, err := w.mgr.Evidence("b-pass", "ans://v0.1.0.auditor.example"); err != nil {
		t.Fatalf("auditor: %v", err)
	}
}
