package station

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/jose"
	"github.com/arjitsama/overpass/internal/schema"
	"github.com/arjitsama/overpass/internal/store"
)

const (
	norad    = int64(27844)
	gsName   = "ans://v0.1.0.gs-blacksburg.example"
	authName = "ans://v0.1.0.authority.example"
	opsName  = "ans://v0.1.0.ops.example"
	// A second registered authority that does not own the satellite.
	otherAuth = "ans://v0.1.0.authority.other.example"
)

var now = time.Unix(1_790_000_000, 0)

type ctxKey struct{}

func withCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

func testCaller(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(ctxKey{}).(Caller)
	return c, ok
}

func newKey(t testing.TB) *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

type fixture struct {
	st       *Station
	authKey  *ecdsa.PrivateKey
	otherKey *ecdsa.PrivateKey
	opsKey   *ecdsa.PrivateKey
	opsJKT   string
	opsCtx   context.Context
	pricing  config.Pricing
	nextAOS  int64
	aosMu    sync.Mutex
}

func newFixture(t testing.TB) *fixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "station.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	f := &fixture{authKey: newKey(t), otherKey: newKey(t), opsKey: newKey(t), nextAOS: now.Unix() + 3600}
	f.opsJKT, _ = jose.Thumbprint(&f.opsKey.PublicKey)
	f.opsCtx = withCaller(context.Background(), Caller{ANSName: opsName, JKT: f.opsJKT})
	f.pricing = config.Pricing{PerMinuteCents: 150, PayTo: "0xStationWallet", Network: "base-sepolia", Asset: "USDC", AssetDecimals: 6}
	f.st = &Station{
		ANSName: gsName, Pricing: f.pricing, Key: newKey(t), Store: db,
		Registry: schema.SatRegistry{Iss: authName, Iat: now.Unix(), Satellites: []schema.Satellite{
			{NoradID: norad, AuthorityANSName: authName, OpsANSNames: []string{opsName}},
			{NoradID: 99999, AuthorityANSName: otherAuth, OpsANSNames: []string{opsName}},
		}},
		Keys:   StaticKeys{authName: {&f.authKey.PublicKey}, otherAuth: {&f.otherKey.PublicKey}},
		Caller: testCaller, Now: func() time.Time { return now },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return f
}

// quote asks the station for a quote on a fresh 8-minute window.
func (f *fixture) quote(t testing.TB, mode string) schema.Quote {
	t.Helper()
	f.aosMu.Lock()
	aos := f.nextAOS
	f.nextAOS += 900
	f.aosMu.Unlock()
	return f.quoteAt(t, mode, aos, aos+480)
}

func (f *fixture) quoteAt(t testing.TB, mode string, aos, los int64) schema.Quote {
	t.Helper()
	return f.quoteFor(t, norad, mode, aos, los)
}

func (f *fixture) quoteFor(t testing.TB, id int64, mode string, aos, los int64) schema.Quote {
	t.Helper()
	args := fmt.Sprintf(`{"skill":"get_pass_quote","norad_id":%d,"aos":%d,"los":%d,"mode":%q,"max_elevation_deg":45}`, id, aos, los, mode)
	out, err := f.st.GetPassQuote(f.opsCtx, json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	return out.(schema.Quote)
}

// mandate is what the authority would issue for q.
func (f *fixture) mandate(q schema.Quote) schema.Mandate {
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	return schema.Mandate{MandateID: "m-" + q.QuoteID[2:], Iss: authName, Sub: opsName, Aud: q.Station,
		QuoteID: q.QuoteID, Scope: schema.Scope(q.Mode, q.NoradID), CommandClasses: []string{"telemetry"},
		MaxAmountCents: q.AmountCents, Nbf: q.AOS, Exp: q.LOS, JKT: f.opsJKT,
		Nonce: strings.TrimRight(fmt.Sprintf("%x", nonce), "=")}
}

func sign(t testing.TB, m schema.Mandate, k *ecdsa.PrivateKey) string {
	t.Helper()
	tok, err := schema.SignMandate(m, k)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (f *fixture) book(ctx context.Context, quoteID, tok string) (BookResult, error) {
	args, _ := json.Marshal(map[string]string{"skill": "book_pass", "quote_id": quoteID, "mandate": tok})
	out, err := f.st.BookPass(ctx, args)
	if err != nil {
		return BookResult{}, err
	}
	return out.(BookResult), nil
}

func wantCode(t testing.TB, err error, code errs.Code) {
	t.Helper()
	if !errs.Is(err, code) {
		t.Fatalf("err = %v, want %s", err, code)
	}
}

// Acceptance 1: quote, mandate, book succeeds once; the same mandate again is consumed.
func TestHappyPathThenConsumed(t *testing.T) {
	f := newFixture(t)
	q := f.quote(t, schema.ModeUplink)
	tok := sign(t, f.mandate(q), f.authKey)
	res, err := f.book(f.opsCtx, q.QuoteID, tok)
	if err != nil {
		t.Fatal(err)
	}
	r, err := schema.VerifyBookingReceipt(res.Receipt, []*ecdsa.PublicKey{&f.st.Key.PublicKey})
	if err != nil || r.BookingID != res.BookingID || r.Nbf != q.AOS || r.AmountCents != q.AmountCents || r.Station != gsName {
		t.Fatalf("receipt %+v %v", r, err)
	}
	_, err = f.book(f.opsCtx, q.QuoteID, tok)
	wantCode(t, err, errs.MandateRejectedConsumed)
	if n, _ := f.st.Store.Bookings(context.Background()); n != 1 {
		t.Fatalf("%d bookings", n)
	}
}

// Acceptance 2: one row per check, each with its exact code.
func TestBookPassChecks(t *testing.T) {
	f := newFixture(t)
	type row struct {
		name  string
		setup func(t *testing.T) (ctx context.Context, quoteID, token string)
		code  errs.Code
	}
	rows := []row{
		{"1 parse: no scope/jkt (superseded format)", func(t *testing.T) (context.Context, string, string) {
			q := f.quote(t, schema.ModeUplink)
			payload, _ := jose.Canonicalize(map[string]any{"mandate_id": "m-x", "iss": authName, "quote_id": q.QuoteID})
			tok, _ := jose.Sign(jose.TypMandate, payload, f.authKey, "")
			return f.opsCtx, q.QuoteID, tok
		}, errs.MandateParseError},
		{"1 parse: garbage", func(t *testing.T) (context.Context, string, string) { return f.opsCtx, "q-x", "not.a.jws" }, errs.MandateParseError},
		{"1 typ: a command presented as a mandate", func(t *testing.T) (context.Context, string, string) {
			tok, _ := schema.SignCommand(schema.Command{NoradID: norad, Counter: 1, MandateID: "m-1", Class: "telemetry",
				Body: json.RawMessage(`{}`), IssuedAt: now.Unix()}, f.opsKey)
			return f.opsCtx, "q-x", tok
		}, errs.TypRejected},
		{"2 signature: flipped byte", func(t *testing.T) (context.Context, string, string) {
			q := f.quote(t, schema.ModeUplink)
			tok := sign(t, f.mandate(q), f.authKey)
			return f.opsCtx, q.QuoteID, tok[:len(tok)-2] + flip(tok[len(tok)-2:])
		}, errs.MandateRejectedSignature},
		{"2 signature: underpay without re-signing", func(t *testing.T) (context.Context, string, string) {
			q := f.quote(t, schema.ModeUplink)
			tok := sign(t, f.mandate(q), f.authKey)
			return f.opsCtx, q.QuoteID, tamper(t, tok, func(m map[string]any) { m["max_amount_cents"] = 1 })
		}, errs.MandateRejectedSignature},
		{"2 signature: unknown key", func(t *testing.T) (context.Context, string, string) {
			q := f.quote(t, schema.ModeUplink)
			return f.opsCtx, q.QuoteID, sign(t, f.mandate(q), newKey(t))
		}, errs.MandateRejectedSignature},
		{"3 not_owner: valid mandate from a different registered authority", func(t *testing.T) (context.Context, string, string) {
			q := f.quote(t, schema.ModeUplink)
			m := f.mandate(q)
			m.Iss = otherAuth
			return f.opsCtx, q.QuoteID, sign(t, m, f.otherKey)
		}, errs.MandateRejectedNotOwner},
		{"3 not_owner: sub is not a registered ops agent", func(t *testing.T) (context.Context, string, string) {
			q := f.quote(t, schema.ModeUplink)
			m := f.mandate(q)
			m.Sub = "ans://v0.1.0.intruder.example"
			return f.opsCtx, q.QuoteID, sign(t, m, f.authKey)
		}, errs.MandateRejectedNotOwner},
		{"4 audience: Svalbard mandate at Blacksburg", func(t *testing.T) (context.Context, string, string) {
			q := f.quote(t, schema.ModeUplink)
			m := f.mandate(q)
			m.Aud = "ans://v0.1.0.gs-svalbard.example"
			return f.opsCtx, q.QuoteID, sign(t, m, f.authKey)
		}, errs.MandateRejectedAudience},
		{"5 quote: mandate for pass A used to book pass B", func(t *testing.T) (context.Context, string, string) {
			a, b := f.quote(t, schema.ModeUplink), f.quote(t, schema.ModeUplink)
			return f.opsCtx, b.QuoteID, sign(t, f.mandate(a), f.authKey)
		}, errs.MandateRejectedQuote},
		{"5 quote: unknown quote", func(t *testing.T) (context.Context, string, string) {
			q := f.quote(t, schema.ModeUplink)
			m := f.mandate(q)
			m.QuoteID = "q-000000000000000000000000"
			return f.opsCtx, m.QuoteID, sign(t, m, f.authKey)
		}, errs.MandateRejectedQuote},
		{"6 scope: downlink mandate used for uplink", func(t *testing.T) (context.Context, string, string) {
			q := f.quote(t, schema.ModeUplink)
			m := f.mandate(q)
			m.Scope = schema.Scope(schema.ModeDownlink, norad)
			return f.opsCtx, q.QuoteID, sign(t, m, f.authKey)
		}, errs.MandateRejectedScope},
		{"7 amount: signed below the quote", func(t *testing.T) (context.Context, string, string) {
			q := f.quote(t, schema.ModeUplink)
			m := f.mandate(q)
			m.MaxAmountCents = q.AmountCents - 1
			return f.opsCtx, q.QuoteID, sign(t, m, f.authKey)
		}, errs.MandateRejectedAmount},
		{"8 window: widened", func(t *testing.T) (context.Context, string, string) {
			q := f.quote(t, schema.ModeUplink)
			m := f.mandate(q)
			m.Exp = q.LOS + 600
			return f.opsCtx, q.QuoteID, sign(t, m, f.authKey)
		}, errs.MandateRejectedWindow},
		{"9 DPoP key: another key", func(t *testing.T) (context.Context, string, string) {
			q := f.quote(t, schema.ModeUplink)
			other, _ := jose.Thumbprint(&newKey(t).PublicKey)
			return withCaller(context.Background(), Caller{ANSName: opsName, JKT: other}), q.QuoteID, sign(t, f.mandate(q), f.authKey)
		}, errs.DPoPRejectedKey},
		{"9 DPoP key: no proven caller", func(t *testing.T) (context.Context, string, string) {
			q := f.quote(t, schema.ModeUplink)
			return context.Background(), q.QuoteID, sign(t, f.mandate(q), f.authKey)
		}, errs.DPoPRejectedKey},
		{"11 overlap: a second quote on a booked window", func(t *testing.T) (context.Context, string, string) {
			first := f.quote(t, schema.ModeUplink)
			if _, err := f.book(f.opsCtx, first.QuoteID, sign(t, f.mandate(first), f.authKey)); err != nil {
				t.Fatal(err)
			}
			second := f.quoteAt(t, schema.ModeUplink, first.AOS+60, first.LOS+60)
			return f.opsCtx, second.QuoteID, sign(t, f.mandate(second), f.authKey)
		}, errs.BookingRejectedOverlap},
		{"12 consumed: replay a spent mandate with a fresh proof", func(t *testing.T) (context.Context, string, string) {
			q := f.quote(t, schema.ModeUplink)
			tok := sign(t, f.mandate(q), f.authKey)
			if _, err := f.book(f.opsCtx, q.QuoteID, tok); err != nil {
				t.Fatal(err)
			}
			return f.opsCtx, q.QuoteID, tok
		}, errs.MandateRejectedConsumed},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			ctx, qid, tok := r.setup(t)
			_, err := f.book(ctx, qid, tok)
			wantCode(t, err, r.code)
		})
	}
	// Window expiry (8) with a clock past exp.
	t.Run("8 window: expired", func(t *testing.T) {
		q := f.quote(t, schema.ModeUplink)
		tok := sign(t, f.mandate(q), f.authKey)
		saved := f.st.Now
		f.st.Now = func() time.Time { return time.Unix(q.LOS+1, 0) }
		defer func() { f.st.Now = saved }()
		_, err := f.book(f.opsCtx, q.QuoteID, tok)
		wantCode(t, err, errs.MandateRejectedWindow)
	})
}

func flip(s string) string {
	b := []byte(s)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	return string(b)
}

// tamper edits the payload of a signed mandate and keeps the old signature.
func tamper(t *testing.T, tok string, edit func(map[string]any)) string {
	t.Helper()
	parts := strings.Split(tok, ".")
	raw, _ := jose.B64Decode(parts[1])
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	edit(m)
	payload, _ := jose.Canonicalize(m)
	return parts[0] + "." + jose.B64Encode(payload) + "." + parts[2]
}

// Acceptance 3: two concurrent bookings for overlapping windows, exactly one wins.
func TestConcurrentOverlap(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 50; i++ {
		a := f.quote(t, schema.ModeUplink)
		b := f.quoteAt(t, schema.ModeUplink, a.AOS+30, a.LOS+30)
		toks := []string{sign(t, f.mandate(a), f.authKey), sign(t, f.mandate(b), f.authKey)}
		ids := []string{a.QuoteID, b.QuoteID}
		var wins, overlaps atomic.Int32
		var wg sync.WaitGroup
		for j := 0; j < 2; j++ {
			wg.Add(1)
			go func(j int) {
				defer wg.Done()
				_, err := f.book(f.opsCtx, ids[j], toks[j])
				switch {
				case err == nil:
					wins.Add(1)
				case errs.Is(err, errs.BookingRejectedOverlap):
					overlaps.Add(1)
				default:
					t.Errorf("iteration %d: %v", i, err)
				}
			}(j)
		}
		wg.Wait()
		if wins.Load() != 1 || overlaps.Load() != 1 {
			t.Fatalf("iteration %d: %d wins, %d overlaps", i, wins.Load(), overlaps.Load())
		}
	}
}

// Acceptance 4: garbage, oversized and truncated inputs get named codes.
func TestHostileInputs(t *testing.T) {
	f := newFixture(t)
	q := f.quote(t, schema.ModeUplink)
	tok := sign(t, f.mandate(q), f.authKey)
	book := []string{``, `null`, `[]`, `{"skill":"book_pass"`, `{"skill":"book_pass","quote_id":1}`,
		`{"skill":"book_pass","quote_id":"` + q.QuoteID + `","mandate":"` + tok[:len(tok)/2] + `"}`,
		`{"skill":"book_pass","quote_id":"x","mandate":"` + strings.Repeat("A", schema.MaxObjectBytes) + `"}`,
		`{"skill":"book_pass","quote_id":"x","mandate":"a.b.c","extra":1}`,
		`{"skill":"book_pass","quote_id":"x","mandate":"a.b.c","n":1.5}`}
	for _, in := range book {
		_, err := f.st.BookPass(f.opsCtx, json.RawMessage(in))
		if err == nil || !named(err) {
			t.Errorf("book_pass(%.60q) = %v", in, err)
		}
	}
	quote := []string{``, `{}`, `{"skill":"get_pass_quote","norad_id":27844}`,
		fmt.Sprintf(`{"skill":"get_pass_quote","norad_id":1,"aos":%d,"los":%d,"mode":"uplink"}`, now.Unix()+60, now.Unix()+300),
		fmt.Sprintf(`{"skill":"get_pass_quote","norad_id":27844,"aos":%d,"los":%d,"mode":"uplink"}`, now.Unix()-60, now.Unix()+300),
		fmt.Sprintf(`{"skill":"get_pass_quote","norad_id":27844,"aos":%d,"los":%d,"mode":"uplink"}`, now.Unix()+60, now.Unix()+6000),
		fmt.Sprintf(`{"skill":"get_pass_quote","norad_id":27844,"aos":%d,"los":%d,"mode":"sideways"}`, now.Unix()+60, now.Unix()+300)}
	for _, in := range quote {
		_, err := f.st.GetPassQuote(f.opsCtx, json.RawMessage(in))
		if err == nil || !named(err) {
			t.Errorf("get_pass_quote(%.60q) = %v", in, err)
		}
	}
}

func named(err error) bool {
	var e *errs.Error
	return errs.As(err, &e) && e.Code != ""
}

// FuzzBookPass: arbitrary arguments never panic and only return named codes.
func FuzzBookPass(f *testing.F) {
	fx := newFixture(f)
	q := fx.quote(f, schema.ModeUplink)
	tok := sign(f, fx.mandate(q), fx.authKey)
	good, _ := json.Marshal(map[string]string{"skill": "book_pass", "quote_id": q.QuoteID, "mandate": tok})
	f.Add(string(good))
	f.Add(`{"skill":"book_pass"}`)
	f.Fuzz(func(t *testing.T, in string) {
		_, err := fx.st.BookPass(fx.opsCtx, json.RawMessage(in))
		if err != nil && !named(err) {
			t.Fatalf("unnamed error %v", err)
		}
	})
}

// Acceptance 6: payTo in every quote equals the configured payTo that the
// signed card carries (see cmd/agent TestQuotePayToMatchesServedCard).
func TestPayToMatchesPricing(t *testing.T) {
	f := newFixture(t)
	for _, mode := range []string{schema.ModeUplink, schema.ModeDownlink} {
		q := f.quote(t, mode)
		if len(q.Accepts) != 1 || q.Accepts[0].PayTo != f.pricing.PayTo || q.Accepts[0].Amount != q.AmountCents*10_000 {
			t.Fatalf("accepts %+v", q.Accepts)
		}
		if q.AmountCents != 8*150 || q.Exp != now.Add(QuoteTTL).Unix() {
			t.Fatalf("price %d exp %d", q.AmountCents, q.Exp)
		}
	}
}

// Rogue mode skips the signature and DPoP-key checks, so a flipped
// signature is booked: the vulnerability the auditor's canary finds.
func TestRogueSkipsChecks(t *testing.T) {
	f := newFixture(t)
	f.st.Rogue = true
	q := f.quote(t, schema.ModeUplink)
	tok := sign(t, f.mandate(q), f.authKey)
	bad := tok[:len(tok)-2] + flip(tok[len(tok)-2:])
	if _, err := f.book(context.Background(), q.QuoteID, bad); err != nil {
		t.Fatalf("rogue refused: %v", err)
	}
}

// Review H1: another registered authority reusing a mandate_id (public in
// receipts) with its own nonce cannot book an overlapping window.
func TestMandateIDReuseStillOverlaps(t *testing.T) {
	f := newFixture(t)
	q := f.quote(t, schema.ModeUplink)
	m := f.mandate(q)
	if _, err := f.book(f.opsCtx, q.QuoteID, sign(t, m, f.authKey)); err != nil {
		t.Fatal(err)
	}
	q2 := f.quoteFor(t, 99999, schema.ModeUplink, q.AOS+60, q.LOS+60)
	m2 := f.mandate(q2)
	m2.Iss, m2.MandateID = otherAuth, m.MandateID
	_, err := f.book(f.opsCtx, q2.QuoteID, sign(t, m2, f.otherKey))
	wantCode(t, err, errs.BookingRejectedOverlap)
}

// Review H2: nonces are scoped to their issuer, so another authority cannot
// burn one first.
func TestNonceScopedToIssuer(t *testing.T) {
	f := newFixture(t)
	victimQ := f.quote(t, schema.ModeUplink)
	victim := f.mandate(victimQ)
	squatQ := f.quoteFor(t, 99999, schema.ModeUplink, victimQ.AOS+7200, victimQ.LOS+7200)
	squat := f.mandate(squatQ)
	squat.Iss, squat.Nonce = otherAuth, victim.Nonce
	if _, err := f.book(f.opsCtx, squatQ.QuoteID, sign(t, squat, f.otherKey)); err != nil {
		t.Fatalf("squat: %v", err)
	}
	if _, err := f.book(f.opsCtx, victimQ.QuoteID, sign(t, victim, f.authKey)); err != nil {
		t.Fatalf("victim refused: %v", err)
	}
}

// Review M3: a spent mandate replayed after the quote's 10-minute TTL (but
// before LOS) is still MANDATE_REJECTED:consumed, not quote.
func TestConsumedAfterQuoteTTL(t *testing.T) {
	f := newFixture(t)
	q := f.quote(t, schema.ModeUplink)
	tok := sign(t, f.mandate(q), f.authKey)
	if _, err := f.book(f.opsCtx, q.QuoteID, tok); err != nil {
		t.Fatal(err)
	}
	later := time.Unix(q.Exp+60, 0)
	f.st.Now = func() time.Time { return later }
	_, err := f.book(f.opsCtx, q.QuoteID, tok)
	wantCode(t, err, errs.MandateRejectedConsumed)
}

// Review M4: prices that would overflow the asset amount are refused by name.
func TestPriceOverflowRefused(t *testing.T) {
	f := newFixture(t)
	f.st.Pricing.PerMinuteCents = 1_000_000
	f.st.Pricing.AssetDecimals = 18
	args := fmt.Sprintf(`{"skill":"get_pass_quote","norad_id":%d,"aos":%d,"los":%d,"mode":"uplink","max_elevation_deg":45}`, norad, now.Unix()+600, now.Unix()+1800)
	_, err := f.st.GetPassQuote(f.opsCtx, json.RawMessage(args))
	wantCode(t, err, errs.QuoteRejectedWindow)
}

// Review L7: a rogue station reads a high-S (corrupted) mandate's payload and
// books it: the canary's corrupted signature is what exposes it.
func TestRogueAcceptsHighS(t *testing.T) {
	f := newFixture(t)
	f.st.Rogue = true
	q := f.quote(t, schema.ModeUplink)
	tok := sign(t, f.mandate(q), f.authKey)
	parts := strings.Split(tok, ".")
	sig, _ := jose.B64Decode(parts[2])
	s := new(big.Int).SetBytes(sig[32:])
	s.Sub(elliptic.P256().Params().N, s)
	s.FillBytes(sig[32:])
	high := parts[0] + "." + parts[1] + "." + jose.B64Encode(sig)
	if _, err := f.book(context.Background(), q.QuoteID, high); err != nil {
		t.Fatalf("rogue refused a high-S mandate: %v", err)
	}
	f2 := newFixture(t)
	q2 := f2.quote(t, schema.ModeUplink)
	tok2 := sign(t, f2.mandate(q2), f2.authKey)
	p2 := strings.Split(tok2, ".")
	sig2, _ := jose.B64Decode(p2[2])
	s2 := new(big.Int).SetBytes(sig2[32:])
	s2.Sub(elliptic.P256().Params().N, s2)
	s2.FillBytes(sig2[32:])
	_, err := f2.book(f2.opsCtx, q2.QuoteID, p2[0]+"."+p2[1]+"."+jose.B64Encode(sig2))
	wantCode(t, err, errs.MandateRejectedSignature)
}
