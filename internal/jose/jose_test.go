package jose

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentnameservice/ans-sdk-go/pop"

	"github.com/arjitsama/overpass/internal/errs"
)

var testProfile = Profile{Typ: TypMandate, ParseCode: errs.MandateParseError, SigCode: errs.MandateRejectedSignature}

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

// Acceptance 1: RFC 8785 sample vectors.
func TestJCSVectors(t *testing.T) {
	files, err := filepath.Glob("testdata/jcs/input/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no vectors: %v", err)
	}
	for _, in := range files {
		name := filepath.Base(in)
		t.Run(name, func(t *testing.T) {
			raw, _ := os.ReadFile(in)
			want, _ := os.ReadFile(filepath.Join("testdata/jcs/output", name))
			got, err := Transform(raw)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, bytes.TrimRight(want, "\n")) {
				t.Fatalf("got  %s\nwant %s", got, want)
			}
		})
	}
}

func TestCanonicalizeSortsAndCompacts(t *testing.T) {
	got, err := Canonicalize(map[string]any{"b": 2, "a": []int{1, 2}, "c": "x"})
	if err != nil || string(got) != `{"a":[1,2],"b":2,"c":"x"}` {
		t.Fatalf("got %s, %v", got, err)
	}
}

// Acceptance 4 at the JSON layer: 100 is accepted, every float spelling is not.
func TestStrictJSONIntegersOnly(t *testing.T) {
	ok := []string{`{"a":100}`, `{"a":-5}`, `{"a":0}`, `[9007199254740991]`, `{"a":{"b":[1,"x",true,null]}}`}
	bad := []string{`{"a":100.0}`, `{"a":1e2}`, `{"a":1E2}`, `{"a":-0}`, `{"a":0.5}`, `{"a":007}`,
		`[9007199254740992]`, `{"a":1,"a":2}`, `{"a":1} {}`, `{"a":1}x`, "{\"a\":\"\xff\"}", `{"a":`,
		strings.Repeat("[", 40) + strings.Repeat("]", 40)}
	for _, s := range ok {
		if err := StrictJSON([]byte(s), 1024); err != nil {
			t.Errorf("%s rejected: %v", s, err)
		}
	}
	for _, s := range bad {
		if err := StrictJSON([]byte(s), 1024); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
	if err := StrictJSON([]byte(`{"a":"`+strings.Repeat("x", 100)+`"}`), 50); err == nil {
		t.Error("size cap not enforced")
	}
}

// Acceptance 6: RFC 7638 section 3.1 example.
func TestThumbprintRFC7638(t *testing.T) {
	n, err := base64.RawURLEncoding.DecodeString("0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAt" +
		"VT86zwu1RK7aPFFxuhDR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMstn6" +
		"4tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6Cf0h4QyQ5v-65YGjQR0_FD" +
		"W2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n9" +
		"1CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINH" +
		"aQ-G_xBniIqbw0Ls1jF44-csFCur-kEgU8awapJzKnqDKgw")
	if err != nil {
		t.Fatal(err)
	}
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: 65537}
	got, err := Thumbprint(pub)
	if err != nil || got != "NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs" {
		t.Fatalf("got %s, %v", got, err)
	}
}

// The mandate's jkt is compared against the DPoP key the SDK sees, so our EC
// thumbprint must equal ans-sdk-go's pop.Signer.JKT for the same key.
func TestThumbprintMatchesSDK(t *testing.T) {
	key := newKey(t)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ops"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	s, err := pop.NewSigner(key, der)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := Thumbprint(&key.PublicKey)
	if got != s.JKT() {
		t.Fatalf("ours %s, sdk %s", got, s.JKT())
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	key := newKey(t)
	payload := []byte(`{"a":1}`)
	tok, err := Sign(TypMandate, payload, key, "")
	if err != nil {
		t.Fatal(err)
	}
	var seen []byte
	got, err := Verify(tok, testProfile, []*ecdsa.PublicKey{&newKey(t).PublicKey, &key.PublicKey},
		func(b []byte) error { seen = b; return nil })
	if err != nil || !bytes.Equal(got, payload) || !bytes.Equal(seen, payload) {
		t.Fatalf("got %s, %v", got, err)
	}
	hdr, _ := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[0])
	kid, _ := Thumbprint(&key.PublicKey)
	want := `{"alg":"ES256","kid":"` + kid + `","typ":"overpass-mandate+jws"}`
	if c, _ := Transform(hdr); string(c) != want {
		t.Fatalf("header %s, want %s", c, want)
	}
}

// Acceptance 3 at the JOSE layer: typ is checked before the signature, even
// when the signature and key are valid.
func TestWrongTypRejectedFirst(t *testing.T) {
	key := newKey(t)
	tok, _ := Sign(TypMandate, []byte(`{"a":1}`), key, "")
	called := false
	cmd := Profile{Typ: TypCommand, ParseCode: errs.CommandParseError, SigCode: errs.CommandRejectedSignature}
	_, err := Verify(tok, cmd, []*ecdsa.PublicKey{&key.PublicKey}, func([]byte) error { called = true; return nil })
	wantCode(t, err, errs.TypRejected)
	if called {
		t.Fatal("payload decoded before typ check")
	}
}

func TestVerifyRejections(t *testing.T) {
	key := newKey(t)
	good, _ := Sign(TypMandate, []byte(`{"a":1}`), key, "")
	keys := []*ecdsa.PublicKey{&key.PublicKey}
	parts := strings.Split(good, ".")
	enc := base64.RawURLEncoding.EncodeToString
	other, _ := Sign(TypMandate, []byte(`{"a":1}`), newKey(t), "")
	jku, _ := Sign(TypMandate, []byte(`{"a":1}`), key, "https://evil.example/keys")

	cases := map[string]struct {
		tok  string
		code errs.Code
	}{
		"empty":           {"", errs.MandateParseError},
		"two segments":    {parts[0] + "." + parts[1], errs.MandateParseError},
		"no signature":    {parts[0] + "." + parts[1] + ".", errs.MandateParseError},
		"padded b64":      {parts[0] + "." + parts[1] + "=." + parts[2], errs.MandateParseError},
		"newline":         {parts[0] + ".\n" + parts[1] + "." + parts[2], errs.MandateParseError},
		"unknown key":     {other, errs.MandateRejectedSignature},
		"jku not allowed": {jku, errs.MandateParseError},
		"alg none":        {enc([]byte(`{"alg":"none","kid":"x","typ":"overpass-mandate+jws"}`)) + "." + parts[1] + "." + parts[2], errs.MandateParseError},
		"extra header":    {enc([]byte(`{"alg":"ES256","crit":["b64"],"kid":"x","typ":"overpass-mandate+jws"}`)) + "." + parts[1] + "." + parts[2], errs.MandateParseError},
		"case header":     {enc([]byte(`{"ALG":"ES256","kid":"x","typ":"overpass-mandate+jws"}`)) + "." + parts[1] + "." + parts[2], errs.MandateParseError},
		"swapped payload": {parts[0] + "." + enc([]byte(`{"a":2}`)) + "." + parts[2], errs.MandateRejectedSignature},
		"oversize":        {strings.Repeat("a", MaxTokenBytes+1), errs.MandateParseError},
		"header not json": {enc([]byte("hi")) + "." + parts[1] + "." + parts[2], errs.MandateParseError},
		"decoder rejects": {good, errs.MandateParseError},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var dec func([]byte) error
			if name == "decoder rejects" {
				dec = func([]byte) error { return os.ErrInvalid }
			}
			_, err := Verify(c.tok, testProfile, keys, dec)
			wantCode(t, err, c.code)
		})
	}
}

// Changing any single character of a token must never verify. Strict base64
// matters here: a lenient decoder ignores the last character's spare bits.
func TestEveryByteFlipFails(t *testing.T) {
	key := newKey(t)
	tok, _ := Sign(TypMandate, []byte(`{"a":1,"b":"xyz"}`), key, "")
	keys := []*ecdsa.PublicKey{&key.PublicKey}
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for i := range tok {
		if tok[i] == '.' {
			continue
		}
		for _, repl := range []byte{alphabet[(strings.IndexByte(alphabet, tok[i])+1)%64], alphabet[(strings.IndexByte(alphabet, tok[i])+17)%64]} {
			mut := tok[:i] + string(repl) + tok[i+1:]
			_, err := Verify(mut, testProfile, keys, nil)
			if err == nil {
				t.Fatalf("flip at %d verified", i)
			}
			if !errs.Is(err, errs.MandateParseError) && !errs.Is(err, errs.MandateRejectedSignature) && !errs.Is(err, errs.TypRejected) {
				t.Fatalf("flip at %d: unnamed error %v", i, err)
			}
		}
	}
}

// ECDSA malleability: (r, n-s) also verifies under plain ECDSA. Sign must emit
// low s, and Verify must refuse high s, so no second valid token exists.
func TestHighSRejected(t *testing.T) {
	key := newKey(t)
	keys := []*ecdsa.PublicKey{&key.PublicKey}
	for i := 0; i < 32; i++ {
		tok, err := Sign(TypMandate, []byte(`{"a":1}`), key, "")
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(tok, ".")
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		s := new(big.Int).SetBytes(sig[32:])
		if s.Cmp(halfOrder) > 0 {
			t.Fatal("Sign emitted a high-S signature")
		}
		s.Sub(elliptic.P256().Params().N, s)
		s.FillBytes(sig[32:])
		flipped := parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(sig)
		_, err = Verify(flipped, testProfile, keys, nil)
		wantCode(t, err, errs.MandateRejectedSignature)
	}
}

// typ is compared before any other header rule: a real agent-card header
// (with jku) presented as a mandate is TYP_REJECTED, not a parse error.
func TestTypBeforeHeaderRules(t *testing.T) {
	key := newKey(t)
	card, _ := SignDetached(TypAgentCard, []byte(`{}`), key, "https://x.example/tc.json")
	parts := strings.Split(card, ".")
	enc := base64.RawURLEncoding.EncodeToString
	tok := parts[0] + "." + enc([]byte(`{"a":1}`)) + "." + parts[2]
	_, err := Verify(tok, testProfile, []*ecdsa.PublicKey{&key.PublicKey}, nil)
	wantCode(t, err, errs.TypRejected)
	odd := enc([]byte(`{"alg":"ES256","crit":["x"],"cty":"y","kid":"k","typ":"agent-card+jws"}`)) + "." + enc([]byte(`{"a":1}`)) + "." + parts[2]
	_, err = Verify(odd, testProfile, nil, nil)
	wantCode(t, err, errs.TypRejected)
}

func TestSignRejectsOversizePayload(t *testing.T) {
	big := []byte(`{"a":"` + strings.Repeat("x", MaxPayloadBytes) + `"}`)
	if _, err := Sign(TypMandate, big, newKey(t), ""); err == nil {
		t.Fatal("oversize payload signed")
	}
	// The largest signable payload still fits the token cap.
	max := []byte(`{"a":"` + strings.Repeat("x", MaxPayloadBytes-8) + `"}`)
	tok, err := Sign(TypMandate, max, newKey(t), "")
	if err != nil || len(tok) > MaxTokenBytes {
		t.Fatalf("len %d err %v", len(tok), err)
	}
}

func TestDetachedRoundTripAndJKU(t *testing.T) {
	key := newKey(t)
	card := Profile{Typ: TypAgentCard, ParseCode: errs.CardParseError, SigCode: errs.CardRejectedSignature, AllowJKU: true}
	payload := []byte(`{"name":"gs-blacksburg"}`)
	tok, err := SignDetached(TypAgentCard, payload, key, "https://gs.example/.well-known/ans/trust-card.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tok, "..") {
		t.Fatalf("not detached: %s", tok)
	}
	keys := []*ecdsa.PublicKey{&key.PublicKey}
	h, err := VerifyDetached(tok, payload, card, keys, nil)
	if err != nil || h.JKU != "https://gs.example/.well-known/ans/trust-card.json" {
		t.Fatalf("h=%+v err=%v", h, err)
	}
	_, err = VerifyDetached(tok, []byte(`{"name":"gs-evil"}`), card, keys, nil)
	wantCode(t, err, errs.CardRejectedSignature)
	_, err = VerifyDetached(tok, nil, card, keys, nil)
	wantCode(t, err, errs.CardParseError)
	attached, _ := Sign(TypAgentCard, payload, key, "")
	_, err = VerifyDetached(attached, payload, card, keys, nil)
	wantCode(t, err, errs.CardParseError)
}

func TestSignRejectsWrongCurve(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if _, err := Sign(TypMandate, []byte(`{}`), k, ""); err == nil {
		t.Fatal("P-384 key accepted")
	}
	if _, err := Sign(TypMandate, []byte(`{}`), nil, ""); err == nil {
		t.Fatal("nil key accepted")
	}
}
