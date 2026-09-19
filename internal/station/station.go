// Package station implements a ground station's skills: get_pass_quote and
// book_pass (master plan 8.1, 8.2, 8.5-8.8).
package station

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"time"

	"github.com/agentnameservice/ans-sdk-go/pop"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/jose"
	"github.com/arjitsama/overpass/internal/schema"
	"github.com/arjitsama/overpass/internal/store"
)

// Limits on what a station will quote.
const (
	QuoteTTL      = 10 * time.Minute
	MaxLead       = 48 * time.Hour
	MaxPassLength = 20 * time.Minute
	maxArgsBytes  = schema.MaxObjectBytes
)

// Caller is the proven identity of whoever called: from the DPoP guard.
type Caller struct {
	ANSName string
	AgentID string
	JKT     string
}

// CallerFunc reads the caller from a request context.
type CallerFunc func(ctx context.Context) (Caller, bool)

// PopCaller reads the identity pop.Middleware proved.
func PopCaller(ctx context.Context) (Caller, bool) {
	id, ok := pop.CallerFromContext(ctx)
	if !ok || id == nil {
		return Caller{}, false
	}
	return Caller{ANSName: id.AnsName, AgentID: id.AgentID, JKT: id.JKT}, true
}

// KeySource returns an issuer's trusted mandate-signing keys (check 2).
type KeySource interface {
	Keys(ctx context.Context, ansName string) ([]*ecdsa.PublicKey, error)
}

// StaticKeys pins authority keys from config.
type StaticKeys map[string][]*ecdsa.PublicKey

// Keys implements KeySource.
func (s StaticKeys) Keys(_ context.Context, ansName string) ([]*ecdsa.PublicKey, error) {
	return s[ansName], nil
}

// LoadStaticKeys reads PEM public keys for each configured authority.
func LoadStaticKeys(ks []config.AuthorityKey) (StaticKeys, error) {
	out := StaticKeys{}
	for _, k := range ks {
		pub, err := LoadPublicKey(k.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("authority key for %s: %w", k.ANSName, err)
		}
		out[k.ANSName] = append(out[k.ANSName], pub)
	}
	return out, nil
}

// LoadPublicKey reads an EC P-256 public key from a PEM file.
func LoadPublicKey(path string) (*ecdsa.PublicKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	k, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("not an EC public key")
	}
	return k, nil
}

// LoadRegistry reads the signed satellite registry and verifies it against
// the pinned signer key.
func LoadRegistry(r config.SatReg) (schema.SatRegistry, error) {
	tok, err := os.ReadFile(r.File)
	if err != nil {
		return schema.SatRegistry{}, fmt.Errorf("satellite registry: %w", err)
	}
	pub, err := LoadPublicKey(r.SignerKeyFile)
	if err != nil {
		return schema.SatRegistry{}, fmt.Errorf("satellite registry signer: %w", err)
	}
	return schema.VerifySatRegistry(string(bytes.TrimSpace(tok)), []*ecdsa.PublicKey{pub})
}

// Station holds what the skills need.
type Station struct {
	ANSName  string
	Pricing  config.Pricing
	Key      *ecdsa.PrivateKey // identity key: signs booking receipts
	Store    *store.Store
	Registry schema.SatRegistry
	Keys     KeySource
	Caller   CallerFunc
	Rogue    bool // skip checks 2 and 9: the rogue station of master plan 10
	Emit     func(bus.Event)
	Now      func() time.Time
	Log      *slog.Logger
}

func (s *Station) emit(kind, subject, result string, code errs.Code, data map[string]any) {
	if s.Emit != nil {
		s.Emit(bus.Event{Agent: s.ANSName, Kind: kind, Subject: subject, Result: result, Reason: string(code), Data: data})
	}
}

// decodeArgs strictly decodes a skill's data part (integers only, no
// unknown fields) into v; failures carry code.
func decodeArgs(raw json.RawMessage, v any, code errs.Code) error {
	if err := jose.StrictJSON(raw, maxArgsBytes); err != nil {
		return errs.New(code, err.Error())
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errs.New(code, "arguments: "+err.Error())
	}
	return nil
}

// randomID is prefix + 24 hex characters (IDs allow [A-Za-z0-9._:-] only).
func randomID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// satellite returns the registry entry for noradID.
func (s *Station) satellite(noradID int64) (schema.Satellite, bool) {
	for _, sat := range s.Registry.Satellites {
		if sat.NoradID == noradID {
			return sat, true
		}
	}
	return schema.Satellite{}, false
}

type quoteArgs struct {
	Skill           string `json:"skill"`
	NoradID         int64  `json:"norad_id"`
	AOS             int64  `json:"aos"`
	LOS             int64  `json:"los"`
	Mode            string `json:"mode"`
	MaxElevationDeg int64  `json:"max_elevation_deg"`
}

// GetPassQuote prices one pass (master plan 8.1): per pass-minute in whole
// cents, with an x402-shaped accepts block whose payTo is the one in the
// signed card, valid for QuoteTTL.
func (s *Station) GetPassQuote(ctx context.Context, raw json.RawMessage) (any, error) {
	var a quoteArgs
	if err := decodeArgs(raw, &a, errs.QuoteParseError); err != nil {
		return nil, err
	}
	now := s.Now()
	if a.Mode != schema.ModeUplink && a.Mode != schema.ModeDownlink {
		return nil, errs.New(errs.QuoteParseError, "mode must be uplink or downlink")
	}
	if _, ok := s.satellite(a.NoradID); !ok {
		return nil, errs.New(errs.QuoteRejectedNorad, fmt.Sprintf("norad_id %d is not in this station's satellite registry", a.NoradID))
	}
	dur := time.Duration(a.LOS-a.AOS) * time.Second
	if a.AOS <= now.Unix() || a.AOS > now.Add(MaxLead).Unix() || dur <= 0 || dur > MaxPassLength ||
		a.MaxElevationDeg < 0 || a.MaxElevationDeg > 90 {
		return nil, errs.New(errs.QuoteRejectedWindow, "window must start within 48 h, last at most 20 min, max elevation 0-90")
	}
	if s.Pricing.PayTo == "" {
		return nil, errs.New(errs.Unavailable, "this station has no pricing configured")
	}
	minutes := (a.LOS - a.AOS + 59) / 60
	cents := s.Pricing.PerMinuteCents * minutes // bounded by config: per_minute_cents <= MaxPerMinuteCents, minutes <= 20
	scale := pow10(s.Pricing.AssetDecimals - 2)
	if cents > math.MaxInt64/scale || cents*scale > jose.MaxSafeInt {
		return nil, errs.New(errs.QuoteRejectedWindow, "price in asset units exceeds the integer range")
	}
	q := schema.Quote{QuoteID: randomID("q-"), Station: s.ANSName, NoradID: a.NoradID, AOS: a.AOS, LOS: a.LOS,
		MaxElevationDeg: a.MaxElevationDeg, Mode: a.Mode, AmountCents: cents,
		Accepts: []schema.Accept{{Scheme: "exact", Network: s.Pricing.Network, PayTo: s.Pricing.PayTo,
			Asset: s.Pricing.Asset, Amount: cents * scale}},
		Exp: now.Add(QuoteTTL).Unix()}
	if err := q.Validate(); err != nil {
		return nil, errs.New(errs.Internal, "built an invalid quote: "+err.Error())
	}
	body, err := jose.Canonicalize(q)
	if err != nil {
		return nil, err
	}
	if err := s.Store.PutQuote(ctx, q, body); err != nil {
		return nil, err
	}
	s.emit("quote", q.QuoteID, "ok", "", map[string]any{"norad_id": q.NoradID, "aos": q.AOS, "amount_cents": q.AmountCents})
	return q, nil
}

func pow10(n int64) int64 {
	out := int64(1)
	for i := int64(0); i < n; i++ {
		out *= 10
	}
	return out
}

type bookArgs struct {
	Skill   string `json:"skill"`
	QuoteID string `json:"quote_id"`
	Mandate string `json:"mandate"`
}

// BookResult is book_pass's answer.
type BookResult struct {
	BookingID string `json:"booking_id"`
	Nbf       int64  `json:"nbf"`
	Exp       int64  `json:"exp"`
	Receipt   string `json:"receipt"` // overpass-receipt+jws, station-signed
}

// BookPass runs master plan 8.5's checks in their exact order, each with its
// own code, and never panics. Check 10 (DPoP jti unseen) runs earlier, in the
// transport (pop.Middleware with the store's replay cache), because the SDK
// does not hand the jti to handlers.
func (s *Station) BookPass(ctx context.Context, raw json.RawMessage) (out any, err error) {
	defer func() {
		if r := recover(); r != nil {
			s.Log.Error("book_pass panic", "panic", r)
			out, err = nil, errs.New(errs.Internal, "book_pass failed")
		}
	}()
	res, err := s.book(ctx, raw)
	if err != nil {
		var e *errs.Error
		code := errs.Internal
		if errors.As(err, &e) {
			code = e.Code
		}
		s.emit("book_pass", "", "rejected", code, nil)
		return nil, err
	}
	s.emit("book_pass", res.BookingID, "ok", "", map[string]any{"nbf": res.Nbf, "exp": res.Exp})
	return res, nil
}

func (s *Station) book(ctx context.Context, raw json.RawMessage) (BookResult, error) {
	// 1. Parse; typ is the mandate type; scope, jkt and signature present.
	var a bookArgs
	if err := decodeArgs(raw, &a, errs.MandateParseError); err != nil {
		return BookResult{}, err
	}
	if a.Mandate == "" {
		return BookResult{}, errs.New(errs.MandateParseError, "mandate is missing")
	}
	m, err := schema.PeekMandate(a.Mandate)
	if err != nil {
		return BookResult{}, err
	}
	// 2. Signature verifies under a key the issuer publishes.
	if !s.Rogue {
		keys, err := s.Keys.Keys(ctx, m.Iss)
		if err != nil {
			return BookResult{}, errs.New(errs.MandateRejectedSignature, "issuer keys unavailable: "+err.Error())
		}
		if m, err = schema.VerifyMandate(a.Mandate, keys); err != nil {
			return BookResult{}, err
		}
	}
	// 3. Ownership: the registry names iss as this satellite's authority and sub as one of its ops agents.
	_, norad, _ := schema.ParseScope(m.Scope)
	sat, ok := s.satellite(norad)
	if !ok || sat.AuthorityANSName != m.Iss || !contains(sat.OpsANSNames, m.Sub) {
		return BookResult{}, errs.New(errs.MandateRejectedNotOwner, fmt.Sprintf("%s is not the registered authority (or %s not an ops agent) for %d", m.Iss, m.Sub, norad))
	}
	// 4. Audience is this station.
	if m.Aud != s.ANSName {
		return BookResult{}, errs.New(errs.MandateRejectedAudience, fmt.Sprintf("mandate is for %s, this is %s", m.Aud, s.ANSName))
	}
	// 5. quote_id exists, is unexpired and is the quote being booked.
	if a.QuoteID != m.QuoteID {
		return BookResult{}, errs.New(errs.MandateRejectedQuote, "mandate names a different quote")
	}
	q, found, err := s.Store.GetQuote(ctx, m.QuoteID)
	if err != nil {
		return BookResult{}, err
	}
	if !found {
		return BookResult{}, errs.New(errs.MandateRejectedQuote, "quote unknown or expired")
	}
	// 6. Scope matches the quote's mode and NORAD ID.
	if m.Scope != schema.Scope(q.Mode, q.NoradID) {
		return BookResult{}, errs.New(errs.MandateRejectedScope, fmt.Sprintf("scope %s, quote is %s", m.Scope, schema.Scope(q.Mode, q.NoradID)))
	}
	// 7. The mandate covers the quoted amount.
	if m.MaxAmountCents < q.AmountCents {
		return BookResult{}, errs.New(errs.MandateRejectedAmount, fmt.Sprintf("max_amount_cents %d below quote %d", m.MaxAmountCents, q.AmountCents))
	}
	// 8. The window is the quote's, and not over.
	now := s.Now().Unix()
	if m.Nbf != q.AOS || m.Exp != q.LOS || m.Exp <= now {
		return BookResult{}, errs.New(errs.MandateRejectedWindow, "nbf/exp must equal the quote's AOS/LOS and exp must be in the future")
	}
	// 9. The DPoP proof's key is the one the mandate was issued to.
	if !s.Rogue {
		c, ok := s.Caller(ctx)
		if !ok || c.JKT != m.JKT {
			return BookResult{}, errs.New(errs.DPoPRejectedKey, "DPoP key thumbprint does not equal the mandate's jkt")
		}
	}
	// 11 and 12 (and the insert) in one transaction.
	bookingID := randomID("b-")
	receipt, err := schema.SignBookingReceipt(schema.BookingReceipt{BookingID: bookingID, Station: s.ANSName,
		MandateID: m.MandateID, QuoteID: q.QuoteID, NoradID: q.NoradID, Nbf: m.Nbf, Exp: m.Exp,
		AmountCents: q.AmountCents, Iat: now}, s.Key)
	if err != nil {
		return BookResult{}, err
	}
	if err := s.Store.Book(ctx, store.Booking{BookingID: bookingID, QuoteID: q.QuoteID, MandateID: m.MandateID,
		Iss: m.Iss, Nonce: m.Nonce, Mandate: a.Mandate, NoradID: q.NoradID, Nbf: m.Nbf, Exp: m.Exp, Receipt: receipt}); err != nil {
		return BookResult{}, err
	}
	return BookResult{BookingID: bookingID, Nbf: m.Nbf, Exp: m.Exp, Receipt: receipt}, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
