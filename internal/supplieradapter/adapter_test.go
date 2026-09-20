package supplieradapter

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gojose "github.com/go-jose/go-jose/v4"

	"github.com/arjitsama/overpass/internal/errs"
)

// --- fixtures: a local Ed25519 "Spending Authority" and a traveler DPoP key ---

type fx struct {
	a        *Adapter
	authPub  ed25519.PublicKey
	authPriv ed25519.PrivateKey
	kid      string
	dpop     *ecdsa.PrivateKey
	jkt      string
	q        *Quote
	now      time.Time
}

func newFx(t *testing.T) *fx {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	tp, _ := (&gojose.JSONWebKey{Key: pub}).Thumbprint(crypto.SHA256)
	kid := base64.RawURLEncoding.EncodeToString(tp)
	dp, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	dtp, _ := (&gojose.JSONWebKey{Key: &dp.PublicKey}).Thumbprint(crypto.SHA256)
	now := time.Unix(1_790_000_000, 0)
	a := New(Config{Host: "gs.example", ANSName: "ans://v0.1.0.gs.example", PublicURL: "https://gs.example",
		PayTo: "0x0000000000000000000000000000000000000000", Network: "eip155:84532", Asset: "0xUSDC",
		AuthorityHost: "authority.example", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return now }, Keys: StaticKeys{kid: pub}})
	q := a.makeQuote("MAD", "SIN", "2026-10-14", 1)
	a.quotes[q.QuoteID] = q
	return &fx{a: a, authPub: pub, authPriv: priv, kid: kid, dpop: dp, jkt: base64.RawURLEncoding.EncodeToString(dtp), q: q, now: now}
}

// mandate returns a JSON-object mandate with a detached EdDSA JWS over its JCS form.
func (f *fx) mandate(t *testing.T, mut func(m map[string]any)) string {
	t.Helper()
	m := map[string]any{
		"mandate_id": "m-" + f.q.QuoteID, "quote_id": f.q.QuoteID, "audience": f.a.cfg.ANSName,
		"scope": f.q.Scope, "max_amount": 900.0, "currency": "USD", "jkt": f.jkt,
		"iat": float64(f.now.Unix()), "exp": float64(f.now.Add(time.Hour).Unix()), "issuer": "ans://v1.0.4.authority.example",
	}
	if mut != nil {
		mut(m)
	}
	return f.signObj(t, m, f.authPriv, f.kid)
}

func (f *fx) signObj(t *testing.T, m map[string]any, priv ed25519.PrivateKey, kid string) string {
	t.Helper()
	payload, err := canonicalize(m)
	if err != nil {
		t.Fatal(err)
	}
	sig := signDetached(t, payload, priv, kid)
	m["signature"] = sig
	b, _ := json.Marshal(m)
	return string(b)
}

func signDetached(t *testing.T, payload []byte, priv ed25519.PrivateKey, kid string) string {
	t.Helper()
	hdr, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": kid, "typ": "ap2-mandate+jws"})
	h := base64.RawURLEncoding.EncodeToString(hdr)
	p := base64.RawURLEncoding.EncodeToString(payload)
	sig := ed25519.Sign(priv, []byte(h+"."+p))
	return h + ".." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *fx) proof(t *testing.T, key *ecdsa.PrivateKey, jti string) string {
	t.Helper()
	jwk := gojose.JSONWebKey{Key: &key.PublicKey}
	signer, err := gojose.NewSigner(gojose.SigningKey{Algorithm: gojose.ES256, Key: key},
		(&gojose.SignerOptions{}).WithType("dpop+jwt").WithHeader("jwk", jwk))
	if err != nil {
		t.Fatal(err)
	}
	claims, _ := json.Marshal(map[string]any{"jti": jti, "htm": "POST", "htu": "https://gs.example/mcp/", "iat": f.now.Unix()})
	obj, err := signer.Sign(claims)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := obj.CompactSerialize()
	return s
}

func (f *fx) book(t *testing.T, mandate, proof string, quote, option string) *errs.Error {
	t.Helper()
	return f.a.Verify(bookArgs{QuoteID: quote, OptionID: option, Mandate: mandate, DPoP: proof}, nil)
}

func want(t *testing.T, got *errs.Error, code errs.Code) {
	t.Helper()
	if code == "" {
		if got != nil {
			t.Fatalf("want accept, got %s: %s", got.Code, got.Detail)
		}
		return
	}
	if got == nil || got.Code != code {
		t.Fatalf("want %s, got %+v", code, got)
	}
}

// --- one test per check -------------------------------------------------------

func TestValidMandateIsAcceptedThenConsumed(t *testing.T) {
	f := newFx(t)
	opt := f.q.Options[0].OptionID
	want(t, f.book(t, f.mandate(t, nil), f.proof(t, f.dpop, "j1"), f.q.QuoteID, opt), "")
	// 10. same mandate, fresh DPoP -> consumed
	want(t, f.book(t, f.mandate(t, nil), f.proof(t, f.dpop, "j2"), f.q.QuoteID, opt), errs.MandateRejectedConsumed)
}

func TestSupersededFormatIsParseError(t *testing.T) {
	f := newFx(t)
	m := map[string]any{"quote_id": f.q.QuoteID, "max_amount": 900.0} // no scope, jkt, signature
	b, _ := json.Marshal(m)
	want(t, f.book(t, string(b), f.proof(t, f.dpop, "j"), f.q.QuoteID, f.q.Options[0].OptionID), errs.MandateParseError)
	want(t, f.book(t, "not json at all", f.proof(t, f.dpop, "j"), f.q.QuoteID, f.q.Options[0].OptionID), errs.MandateParseError)
}

func TestTamperedAmountFailsSignature(t *testing.T) {
	f := newFx(t)
	s := f.mandate(t, nil)
	var m map[string]any
	_ = json.Unmarshal([]byte(s), &m)
	m["max_amount"] = 9000.0 // inflated after signing
	b, _ := json.Marshal(m)
	want(t, f.book(t, string(b), f.proof(t, f.dpop, "j"), f.q.QuoteID, f.q.Options[0].OptionID), errs.MandateRejectedSignature)
}

func TestCorruptSignatureBytes(t *testing.T) {
	f := newFx(t)
	s := f.mandate(t, nil)
	var m map[string]any
	_ = json.Unmarshal([]byte(s), &m)
	sig := m["signature"].(string)
	last := sig[len(sig)-2:]
	flipped := "AA"
	if last == "AA" {
		flipped = "BB"
	}
	m["signature"] = sig[:len(sig)-2] + flipped
	b, _ := json.Marshal(m)
	want(t, f.book(t, string(b), f.proof(t, f.dpop, "j"), f.q.QuoteID, f.q.Options[0].OptionID), errs.MandateRejectedSignature)
}

func TestUnknownKeyIsRejected(t *testing.T) {
	f := newFx(t)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	m := map[string]any{"mandate_id": "m-x", "quote_id": f.q.QuoteID, "audience": f.a.cfg.ANSName, "scope": f.q.Scope,
		"max_amount": 900.0, "jkt": f.jkt}
	s := f.signObj(t, m, other, "fresh-unpublished-kid")
	want(t, f.book(t, s, f.proof(t, f.dpop, "j"), f.q.QuoteID, f.q.Options[0].OptionID), errs.MandateRejectedUnknownKey)
}

func TestWrongAudience(t *testing.T) {
	f := newFx(t)
	s := f.mandate(t, func(m map[string]any) { m["audience"] = "ans://v1.0.3.rogue-supplier.example" })
	want(t, f.book(t, s, f.proof(t, f.dpop, "j"), f.q.QuoteID, f.q.Options[0].OptionID), errs.MandateRejectedAudience)
}

func TestQuoteSwap(t *testing.T) {
	f := newFx(t)
	qb := f.a.makeQuote("MAD", "NRT", "2026-10-15", 1)
	f.a.quotes[qb.QuoteID] = qb
	s := f.mandate(t, nil) // for quote A
	want(t, f.book(t, s, f.proof(t, f.dpop, "j"), qb.QuoteID, qb.Options[0].OptionID), errs.MandateRejectedQuote)
	want(t, f.book(t, s, f.proof(t, f.dpop, "j"), "q-unknown", "opt"), errs.MandateRejectedQuote)
}

func TestWrongScope(t *testing.T) {
	f := newFx(t)
	s := f.mandate(t, func(m map[string]any) { m["scope"] = "purchase:flight:MAD-NYC:2026-10-14" })
	want(t, f.book(t, s, f.proof(t, f.dpop, "j"), f.q.QuoteID, f.q.Options[0].OptionID), errs.MandateRejectedScope)
}

func TestUnderpayWithValidSignature(t *testing.T) {
	f := newFx(t)
	s := f.mandate(t, func(m map[string]any) { m["max_amount"] = 0.01 })
	want(t, f.book(t, s, f.proof(t, f.dpop, "j"), f.q.QuoteID, f.q.Options[0].OptionID), errs.MandateRejectedAmount)
}

func TestExpiredWindow(t *testing.T) {
	f := newFx(t)
	s := f.mandate(t, func(m map[string]any) { m["exp"] = float64(f.now.Add(-time.Hour).Unix()) })
	want(t, f.book(t, s, f.proof(t, f.dpop, "j"), f.q.QuoteID, f.q.Options[0].OptionID), errs.MandateRejectedExpired)
}

func TestWrongDPoPKey(t *testing.T) {
	f := newFx(t)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	want(t, f.book(t, f.mandate(t, nil), f.proof(t, other, "j"), f.q.QuoteID, f.q.Options[0].OptionID), errs.DPoPRejectedKey)
}

func TestDPoPReplay(t *testing.T) {
	f := newFx(t)
	p := f.proof(t, f.dpop, "same-jti")
	want(t, f.book(t, f.mandate(t, nil), p, f.q.QuoteID, f.q.Options[0].OptionID), "")
	// fresh mandate (different id), spent proof -> replay before consumed
	s := f.mandate(t, func(m map[string]any) { m["mandate_id"] = "m-second" })
	want(t, f.book(t, s, p, f.q.QuoteID, f.q.Options[0].OptionID), errs.DPoPRejectedReplay)
}

// canonicalization: 620 and 620.0 must produce identical bytes and outcomes.
func TestFloatAndIntCanonicalizeTheSame(t *testing.T) {
	a, _ := canonicalize(map[string]any{"max_amount": 620.0, "x": "<&>"})
	var m map[string]any
	_ = json.Unmarshal([]byte(`{"max_amount": 620, "x": "<&>"}`), &m)
	b, _ := canonicalize(m)
	if !bytes.Equal(a, b) || string(a) != `{"max_amount":620,"x":"<&>"}` {
		t.Fatalf("canon %s vs %s", a, b)
	}
	if fmtNum(0.01) != "0.01" || fmtNum(1e21) != "1e+21" || fmtNum(1234.5) != "1234.5" {
		t.Fatalf("fmtNum: %s %s %s", fmtNum(0.01), fmtNum(1e21), fmtNum(1234.5))
	}
	f := newFx(t)
	sInt := f.mandate(t, func(m map[string]any) { m["max_amount"] = 900.0; m["mandate_id"] = "m-int" })
	// re-encode with an explicit ".0" on the wire: the parser must canonicalize it away
	sFloat := strings.Replace(sInt, `"max_amount":900`, `"max_amount":900.0`, 1)
	if sFloat == sInt {
		t.Fatal("fixture did not contain max_amount")
	}
	want(t, f.book(t, sFloat, f.proof(t, f.dpop, "j1"), f.q.QuoteID, f.q.Options[0].OptionID), "")
}

// --- transport --------------------------------------------------------------

func TestMCPTransportAndX402Challenge(t *testing.T) {
	f := newFx(t)
	srv := httptest.NewServer(f.a)
	defer srv.Close()
	call := func(body string, hdr map[string]string) map[string]any {
		req, _ := http.NewRequest("POST", srv.URL+"/mcp/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 500 {
			t.Fatalf("5xx: %d", resp.StatusCode)
		}
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	init := call(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`, nil)
	if init["result"].(map[string]any)["protocolVersion"] != ProtocolVersion {
		t.Fatalf("init %+v", init)
	}
	tl := call(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, nil)
	if n := len(tl["result"].(map[string]any)["tools"].([]any)); n != 2 {
		t.Fatalf("tools %d", n)
	}
	q := call(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_quote","arguments":{"origin":"MAD","destination":"SIN","date":"2026-10-14"}}}`, nil)
	res := q["result"].(map[string]any)
	sc := res["structuredContent"].(map[string]any)
	if res["isError"] != true || sc["x402Version"] != float64(2) || sc["accepts"].([]any)[0].(map[string]any)["payTo"] != f.a.cfg.PayTo {
		t.Fatalf("challenge %+v", res)
	}
	paid := call(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_quote","arguments":{"origin":"MAD","destination":"SIN","date":"2026-10-14"}}}`, map[string]string{"X-Payment": "eyJ0ZXN0IjoxfQ"})
	pr := paid["result"].(map[string]any)
	if pr["isError"] != false || len(pr["structuredContent"].(map[string]any)["options"].([]any)) != 3 {
		t.Fatalf("paid quote %+v", pr)
	}
	bad := call(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"book_flight","arguments":{"quote_id":"q","option_id":"o","mandate":"{{{{","dpop_proof":"x"}}}`, nil)
	br := bad["result"].(map[string]any)
	txt := br["content"].([]any)[0].(map[string]any)["text"].(string)
	if br["isError"] != true || br["structuredContent"] != nil || !strings.HasPrefix(txt, "Error executing tool book_flight: MANDATE_PARSE_ERROR: ") {
		t.Fatalf("bad mandate envelope %+v", br)
	}
	garbage := call(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"book_flight","arguments":"not-an-object"}}`, nil)
	if garbage["result"] == nil && garbage["error"] == nil {
		t.Fatalf("garbage args produced no JSON-RPC answer")
	}
}

func FuzzVerifyNeverPanics(f *testing.F) {
	fx := newFx(&testing.T{})
	f.Add(`{"quote_id":"q","scope":"s","jkt":"k","audience":"a","max_amount":1,"signature":"a..b"}`, "x.y.z")
	f.Add("e30..", "")
	f.Fuzz(func(t *testing.T, mandate, proof string) {
		_ = fx.a.Verify(bookArgs{QuoteID: fx.q.QuoteID, OptionID: "o", Mandate: mandate, DPoP: proof}, nil)
	})
}
