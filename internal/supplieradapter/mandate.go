package supplieradapter

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	gojose "github.com/go-jose/go-jose/v4"

	"github.com/arjitsama/overpass/internal/errs"
)

// --- Spending Authority keys, pinned by host ---------------------------------

// KeySource returns the authority's public keys by kid. Keys never come from
// the request.
type KeySource interface {
	Keys() (map[string]crypto.PublicKey, error)
}

// StaticKeys is a fixed key set (tests).
type StaticKeys map[string]crypto.PublicKey

func (s StaticKeys) Keys() (map[string]crypto.PublicKey, error) { return s, nil }

// HTTPKeySource fetches https://<host>/.well-known/ans/trust-card.json keys[]
// and, when it answers, https://<host>/.well-known/jwks.json, caching 10 min.
type HTTPKeySource struct {
	host string
	log  *slog.Logger
	http *http.Client
	mu   sync.Mutex
	keys map[string]crypto.PublicKey
	at   time.Time
}

func NewHTTPKeySource(host string, log *slog.Logger) *HTTPKeySource {
	return &HTTPKeySource{host: host, log: log, http: &http.Client{Timeout: 8 * time.Second}}
}

func (h *HTTPKeySource) Keys() (map[string]crypto.PublicKey, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.keys != nil && time.Since(h.at) < 10*time.Minute {
		return h.keys, nil
	}
	out := map[string]crypto.PublicKey{}
	for _, path := range []string{"/.well-known/ans/trust-card.json", "/.well-known/jwks.json"} {
		resp, err := h.http.Get("https://" + h.host + path)
		if err != nil {
			h.log.Warn("authority key fetch failed", "url", h.host+path, "err", err.Error())
			continue
		}
		var doc struct {
			Keys []json.RawMessage `json:"keys"`
		}
		err = json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&doc)
		resp.Body.Close()
		if resp.StatusCode != 200 || err != nil {
			h.log.Warn("authority key doc unusable", "url", h.host+path, "status", resp.StatusCode)
			continue
		}
		for _, raw := range doc.Keys {
			var k gojose.JSONWebKey
			// x5c may be a stringified list in their trust cards; drop it.
			var m map[string]any
			if json.Unmarshal(raw, &m) == nil {
				delete(m, "x5c")
				raw, _ = json.Marshal(m)
			}
			if err := k.UnmarshalJSON(raw); err != nil || !k.Valid() || !k.IsPublic() {
				continue
			}
			out[k.KeyID] = k.Key
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no authority keys reachable at " + h.host)
	}
	h.keys, h.at = out, time.Now()
	return out, nil
}

// --- Mandate ----------------------------------------------------------------

// Mandate is the AP2 mandate as observed on the wire: a JSON object whose
// `signature` (compact JWS, detached or not) covers the JCS form of the object
// without that field, OR a compact JWS whose payload is the mandate object.
// The parser accepts both; the signature is verified against the pinned
// authority keys only.
type Mandate struct {
	Raw           map[string]any // full object as sent (numbers as float64)
	MandateID     string
	QuoteID       string
	Audience      string
	Scope         string
	JKT           string
	MaxAmount     float64
	Currency      string
	Iat, Nbf, Exp int64
	SigAlg        string
	Kid           string
	sig           string // compact JWS (may be detached: header..signature)
	payload       []byte // bytes the signature covers
}

// parseMandate does step 1 (structure) only.
func parseMandate(s string) (*Mandate, *errs.Error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errs.New(errs.MandateParseError, "mandate is empty")
	}
	m := &Mandate{}
	var obj map[string]any
	if strings.HasPrefix(s, "{") {
		dec := json.NewDecoder(strings.NewReader(s))
		if err := dec.Decode(&obj); err != nil {
			return nil, errs.New(errs.MandateParseError, "mandate is not valid JSON: "+err.Error())
		}
		sig, _ := obj["signature"].(string)
		if sig == "" {
			return nil, errs.New(errs.MandateParseError, "mandate has no signature")
		}
		m.sig = sig
		unsigned := map[string]any{}
		for k, v := range obj {
			if k != "signature" {
				unsigned[k] = v
			}
		}
		payload, err := canonicalize(unsigned)
		if err != nil {
			return nil, errs.New(errs.MandateParseError, "mandate cannot be canonicalized: "+err.Error())
		}
		m.payload = payload
	} else if strings.Count(s, ".") == 2 {
		// compact JWS: payload is the mandate
		parts := strings.Split(s, ".")
		pl, err := b64url(parts[1])
		if err != nil {
			return nil, errs.New(errs.MandateParseError, "mandate JWS payload is not base64url")
		}
		if err := json.Unmarshal(pl, &obj); err != nil {
			return nil, errs.New(errs.MandateParseError, "mandate JWS payload is not a JSON object")
		}
		m.sig, m.payload = s, pl
	} else {
		return nil, errs.New(errs.MandateParseError, "mandate is neither a JSON object nor a compact JWS")
	}
	m.Raw = obj
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := obj[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	num := func(keys ...string) (float64, bool) {
		for _, k := range keys {
			switch v := obj[k].(type) {
			case float64:
				return v, true
			case string:
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					return f, true
				}
			}
		}
		return 0, false
	}
	m.MandateID = str("mandate_id", "id", "jti", "nonce")
	m.QuoteID = str("quote_id")
	m.Audience = str("audience", "aud", "merchant_ans", "merchant")
	m.Scope = str("scope", "scope_hint")
	m.JKT = str("jkt")
	if v, ok := obj["cnf"].(map[string]any); ok && m.JKT == "" {
		m.JKT, _ = v["jkt"].(string)
	}
	m.Currency = str("currency")
	var missing []string
	if amt, ok := num("max_amount", "total", "amount"); ok {
		m.MaxAmount = amt
	} else {
		missing = append(missing, "max_amount")
	}
	for _, f := range []struct{ name, v string }{{"scope", m.Scope}, {"jkt", m.JKT}, {"audience", m.Audience}, {"quote_id", m.QuoteID}} {
		if f.v == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return nil, errs.New(errs.MandateParseError, "mandate missing required fields: "+strings.Join(missing, ", "))
	}
	if v, ok := num("iat"); ok {
		m.Iat = int64(v)
	}
	if v, ok := num("nbf", "not_before"); ok {
		m.Nbf = int64(v)
	}
	if v, ok := num("exp", "expires_at", "not_after"); ok {
		m.Exp = int64(v)
	}
	return m, nil
}

// verifySignature does step 2 against the pinned keys. Detached JWS
// (header..signature) is verified over m.payload; a full JWS over its own
// payload. Unknown kid -> unknown_key; anything else wrong -> signature.
func (m *Mandate) verifySignature(keys map[string]crypto.PublicKey) *errs.Error {
	parts := strings.Split(m.sig, ".")
	if len(parts) != 3 {
		return errs.New(errs.MandateRejectedSignature, "signature is not a compact JWS")
	}
	hdrRaw, err := b64url(parts[0])
	if err != nil {
		return errs.New(errs.MandateRejectedSignature, "JWS header is not base64url")
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hdrRaw, &hdr); err != nil {
		return errs.New(errs.MandateRejectedSignature, "JWS header is not JSON")
	}
	m.SigAlg, m.Kid = hdr.Alg, hdr.Kid
	key, ok := keys[hdr.Kid]
	if !ok {
		return errs.New(errs.MandateRejectedUnknownKey, "signing key "+hdr.Kid+" is not published by the Spending Authority")
	}
	switch key.(type) {
	case ed25519.PublicKey, *ecdsa.PublicKey:
	default:
		return errs.New(errs.MandateRejectedUnknownKey, "authority key type unsupported")
	}
	tok := m.sig
	if parts[1] == "" { // detached
		tok = parts[0] + "." + base64.RawURLEncoding.EncodeToString(m.payload) + "." + parts[2]
	}
	jws, err := gojose.ParseSigned(tok, []gojose.SignatureAlgorithm{gojose.EdDSA, gojose.ES256})
	if err != nil {
		return errs.New(errs.MandateRejectedSignature, "JWS does not parse: "+err.Error())
	}
	if _, err := jws.Verify(key); err != nil {
		return errs.New(errs.MandateRejectedSignature, "signature does not verify against the authority key "+hdr.Kid)
	}
	return nil
}

// --- DPoP -------------------------------------------------------------------

type dpopProof struct {
	JKT string
	JTI string
	HTM string
	HTU string
	Iat int64
}

// parseDPoP verifies the proof with the key it carries (that is the DPoP
// contract) and returns its thumbprint; binding to the mandate is the caller's.
func parseDPoP(s string, now time.Time) (*dpopProof, *errs.Error) {
	s = strings.TrimSpace(s)
	if strings.Count(s, ".") != 2 {
		return nil, errs.New(errs.DPoPRejectedKey, "dpop_proof is not a compact JWS")
	}
	parts := strings.Split(s, ".")
	hdrRaw, err := b64url(parts[0])
	if err != nil {
		return nil, errs.New(errs.DPoPRejectedKey, "DPoP header is not base64url")
	}
	var hdr struct {
		Typ string          `json:"typ"`
		Alg string          `json:"alg"`
		JWK json.RawMessage `json:"jwk"`
	}
	if err := json.Unmarshal(hdrRaw, &hdr); err != nil || len(hdr.JWK) == 0 {
		return nil, errs.New(errs.DPoPRejectedKey, "DPoP header has no jwk")
	}
	var jwk gojose.JSONWebKey
	if err := jwk.UnmarshalJSON(hdr.JWK); err != nil || !jwk.Valid() || !jwk.IsPublic() {
		return nil, errs.New(errs.DPoPRejectedKey, "DPoP jwk is not a valid public key")
	}
	jws, err := gojose.ParseSigned(s, []gojose.SignatureAlgorithm{gojose.EdDSA, gojose.ES256})
	if err != nil {
		return nil, errs.New(errs.DPoPRejectedKey, "DPoP proof does not parse")
	}
	pl, err := jws.Verify(jwk.Key)
	if err != nil {
		return nil, errs.New(errs.DPoPRejectedKey, "DPoP proof signature does not verify with its own jwk")
	}
	var claims struct {
		JTI string  `json:"jti"`
		HTM string  `json:"htm"`
		HTU string  `json:"htu"`
		Iat float64 `json:"iat"`
	}
	if err := json.Unmarshal(pl, &claims); err != nil || claims.JTI == "" {
		return nil, errs.New(errs.DPoPRejectedKey, "DPoP claims missing jti")
	}
	if claims.Iat != 0 && math.Abs(now.Sub(time.Unix(int64(claims.Iat), 0)).Seconds()) > 300 {
		return nil, errs.New(errs.DPoPRejectedKey, "DPoP proof iat is not within 300 s")
	}
	tp, err := jwk.Thumbprint(crypto.SHA256)
	if err != nil {
		return nil, errs.New(errs.DPoPRejectedKey, "DPoP jwk thumbprint failed")
	}
	return &dpopProof{JKT: base64.RawURLEncoding.EncodeToString(tp), JTI: claims.JTI, HTM: claims.HTM, HTU: claims.HTU, Iat: int64(claims.Iat)}, nil
}

// --- book_flight: the check chain --------------------------------------------

type bookArgs struct {
	QuoteID     string `json:"quote_id"`
	OptionID    string `json:"option_id"`
	Mandate     string `json:"mandate"`
	DPoP        string `json:"dpop_proof"`
	PaymentAuth string `json:"payment_auth"`
}

// bookFlight runs the checks in order; the first failure names its code.
func (a *Adapter) bookFlight(r *http.Request, raw json.RawMessage) (any, bool) {
	var in bookArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return reject(errs.MandateParseError, "book_flight: arguments are not valid JSON"), true
	}
	if e := a.Verify(in, r); e != nil {
		return reject(e.Code, e.Detail), true
	}
	// 11. Everything valid. This station does not settle on-chain: no ticket.
	out := a.x402Challenge("Booking " + in.QuoteID + "/" + in.OptionID + ": mandate and DPoP verified; on-chain settlement is not performed by this station (Honest limits)")
	out["code"] = string(errs.PaymentRequired)
	out["detail"] = "mandate and DPoP verified; EIP-3009 settlement not supported here, no ticket issued"
	return out, true
}

// Verify is the exported chain (also used by tests). r may be nil.
func (a *Adapter) Verify(in bookArgs, r *http.Request) (e *errs.Error) {
	defer func() {
		if p := recover(); p != nil {
			a.cfg.Log.Error("supplieradapter: verifier recovered", "panic", fmt.Sprint(p))
			e = errs.New(errs.MandateParseError, "mandate could not be processed (recovered)")
		}
	}()
	now := a.cfg.Now()
	// 1. structure
	m, err := parseMandate(in.Mandate)
	if err != nil {
		return err
	}
	if in.QuoteID == "" || in.OptionID == "" || strings.TrimSpace(in.DPoP) == "" {
		return errs.New(errs.MandateParseError, "quote_id, option_id and dpop_proof are required")
	}
	// 2. signature against pinned authority keys
	keys, kerr := a.keys.Keys()
	if kerr != nil {
		// Fail closed, but truthfully: we could not reach the authority's keys.
		return errs.New(errs.MandateRejectedUnknownKey, "authority keys unavailable: "+kerr.Error())
	}
	if err := m.verifySignature(keys); err != nil {
		return err
	}
	// 2b. issuer: the supplier checks the mandate's authority_ans against its
	// pinned authority before anything else (observed 2026-09-20); so do we.
	if a.cfg.Authority != "" {
		if iss := firstNonEmpty(strS(m.Raw["authority_ans"]), strS(m.Raw["issuer"]), strS(m.Raw["iss"])); iss != "" && !strings.EqualFold(iss, a.cfg.Authority) {
			return errs.New(errs.MandateRejectedUnknownKey, "authority_ans "+iss+" does not match pinned authority "+a.cfg.Authority)
		}
	}
	// 3. audience
	if !strings.EqualFold(m.Audience, a.cfg.ANSName) && !strings.EqualFold(m.Audience, a.cfg.Host) {
		return errs.New(errs.MandateRejectedAudience, "mandate audience "+m.Audience+" is not this station ("+a.cfg.ANSName+")")
	}
	// 4. quote binding
	q, ok := a.Quote(in.QuoteID)
	if !ok {
		return errs.New(errs.MandateRejectedQuote, "quote "+in.QuoteID+" is unknown or expired")
	}
	if m.QuoteID != in.QuoteID {
		return errs.New(errs.MandateRejectedQuote, "mandate quote_id "+m.QuoteID+" is not the booked quote "+in.QuoteID)
	}
	var opt *Option
	for i := range q.Options {
		if q.Options[i].OptionID == in.OptionID {
			opt = &q.Options[i]
		}
	}
	if opt == nil {
		return errs.New(errs.MandateRejectedQuote, "option "+in.OptionID+" is not on quote "+in.QuoteID)
	}
	// 5. scope
	if !strings.EqualFold(m.Scope, q.Scope) {
		return errs.New(errs.MandateRejectedScope, "mandate scope "+m.Scope+" does not cover "+q.Scope)
	}
	// 6. amount (numeric, after JCS-consistent parsing)
	price, _ := strconv.ParseFloat(opt.Price, 64)
	if m.MaxAmount+1e-9 < price {
		return errs.New(errs.MandateRejectedAmount, fmt.Sprintf("mandate max_amount %s is below the option price %s", fmtNum(m.MaxAmount), opt.Price))
	}
	if m.Currency != "" && !strings.EqualFold(m.Currency, opt.Currency) {
		return errs.New(errs.MandateRejectedAmount, "mandate currency "+m.Currency+" is not "+opt.Currency)
	}
	// 7. validity window
	if m.Nbf != 0 && now.Unix() < m.Nbf-30 {
		return errs.New(errs.MandateRejectedExpired, "mandate is not yet valid")
	}
	if m.Exp != 0 && now.Unix() > m.Exp+30 {
		return errs.New(errs.MandateRejectedExpired, "mandate has expired")
	}
	// 8. DPoP proof and key binding
	p, err := parseDPoP(in.DPoP, now)
	if err != nil {
		return err
	}
	if p.JKT != m.JKT {
		return errs.New(errs.DPoPRejectedKey, "DPoP key thumbprint "+p.JKT+" is not the mandate's jkt "+m.JKT)
	}
	if p.HTM != "" && !strings.EqualFold(p.HTM, "POST") {
		return errs.New(errs.DPoPRejectedKey, "DPoP htm is not POST")
	}
	if p.HTU != "" && !strings.HasPrefix(strings.ToLower(p.HTU), strings.ToLower(a.cfg.PublicURL)) {
		return errs.New(errs.DPoPRejectedKey, "DPoP htu "+p.HTU+" is not this station")
	}
	// 9. DPoP replay, 10. mandate consumed — atomically
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, seen := a.usedJTI[p.JTI]; seen {
		return errs.New(errs.DPoPRejectedReplay, "DPoP jti "+p.JTI+" was already used")
	}
	mid := m.MandateID
	if mid == "" {
		mid = m.sigDigest()
	}
	if _, seen := a.used[mid]; seen {
		return errs.New(errs.MandateRejectedConsumed, "mandate "+mid+" was already used")
	}
	a.usedJTI[p.JTI] = now.Add(10 * time.Minute)
	a.used[mid] = now.Add(24 * time.Hour)
	return nil
}

func strS(v any) string { s, _ := v.(string); return s }

func (m *Mandate) sigDigest() string {
	sum := sha256Sum([]byte(m.sig))
	return "sig-" + sum[:16]
}

// --- JCS (RFC 8785) canonicalization with ES6 number serialization ------------
// 620.0 and 620 canonicalize identically, so int-vs-float mandates verify the
// same way. Non-finite numbers are rejected.

func canonicalize(v any) ([]byte, error) {
	var b strings.Builder
	if err := canon(&b, v); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

func canon(b *strings.Builder, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return errors.New("non-finite number")
		}
		b.WriteString(fmtNum(x))
	case string:
		b.WriteString(jsonString(x))
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := canon(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		b.WriteByte('{')
		for i, k := range sortedKeys(x) {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(jsonString(k))
			b.WriteByte(':')
			if err := canon(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("unsupported JSON value %T", v)
	}
	return nil
}

// jsonString encodes a string with JCS's minimal escaping (no HTML escapes).
func jsonString(x string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(x)
	return strings.TrimRight(buf.String(), "\n")
}

// fmtNum is ES6 Number::toString for the finite range JCS cares about.
func fmtNum(f float64) string {
	if f == 0 {
		return "0"
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e21 {
		return strconv.FormatFloat(f, 'f', 0, 64)
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if strings.Contains(s, "e") {
		// ES6 uses e+21 / e-7 style; Go 'g' gives e+21 / e-07. Normalise exponent.
		mant, exp, _ := strings.Cut(s, "e")
		sign := exp[0]
		exp = strings.TrimLeft(exp[1:], "0")
		if exp == "" {
			exp = "0"
		}
		return mant + "e" + string(sign) + exp
	}
	return s
}

func b64url(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	return base64.RawURLEncoding.DecodeString(s)
}

func sha256Sum(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
