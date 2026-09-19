package schema

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/jose"
)

const (
	aos = int64(1_760_000_000)
	los = aos + 480
)

var hex64 = strings.Repeat("ab", 32)

func newKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func wantCode(t testing.TB, err error, code errs.Code) {
	t.Helper()
	if !errs.Is(err, code) {
		t.Fatalf("err = %v, want %s", err, code)
	}
}

func sampleQuote() Quote {
	return Quote{QuoteID: "q-1", Station: "gs-blacksburg.overpass.example", NoradID: 25544,
		AOS: aos, LOS: los, MaxElevationDeg: 62, Mode: ModeUplink, AmountCents: 1200,
		Accepts: []Accept{{Scheme: "exact", Network: "base-sepolia", PayTo: "0xabc", Asset: "USDC", Amount: 12000000}},
		Exp:     aos - 60}
}

func sampleMandate() Mandate {
	return Mandate{MandateID: "m-1", Iss: "authority.overpass.example", Sub: "ops.overpass.example",
		Aud: "gs-blacksburg.overpass.example", QuoteID: "q-1", Scope: Scope(ModeUplink, 25544),
		CommandClasses: []string{"telemetry", "attitude"}, MaxAmountCents: 1500, Nbf: aos, Exp: los,
		JKT: strings.Repeat("A", 43), Nonce: "bm9uY2Utbm9uY2Utbm9uY2U"}
}

func sampleCommand() Command {
	return Command{NoradID: 25544, Counter: 7, MandateID: "m-1", Class: "telemetry",
		Body: json.RawMessage(`{"op":"dump","since":1760000000}`), IssuedAt: aos + 10}
}

func sampleSatReg() SatRegistry {
	return SatRegistry{Iss: "authority.overpass.example", Iat: aos - 86400, Satellites: []Satellite{
		{NoradID: 25544, AuthorityANSName: "authority.overpass.example", OpsANSNames: []string{"ops.overpass.example"}}}}
}

func sampleAudit() AuditReport {
	return AuditReport{PassID: "b-1", Iss: "auditor.overpass.example", Station: "gs-rogue.overpass.example",
		Iat: los + 60, Checks: []Check{{Name: "identity", Result: Pass}},
		Canaries:  []Canary{{Probe: "flipped_signature", Result: Accepted, Code: "CANARY_ACCEPTED"}},
		ChainHead: hex64, StationChainHead: hex64, MissingAcks: 0, Verdict: Fail}
}

func sampleReceipt() BookingReceipt {
	return BookingReceipt{BookingID: "b-1", Station: "gs-blacksburg.overpass.example", MandateID: "m-1",
		QuoteID: "q-1", NoradID: 25544, Nbf: aos, Exp: los, AmountCents: 1200, Iat: aos - 300}
}

// signed describes one signed type generically for the table tests.
type signed struct {
	name   string
	prof   jose.Profile
	sign   func(*ecdsa.PrivateKey) (string, error)
	verify func(string, []*ecdsa.PublicKey) (any, error)
	want   any
}

func signedTypes() []signed {
	m, sr, c, a, r := sampleMandate(), sampleSatReg(), sampleCommand(), sampleAudit(), sampleReceipt()
	return []signed{
		{"mandate", MandateProfile, func(k *ecdsa.PrivateKey) (string, error) { return SignMandate(m, k) },
			func(s string, k []*ecdsa.PublicKey) (any, error) { return VerifyMandate(s, k) }, m},
		{"satreg", SatRegProfile, func(k *ecdsa.PrivateKey) (string, error) { return SignSatRegistry(sr, k) },
			func(s string, k []*ecdsa.PublicKey) (any, error) { return VerifySatRegistry(s, k) }, sr},
		{"command", CommandProfile, func(k *ecdsa.PrivateKey) (string, error) { return SignCommand(c, k) },
			func(s string, k []*ecdsa.PublicKey) (any, error) { return VerifyCommand(s, k) }, c},
		{"audit", AuditProfile, func(k *ecdsa.PrivateKey) (string, error) { return SignAuditReport(a, k) },
			func(s string, k []*ecdsa.PublicKey) (any, error) { return VerifyAuditReport(s, k) }, a},
		{"receipt", ReceiptProfile, func(k *ecdsa.PrivateKey) (string, error) { return SignBookingReceipt(r, k) },
			func(s string, k []*ecdsa.PublicKey) (any, error) { return VerifyBookingReceipt(s, k) }, r},
	}
}

// Acceptance 2, round trip: sign then verify returns the same object.
func TestSignVerifyRoundTrip(t *testing.T) {
	key := newKey(t)
	keys := []*ecdsa.PublicKey{&key.PublicKey}
	for _, s := range signedTypes() {
		t.Run(s.name, func(t *testing.T) {
			tok, err := s.sign(key)
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.verify(tok, keys)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, s.want) {
				t.Fatalf("got %+v\nwant %+v", got, s.want)
			}
		})
	}
}

// Acceptance 2, tamper: changing any byte of the decoded payload or the
// signature fails with that object's named code and returns a zero value.
func TestTamperEveryByte(t *testing.T) {
	key := newKey(t)
	keys := []*ecdsa.PublicKey{&key.PublicKey}
	enc := base64.RawURLEncoding
	for _, s := range signedTypes() {
		t.Run(s.name, func(t *testing.T) {
			tok, _ := s.sign(key)
			parts := strings.Split(tok, ".")
			for seg := 1; seg <= 2; seg++ {
				raw, _ := enc.DecodeString(parts[seg])
				for i := 0; i < 2*len(raw); i++ {
					mut := append([]byte(nil), raw...)
					mut[i/2] ^= []byte{0x01, 0x80}[i%2]
					p := append([]string(nil), parts...)
					p[seg] = enc.EncodeToString(mut)
					got, err := s.verify(strings.Join(p, "."), keys)
					if !errs.Is(err, s.prof.ParseCode) && !errs.Is(err, s.prof.SigCode) {
						t.Fatalf("segment %d byte %d: err = %v", seg, i, err)
					}
					if !reflect.ValueOf(got).IsZero() {
						t.Fatalf("segment %d byte %d: non-zero value returned on error", seg, i)
					}
				}
			}
		})
	}
}

// Acceptance 3: a mandate presented as a command is TYP_REJECTED, and every
// other cross-type presentation too.
func TestTypConfusion(t *testing.T) {
	key := newKey(t)
	keys := []*ecdsa.PublicKey{&key.PublicKey}
	types := signedTypes()
	for _, from := range types {
		tok, _ := from.sign(key)
		for _, to := range types {
			if from.name == to.name {
				continue
			}
			if _, err := to.verify(tok, keys); !errs.Is(err, errs.TypRejected) {
				t.Errorf("%s presented as %s: err = %v", from.name, to.name, err)
			}
		}
	}
	// A real agent-card header carries jku; it must still be TYP_REJECTED.
	card, _ := jose.SignDetached(jose.TypAgentCard, []byte(`{}`), key, "https://x.example/tc.json")
	parts := strings.Split(card, ".")
	cardTok := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"a":1}`)) + "." + parts[2]
	for _, to := range types {
		if _, err := to.verify(cardTok, keys); !errs.Is(err, errs.TypRejected) {
			t.Errorf("agent card presented as %s: err = %v", to.name, err)
		}
	}
}

// Acceptance 4: 100 and 100.0 are not both accepted.
func TestFloatRejectedAtDecode(t *testing.T) {
	good, _ := json.Marshal(sampleQuote())
	if _, err := DecodeQuote(good); err != nil {
		t.Fatalf("integer form rejected: %v", err)
	}
	for _, f := range []string{"1200.0", "1.2e3", "1.2E3", "12e2"} {
		bad := strings.Replace(string(good), `"amount_cents":1200`, `"amount_cents":`+f, 1)
		_, err := DecodeQuote([]byte(bad))
		wantCode(t, err, errs.QuoteParseError)
	}

	// The same probe on a signed mandate (canonicalization_probe, battery 13a):
	// re-sign a payload whose amount is written 1500.0.
	key := newKey(t)
	payload, _ := jose.Canonicalize(sampleMandate())
	floaty := strings.Replace(string(payload), `"max_amount_cents":1500`, `"max_amount_cents":1500.0`, 1)
	tok, _ := jose.Sign(jose.TypMandate, []byte(floaty), key, "")
	_, err := VerifyMandate(tok, []*ecdsa.PublicKey{&key.PublicKey})
	wantCode(t, err, errs.MandateParseError)
}

// superseded_format_attack (battery 10): strip scope, jkt and the signature.
func TestSupersededFormat(t *testing.T) {
	key := newKey(t)
	keys := []*ecdsa.PublicKey{&key.PublicKey}
	tok, _ := SignMandate(sampleMandate(), key)
	parts := strings.Split(tok, ".")
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var obj map[string]any
	_ = json.Unmarshal(payload, &obj)
	delete(obj, "scope")
	delete(obj, "jkt")
	stripped, _ := jose.Canonicalize(obj)
	for name, tk := range map[string]string{
		"no scope/jkt, old sig": parts[0] + "." + base64.RawURLEncoding.EncodeToString(stripped) + "." + parts[2],
		"no signature":          parts[0] + "." + parts[1] + ".",
	} {
		_, err := VerifyMandate(tk, keys)
		if !errs.Is(err, errs.MandateParseError) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestUnknownKeyIsSignatureRejection(t *testing.T) {
	tok, _ := SignMandate(sampleMandate(), newKey(t))
	_, err := VerifyMandate(tok, []*ecdsa.PublicKey{&newKey(t).PublicKey})
	wantCode(t, err, errs.MandateRejectedSignature)
}

func TestSignedPayloadMustBeCanonical(t *testing.T) {
	key := newKey(t)
	raw, _ := json.MarshalIndent(sampleMandate(), "", " ")
	tok, _ := jose.Sign(jose.TypMandate, raw, key, "")
	_, err := VerifyMandate(tok, []*ecdsa.PublicKey{&key.PublicKey})
	wantCode(t, err, errs.MandateParseError)
}

func TestStrictDecode(t *testing.T) {
	good, _ := json.Marshal(sampleQuote())
	s := string(good)
	cases := map[string]string{
		"unknown field":   strings.Replace(s, `{`, `{"extra":1,`, 1),
		"case variant":    strings.Replace(s, `"quote_id"`, `"Quote_ID"`, 1),
		"missing field":   strings.Replace(s, `"mode":"uplink",`, ``, 1),
		"duplicate key":   strings.Replace(s, `{`, `{"mode":"downlink",`, 1),
		"bad mode":        strings.Replace(s, `"mode":"uplink"`, `"mode":"sideways"`, 1),
		"los before aos":  strings.Replace(s, `"los":1760000480`, `"los":1`, 1),
		"trailing data":   s + "{}",
		"not an object":   `[1,2]`,
		"huge":            `{"quote_id":"` + strings.Repeat("x", MaxObjectBytes) + `"}`,
		"empty":           ``,
		"string for int":  strings.Replace(s, `"norad_id":25544`, `"norad_id":"25544"`, 1),
		"above 2^53":      strings.Replace(s, `"norad_id":25544`, `"norad_id":9007199254740993`, 1),
		"negative amount": strings.Replace(s, `"amount_cents":1200`, `"amount_cents":-1`, 1),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			q, err := DecodeQuote([]byte(in))
			wantCode(t, err, errs.QuoteParseError)
			if !reflect.ValueOf(q).IsZero() {
				t.Fatal("non-zero quote returned on error")
			}
		})
	}
}

func TestAckAndRecordDecode(t *testing.T) {
	if _, err := DecodeAck([]byte(`{"counter":3,"result":"accepted","telemetry_sha256":"` + hex64 + `"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeAck([]byte(`{"counter":3,"reason":"COMMAND_REJECTED:signature","result":"rejected"}`)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"counter":3,"result":"accepted"}`,
		`{"counter":3,"result":"maybe","reason":"x"}`,
		`{"counter":0,"result":"rejected","reason":"x"}`,
	} {
		_, err := DecodeAck([]byte(bad))
		wantCode(t, err, errs.AckParseError)
	}
	for _, bad := range []string{
		`{"counter":3,"reason":"X","result":"rejected","telemetry_sha256":"` + hex64 + `"}`,
		`{"counter":3,"reason":"X","result":"accepted","telemetry_sha256":"` + hex64 + `"}`,
	} {
		_, err := DecodeAck([]byte(bad))
		wantCode(t, err, errs.AckParseError)
	}
	_, err := DecodeCommandRecord([]byte(`{"counter":1}`))
	wantCode(t, err, errs.RecordParseError)
}

func TestValidateRejectsBadFields(t *testing.T) {
	cases := map[string]Object{
		"mandate scope":   func() *Mandate { m := sampleMandate(); m.Scope = "pass:uplink:x"; return &m }(),
		"mandate jkt":     func() *Mandate { m := sampleMandate(); m.JKT = ""; return &m }(),
		"mandate classes": func() *Mandate { m := sampleMandate(); m.CommandClasses = []string{"a", "a"}; return &m }(),
		"mandate window":  func() *Mandate { m := sampleMandate(); m.Exp = m.Nbf; return &m }(),
		"command body":    func() *Command { c := sampleCommand(); c.Body = json.RawMessage(`[1]`); return &c }(),
		"command counter": func() *Command { c := sampleCommand(); c.Counter = 0; return &c }(),
		"satreg dup norad": func() *SatRegistry {
			r := sampleSatReg()
			r.Satellites = append(r.Satellites, r.Satellites[0])
			return &r
		}(),
		"satreg no ops":    func() *SatRegistry { r := sampleSatReg(); r.Satellites[0].OpsANSNames = nil; return &r }(),
		"audit verdict":    func() *AuditReport { a := sampleAudit(); a.Verdict = "meh"; return &a }(),
		"audit head":       func() *AuditReport { a := sampleAudit(); a.ChainHead = "ABC"; return &a }(),
		"audit nil checks": func() *AuditReport { a := sampleAudit(); a.Checks = nil; return &a }(),
		"audit nil canary": func() *AuditReport { a := sampleAudit(); a.Canaries = nil; return &a }(),
		"receipt station":  func() *BookingReceipt { b := sampleReceipt(); b.Station = "has space"; return &b }(),
		"quote elevation":  func() *Quote { q := sampleQuote(); q.MaxElevationDeg = 91; return &q }(),
		"quote no accepts": func() *Quote { q := sampleQuote(); q.Accepts = nil; return &q }(),
	}
	for name, o := range cases {
		if o.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := SignMandate(Mandate{}, newKey(t)); err == nil {
		t.Error("signed an invalid mandate")
	}
}

func TestScope(t *testing.T) {
	mode, n, err := ParseScope("pass:downlink:43013")
	if err != nil || mode != ModeDownlink || n != 43013 {
		t.Fatalf("%s %d %v", mode, n, err)
	}
	for _, bad := range []string{"", "pass:uplink", "pass:uplink:+5", "pass:uplink:05", "cmd:uplink:5", "pass:up:5", "pass:uplink:0"} {
		if _, _, err := ParseScope(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// Acceptance 2, fuzz: the verifiers never panic and only return named codes.
func fuzzVerifier(f *testing.F, prof jose.Profile, sign func(*ecdsa.PrivateKey) (string, error),
	verify func(string, []*ecdsa.PublicKey) error) {
	key := newKey(f)
	tok, err := sign(key)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(tok)
	f.Add(strings.Replace(tok, ".", "..", 1))
	f.Add(tok[:len(tok)-2] + "AA")
	f.Add("")
	// Seeds for header and signature shapes the mutator rarely reaches.
	parts := strings.Split(tok, ".")
	enc := base64.RawURLEncoding.EncodeToString
	card, _ := jose.SignDetached(jose.TypAgentCard, []byte(`{}`), key, "https://x.example/tc.json")
	f.Add(strings.Split(card, ".")[0] + "." + parts[1] + "." + parts[2])
	f.Add(enc([]byte(`{"alg":"ES256","crit":["b64"],"kid":"k","typ":"`+prof.Typ+`"}`)) + "." + parts[1] + "." + parts[2])
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	s := new(big.Int).SetBytes(sig[32:])
	s.Sub(elliptic.P256().Params().N, s)
	s.FillBytes(sig[32:])
	f.Add(parts[0] + "." + parts[1] + "." + enc(sig))
	keys := []*ecdsa.PublicKey{&key.PublicKey}
	f.Fuzz(func(t *testing.T, s string) {
		err := verify(s, keys)
		if s == tok {
			if err != nil {
				t.Fatalf("original token rejected: %v", err)
			}
			return
		}
		if err == nil {
			// Strict base64url gives each JWS exactly one encoding.
			t.Fatalf("modified token verified: %q", s)
		}
		if !errs.Is(err, prof.ParseCode) && !errs.Is(err, prof.SigCode) && !errs.Is(err, errs.TypRejected) {
			t.Fatalf("unnamed error for %q: %v", s, err)
		}
	})
}

func FuzzVerifyMandate(f *testing.F) {
	fuzzVerifier(f, MandateProfile, func(k *ecdsa.PrivateKey) (string, error) { return SignMandate(sampleMandate(), k) },
		func(s string, k []*ecdsa.PublicKey) error { _, err := VerifyMandate(s, k); return err })
}

func FuzzVerifyCommand(f *testing.F) {
	fuzzVerifier(f, CommandProfile, func(k *ecdsa.PrivateKey) (string, error) { return SignCommand(sampleCommand(), k) },
		func(s string, k []*ecdsa.PublicKey) error { _, err := VerifyCommand(s, k); return err })
}
