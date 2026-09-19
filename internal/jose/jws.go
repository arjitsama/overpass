package jose

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	gojose "github.com/go-jose/go-jose/v4"

	"github.com/arjitsama/overpass/internal/errs"
)

// JWS typ values. Every signed object has its own, and every verifier checks
// typ before anything else, so one object can never be replayed as another.
const (
	TypMandate   = "overpass-mandate+jws"
	TypSatReg    = "overpass-satreg+jws"
	TypCommand   = "overpass-cmd+jws"
	TypAudit     = "overpass-audit+jws"
	TypReceipt   = "overpass-receipt+jws"
	TypAgentCard = "agent-card+jws"
)

// Size limits.
const (
	// MaxPayloadBytes caps what Sign will sign; MaxTokenBytes leaves room for
	// its base64 expansion (4/3) plus header and signature.
	MaxPayloadBytes = 48 << 10
	MaxTokenBytes   = 68 << 10
	maxHeaderBytes  = 1 << 10
	es256SigLen     = 64
)

var b64 = base64.RawURLEncoding.Strict()

// Profile says what a verifier expects and which codes it rejects with.
type Profile struct {
	Typ       string
	ParseCode errs.Code // malformed token, header or payload
	SigCode   errs.Code // unknown key or bad signature
	AllowJKU  bool      // only agent cards carry jku
}

// Header is the protected header Overpass writes and accepts. Nothing else
// (no crit, b64, jwk, x5u, x5c) is allowed.
type Header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
	JKU string `json:"jku,omitempty"`
}

// Sign returns a compact ES256 JWS over payload with the given typ. kid is the
// RFC 7638 thumbprint of key; jku is set only when non-empty.
func Sign(typ string, payload []byte, key *ecdsa.PrivateKey, jku string) (string, error) {
	obj, err := sign(typ, payload, key, jku)
	if err != nil {
		return "", err
	}
	tok, err := obj.CompactSerialize()
	if err != nil {
		return "", err
	}
	return lowS(tok)
}

// SignDetached is Sign with the payload left out: "header..signature".
func SignDetached(typ string, payload []byte, key *ecdsa.PrivateKey, jku string) (string, error) {
	obj, err := sign(typ, payload, key, jku)
	if err != nil {
		return "", err
	}
	tok, err := obj.DetachedCompactSerialize()
	if err != nil {
		return "", err
	}
	return lowS(tok)
}

// lowS rewrites the signature's s to the lower half of the curve order.
// ECDSA accepts both (r, s) and (r, n-s); allowing only low s gives every
// payload exactly one valid signature encoding per signing, so a relay cannot
// mint a second valid token (which would change cmd_sha256 in the chain).
func lowS(tok string) (string, error) {
	i := strings.LastIndexByte(tok, '.')
	sig, err := b64.DecodeString(tok[i+1:])
	if err != nil || len(sig) != es256SigLen {
		return "", errors.New("jose: unexpected signature encoding")
	}
	s := new(big.Int).SetBytes(sig[32:])
	if s.Cmp(halfOrder) > 0 {
		s.Sub(elliptic.P256().Params().N, s)
		s.FillBytes(sig[32:])
	}
	return tok[:i+1] + b64.EncodeToString(sig), nil
}

// halfOrder is n/2 for P-256.
var halfOrder = new(big.Int).Rsh(elliptic.P256().Params().N, 1)

func sign(typ string, payload []byte, key *ecdsa.PrivateKey, jku string) (*gojose.JSONWebSignature, error) {
	if key == nil || key.Curve != elliptic.P256() {
		return nil, errors.New("jose: signing key must be EC P-256")
	}
	if len(payload) > MaxPayloadBytes {
		return nil, fmt.Errorf("jose: payload exceeds %d bytes", MaxPayloadBytes)
	}
	kid, err := Thumbprint(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	opts := (&gojose.SignerOptions{}).WithType(gojose.ContentType(typ))
	if jku != "" {
		opts = opts.WithHeader("jku", jku)
	}
	signer, err := gojose.NewSigner(gojose.SigningKey{
		Algorithm: gojose.ES256,
		Key:       gojose.JSONWebKey{Key: key, KeyID: kid, Algorithm: string(gojose.ES256)},
	}, opts)
	if err != nil {
		return nil, err
	}
	return signer.Sign(payload)
}

// Thumbprint returns the base64url RFC 7638 SHA-256 thumbprint of pub.
func Thumbprint(pub crypto.PublicKey) (string, error) {
	sum, err := (&gojose.JSONWebKey{Key: pub}).Thumbprint(crypto.SHA256)
	if err != nil {
		return "", fmt.Errorf("jose: thumbprint: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(sum), nil
}

// Verify checks a compact JWS and returns its payload. Order: structure,
// header, typ (TYP_REJECTED), alg, decode(payload), key by kid, signature.
// decode runs before the signature so a malformed payload is a parse error,
// matching book_pass step 1; it may be nil. Verify never panics.
func Verify(token string, p Profile, keys []*ecdsa.PublicKey, decode func([]byte) error) (payload []byte, err error) {
	defer recoverInto(&err, p)
	segs, err := splitToken(token, p)
	if err != nil {
		return nil, err
	}
	if len(segs[1]) == 0 {
		return nil, errs.New(p.ParseCode, "payload segment is empty")
	}
	if _, err := check(segs, p, keys, decode, false); err != nil {
		return nil, err
	}
	return segs[1], nil
}

// VerifyDetached checks a "header..signature" JWS over payload and returns
// its header (callers need jku). It never panics.
func VerifyDetached(token string, payload []byte, p Profile, keys []*ecdsa.PublicKey, decode func([]byte) error) (h Header, err error) {
	defer recoverInto(&err, p)
	if payload == nil {
		return Header{}, errs.New(p.ParseCode, "no payload for detached JWS")
	}
	segs, err := splitToken(token, p)
	if err != nil {
		return Header{}, err
	}
	if len(segs[1]) != 0 {
		return Header{}, errs.New(p.ParseCode, "payload is not detached")
	}
	segs[1] = payload
	return check(segs, p, keys, decode, true)
}

func recoverInto(err *error, p Profile) {
	if r := recover(); r != nil {
		*err = errs.New(p.ParseCode, "malformed JWS")
	}
}

// splitToken returns the decoded header, payload and signature segments.
// Base64url is strict: no padding, no whitespace, no non-zero trailing bits,
// so each token has exactly one encoding.
func splitToken(token string, p Profile) ([3][]byte, error) {
	var out [3][]byte
	if len(token) > MaxTokenBytes {
		return out, errs.New(p.ParseCode, fmt.Sprintf("token exceeds %d bytes", MaxTokenBytes))
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return out, errs.New(p.ParseCode, "JWS must have three segments")
	}
	for i, s := range parts {
		if strings.ContainsAny(s, "\r\n") {
			return out, errs.New(p.ParseCode, "whitespace in JWS")
		}
		b, err := b64.DecodeString(s)
		if err != nil {
			return out, errs.New(p.ParseCode, fmt.Sprintf("segment %d is not strict base64url", i))
		}
		out[i] = b
	}
	return out, nil
}

func check(segs [3][]byte, p Profile, keys []*ecdsa.PublicKey, decode func([]byte) error, detached bool) (Header, error) {
	if err := checkTyp(segs[0], p); err != nil {
		return Header{}, err
	}
	h, err := parseHeader(segs[0], p)
	if err != nil {
		return Header{}, err
	}
	if h.Alg != string(gojose.ES256) {
		return Header{}, errs.New(p.ParseCode, fmt.Sprintf("alg %q, want ES256", h.Alg))
	}
	if len(segs[2]) != es256SigLen {
		return Header{}, errs.New(p.ParseCode, "missing or malformed ES256 signature")
	}
	if new(big.Int).SetBytes(segs[2][32:]).Cmp(halfOrder) > 0 {
		return Header{}, errs.New(p.SigCode, "high-S signature (non-canonical)")
	}
	if decode != nil {
		if err := decode(segs[1]); err != nil {
			return Header{}, asCode(err, p.ParseCode)
		}
	}
	key := findKey(keys, h.Kid)
	if key == nil {
		return Header{}, errs.New(p.SigCode, "kid is not a trusted key")
	}
	if err := verifySig(segs, key, detached); err != nil {
		return Header{}, errs.New(p.SigCode, "signature does not verify")
	}
	return h, nil
}

// checkTyp reads only typ from the header and compares it, before any other
// header rule, so a foreign JWS (an agent card with jku, say) is always
// TYP_REJECTED rather than a parse error.
func checkTyp(raw []byte, p Profile) error {
	if err := StrictJSON(raw, maxHeaderBytes); err != nil {
		return errs.New(p.ParseCode, "header: "+err.Error())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return errs.New(p.ParseCode, "header is not a JSON object")
	}
	var typ string
	if err := json.Unmarshal(fields["typ"], &typ); err != nil || typ == "" {
		return errs.New(p.ParseCode, "header has no string typ")
	}
	if typ != p.Typ {
		return errs.New(errs.TypRejected, fmt.Sprintf("typ %q, want %q", typ, p.Typ))
	}
	return nil
}

func parseHeader(raw []byte, p Profile) (Header, error) {
	var h Header
	if err := StrictJSON(raw, maxHeaderBytes); err != nil {
		return h, errs.New(p.ParseCode, "header: "+err.Error())
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&h); err != nil {
		return h, errs.New(p.ParseCode, "header: "+err.Error())
	}
	// Go matches JSON keys case-insensitively; require the exact spelling.
	canon, err := Canonicalize(h)
	if err != nil || !sameJSON(raw, canon) {
		return h, errs.New(p.ParseCode, "header fields must be exactly alg, kid, typ[, jku]")
	}
	if h.Kid == "" || h.Typ == "" {
		return h, errs.New(p.ParseCode, "header needs kid and typ")
	}
	if h.JKU != "" && !p.AllowJKU {
		return h, errs.New(p.ParseCode, "jku not allowed on "+p.Typ)
	}
	return h, nil
}

// sameJSON reports whether raw has the same JCS form as canon.
func sameJSON(raw, canon []byte) bool {
	c, err := Transform(raw)
	return err == nil && bytes.Equal(c, canon)
}

func findKey(keys []*ecdsa.PublicKey, kid string) *ecdsa.PublicKey {
	for _, k := range keys {
		if k == nil || k.Curve != elliptic.P256() {
			continue
		}
		if t, err := Thumbprint(k); err == nil && t == kid {
			return k
		}
	}
	return nil
}

func verifySig(segs [3][]byte, key *ecdsa.PublicKey, detached bool) error {
	hdr := base64.RawURLEncoding.EncodeToString(segs[0])
	sig := base64.RawURLEncoding.EncodeToString(segs[2])
	algs := []gojose.SignatureAlgorithm{gojose.ES256}
	var obj *gojose.JSONWebSignature
	var err error
	if detached {
		obj, err = gojose.ParseDetached(hdr+".."+sig, segs[1], algs)
	} else {
		obj, err = gojose.ParseSignedCompact(hdr+"."+base64.RawURLEncoding.EncodeToString(segs[1])+"."+sig, algs)
	}
	if err != nil {
		return err
	}
	return obj.DetachedVerify(segs[1], key)
}

// asCode keeps an *errs.Error as is and wraps anything else in code.
func asCode(err error, code errs.Code) error {
	var e *errs.Error
	if errors.As(err, &e) {
		return e
	}
	return errs.New(code, err.Error())
}
