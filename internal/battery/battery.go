package battery

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/jose"
	"github.com/arjitsama/overpass/internal/schema"
	"github.com/arjitsama/overpass/internal/verify"
	"github.com/arjitsama/overpass/internal/wellknown"
)

// Verdicts.
const (
	Blocked      = "BLOCKED"      // the attack was refused with the expected code
	Vulnerable   = "VULNERABLE"   // the attack succeeded, or got the wrong refusal
	Inconclusive = "INCONCLUSIVE" // the attack could not be run (target not wired, transport error)
)

// Result is one attack's outcome.
type Result struct {
	Name     string    `json:"name"`
	Verdict  string    `json:"verdict"`
	Expected errs.Code `json:"expected"`
	Observed errs.Code `json:"observed"`
	Detail   string    `json:"detail,omitempty"`
}

// Battery runs the attacks against one target station over A2A.
type Battery struct {
	HTTP        HTTPDoer
	StationURL  string // the target station's A2A URL (POST /)
	StationANS  string // the target station's ANS name (mandate audience)
	StationHost string // for jku_injection / card checks
	SpaceURL    string // the spacecraft's A2A URL, or "" to skip command attacks

	Ops       *verify.Outbound // primary registered Ops identity (DPoP)
	OpsANS    string
	OpsJKT    string
	OpsKey    *ecdsa.PrivateKey // signs commands; the spacecraft trusts its public half
	OpsWrong  *verify.Outbound  // a second registered Ops identity (wrong_dpop_key)
	OpsWrong2 *ecdsa.PrivateKey // its command-signing key (forged_command)

	AuthKey       *ecdsa.PrivateKey // trusted by the station as the satellite's authority
	AuthName      string
	OtherAuthKey  *ecdsa.PrivateKey // trusted as a different registered authority (not_owner)
	OtherAuthName string
	UnknownKey    *ecdsa.PrivateKey // not trusted (unknown_key_mandate)

	NoradID      int64
	OtherStation string // an ANS name that is not this station (wrong_audience)

	Now  func() time.Time
	Emit func(bus.Event)
}

// HTTPDoer is the subset of *http.Client the battery uses.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func (b *Battery) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *Battery) emit(r Result) {
	if b.Emit != nil {
		b.Emit(bus.Event{Agent: b.OpsANS, Kind: "battery_attack", Subject: r.Name, Result: r.Verdict,
			Reason: string(r.Observed), Data: map[string]any{"expected": string(r.Expected), "detail": r.Detail}})
	}
}

// verdict decides an attack that expects a rejection code.
func verdict(name string, expected, observed errs.Code, transportErr error) Result {
	r := Result{Name: name, Expected: expected, Observed: observed}
	switch {
	case transportErr != nil:
		r.Verdict, r.Detail = Inconclusive, transportErr.Error()
	case observed == expected:
		r.Verdict = Blocked
	case observed == "":
		r.Verdict, r.Detail = Vulnerable, "the attack succeeded (no rejection)"
	default:
		r.Verdict, r.Detail = Vulnerable, fmt.Sprintf("refused, but with %s not %s", observed, expected)
	}
	return r
}

// --- mandate helpers ---------------------------------------------------------

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// template is a valid mandate for q from issuer iss, bound to the primary
// Ops DPoP key.
func (b *Battery) template(q schema.Quote, iss string) schema.Mandate {
	return schema.Mandate{MandateID: "m-" + randHex(12), Iss: iss, Sub: b.OpsANS, Aud: q.Station,
		QuoteID: q.QuoteID, Scope: schema.Scope(q.Mode, q.NoradID), CommandClasses: []string{"telemetry"},
		MaxAmountCents: q.AmountCents, Nbf: q.AOS, Exp: q.LOS, JKT: b.OpsJKT, Nonce: randHex(12)}
}

// mutate edits a signed mandate's payload and keeps the old signature.
func mutate(tok string, edit func(map[string]any)) (string, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("not a compact JWS")
	}
	raw, err := jose.B64Decode(parts[1])
	if err != nil {
		return "", err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", err
	}
	edit(m)
	payload, err := jose.Canonicalize(m)
	if err != nil {
		return "", err
	}
	return parts[0] + "." + jose.B64Encode(payload) + "." + parts[2], nil
}

// flipSig flips the last two bytes of the decoded signature and re-encodes,
// so the result is always valid base64url (a decodable but wrong signature),
// giving a deterministic MANDATE_REJECTED:signature (master plan #9).
func flipSig(tok string) string {
	parts := strings.Split(tok, ".")
	sig, err := jose.B64Decode(parts[2])
	if err != nil || len(sig) < 2 {
		return tok
	}
	sig[len(sig)-1] ^= 0x01
	sig[len(sig)-2] ^= 0x01
	return parts[0] + "." + parts[1] + "." + jose.B64Encode(sig)
}

// --- target operations -------------------------------------------------------

// FreshQuote gets a real quote for a window starting lead from now.
func (b *Battery) FreshQuote(ctx context.Context, mode string, lead, dur time.Duration) (schema.Quote, error) {
	aos := b.now().Add(lead).Unix()
	data, code, err := b.call(ctx, b.StationURL, b.Ops, "get_pass_quote", map[string]any{
		"norad_id": b.NoradID, "aos": aos, "los": aos + int64(dur.Seconds()), "mode": mode, "max_elevation_deg": 45})
	if err != nil {
		return schema.Quote{}, err
	}
	if code != "" {
		return schema.Quote{}, fmt.Errorf("get_pass_quote refused: %s", code)
	}
	var q schema.Quote
	return q, json.Unmarshal(data, &q)
}

func (b *Battery) sign(m schema.Mandate, key *ecdsa.PrivateKey) (string, error) {
	return schema.SignMandate(m, key)
}

// book attempts book_pass and returns the observed code.
func (b *Battery) book(ctx context.Context, sign signer, quoteID, mandate string) (errs.Code, error) {
	_, code, err := b.call(ctx, b.StationURL, sign, "book_pass", map[string]any{"quote_id": quoteID, "mandate": mandate})
	return code, err
}

// --- attacks (master plan section 10 table) ---------------------------------

func (b *Battery) replayBooking(ctx context.Context) Result {
	q, err := b.FreshQuote(ctx, schema.ModeUplink, time.Hour, 8*time.Minute)
	if err != nil {
		return verdict("replay_booking", errs.DPoPRejectedReplay, "", err)
	}
	tok, _ := b.sign(b.template(q, b.AuthName), b.AuthKey)
	first, second, err := b.callReplay(ctx, b.StationURL, b.Ops, "book_pass", map[string]any{"quote_id": q.QuoteID, "mandate": tok})
	if err != nil {
		return verdict("replay_booking", errs.DPoPRejectedReplay, "", err)
	}
	r := verdict("replay_booking", errs.DPoPRejectedReplay, second, nil)
	if first != "" {
		r.Verdict, r.Detail = Inconclusive, "the legitimate first booking was refused: "+string(first)
	}
	return r
}

func (b *Battery) underpayBooking(ctx context.Context) Result {
	q, err := b.FreshQuote(ctx, schema.ModeUplink, time.Hour, 8*time.Minute)
	if err != nil {
		return verdict("underpay_booking", errs.MandateRejectedSignature, "", err)
	}
	tok, _ := b.sign(b.template(q, b.AuthName), b.AuthKey)
	tampered, err := mutate(tok, func(m map[string]any) { m["max_amount_cents"] = float64(1) })
	if err != nil {
		return verdict("underpay_booking", errs.MandateRejectedSignature, "", err)
	}
	code, err := b.book(ctx, b.Ops, q.QuoteID, tampered)
	return verdict("underpay_booking", errs.MandateRejectedSignature, code, err)
}

func (b *Battery) tamperMandate(ctx context.Context) Result {
	q, err := b.FreshQuote(ctx, schema.ModeUplink, time.Hour, 8*time.Minute)
	if err != nil {
		return verdict("tamper_mandate", errs.MandateRejectedSignature, "", err)
	}
	tok, _ := b.sign(b.template(q, b.AuthName), b.AuthKey)
	tampered, err := mutate(tok, func(m map[string]any) {
		m["command_classes"] = []any{"telemetry", "attitude", "reboot"}
	})
	if err != nil {
		return verdict("tamper_mandate", errs.MandateRejectedSignature, "", err)
	}
	code, err := b.book(ctx, b.Ops, q.QuoteID, tampered)
	return verdict("tamper_mandate", errs.MandateRejectedSignature, code, err)
}

func (b *Battery) underpayValidSig(ctx context.Context) Result {
	q, err := b.FreshQuote(ctx, schema.ModeUplink, time.Hour, 8*time.Minute)
	if err != nil {
		return verdict("underpay_valid_sig", errs.MandateRejectedAmount, "", err)
	}
	m := b.template(q, b.AuthName)
	m.MaxAmountCents = q.AmountCents - 1
	tok, _ := b.sign(m, b.AuthKey)
	code, err := b.book(ctx, b.Ops, q.QuoteID, tok)
	return verdict("underpay_valid_sig", errs.MandateRejectedAmount, code, err)
}

func (b *Battery) quoteSwap(ctx context.Context) Result {
	a, err := b.FreshQuote(ctx, schema.ModeUplink, time.Hour, 8*time.Minute)
	if err != nil {
		return verdict("quote_swap_attack", errs.MandateRejectedQuote, "", err)
	}
	other, err := b.FreshQuote(ctx, schema.ModeUplink, 2*time.Hour, 8*time.Minute)
	if err != nil {
		return verdict("quote_swap_attack", errs.MandateRejectedQuote, "", err)
	}
	tok, _ := b.sign(b.template(a, b.AuthName), b.AuthKey)
	code, err := b.book(ctx, b.Ops, other.QuoteID, tok)
	return verdict("quote_swap_attack", errs.MandateRejectedQuote, code, err)
}

func (b *Battery) wrongAudience(ctx context.Context) Result {
	q, err := b.FreshQuote(ctx, schema.ModeUplink, time.Hour, 8*time.Minute)
	if err != nil {
		return verdict("wrong_audience_attack", errs.MandateRejectedAudience, "", err)
	}
	m := b.template(q, b.AuthName)
	m.Aud = b.OtherStation
	tok, _ := b.sign(m, b.AuthKey)
	code, err := b.book(ctx, b.Ops, q.QuoteID, tok)
	return verdict("wrong_audience_attack", errs.MandateRejectedAudience, code, err)
}

func (b *Battery) wrongScope(ctx context.Context) Result {
	q, err := b.FreshQuote(ctx, schema.ModeUplink, time.Hour, 8*time.Minute)
	if err != nil {
		return verdict("wrong_scope_attack", errs.MandateRejectedScope, "", err)
	}
	m := b.template(q, b.AuthName)
	m.Scope = schema.Scope(schema.ModeDownlink, b.NoradID)
	tok, _ := b.sign(m, b.AuthKey)
	code, err := b.book(ctx, b.Ops, q.QuoteID, tok)
	return verdict("wrong_scope_attack", errs.MandateRejectedScope, code, err)
}

func (b *Battery) wrongDPoPKey(ctx context.Context) Result {
	// This attack needs a *second* registered identity to sign with. Where only
	// one is registered it cannot run, and says so: an attack that never
	// reached the target is never evidence that the target blocked it.
	if b.OpsWrong == nil {
		return Result{Name: "wrong_dpop_key_attack", Verdict: Inconclusive, Expected: errs.DPoPRejectedKey,
			Detail: "needs a second registered Ops identity; none is configured"}
	}
	q, err := b.FreshQuote(ctx, schema.ModeUplink, time.Hour, 8*time.Minute)
	if err != nil {
		return verdict("wrong_dpop_key_attack", errs.DPoPRejectedKey, "", err)
	}
	tok, _ := b.sign(b.template(q, b.AuthName), b.AuthKey) // jkt = primary Ops key
	code, err := b.book(ctx, b.OpsWrong, q.QuoteID, tok)   // but signed by the second identity
	return verdict("wrong_dpop_key_attack", errs.DPoPRejectedKey, code, err)
}

func (b *Battery) corruptJWS(ctx context.Context) Result {
	q, err := b.FreshQuote(ctx, schema.ModeUplink, time.Hour, 8*time.Minute)
	if err != nil {
		return verdict("corrupt_jws_attack", errs.MandateRejectedSignature, "", err)
	}
	tok, _ := b.sign(b.template(q, b.AuthName), b.AuthKey)
	code, err := b.book(ctx, b.Ops, q.QuoteID, flipSig(tok))
	r := verdict("corrupt_jws_attack", errs.MandateRejectedSignature, code, err)
	if r.Verdict == Blocked {
		r.Detail = "never a 500"
	}
	return r
}

func (b *Battery) supersededFormat(ctx context.Context) Result {
	q, err := b.FreshQuote(ctx, schema.ModeUplink, time.Hour, 8*time.Minute)
	if err != nil {
		return verdict("superseded_format_attack", errs.MandateParseError, "", err)
	}
	tok, _ := b.sign(b.template(q, b.AuthName), b.AuthKey)
	stripped, err := mutate(tok, func(m map[string]any) { delete(m, "scope"); delete(m, "jkt") })
	if err != nil {
		return verdict("superseded_format_attack", errs.MandateParseError, "", err)
	}
	code, err := b.book(ctx, b.Ops, q.QuoteID, stripped)
	return verdict("superseded_format_attack", errs.MandateParseError, code, err)
}

func (b *Battery) unknownKey(ctx context.Context) Result {
	q, err := b.FreshQuote(ctx, schema.ModeUplink, time.Hour, 8*time.Minute)
	if err != nil {
		return verdict("unknown_key_mandate", errs.MandateRejectedSignature, "", err)
	}
	tok, _ := b.sign(b.template(q, b.AuthName), b.UnknownKey)
	code, err := b.book(ctx, b.Ops, q.QuoteID, tok)
	return verdict("unknown_key_mandate", errs.MandateRejectedSignature, code, err)
}

func (b *Battery) replaySettled(ctx context.Context) Result {
	// A window distinct from replay_booking's, so this real booking does not
	// overlap that one.
	q, err := b.FreshQuote(ctx, schema.ModeUplink, 90*time.Minute, 8*time.Minute)
	if err != nil {
		return verdict("replay_settled", errs.MandateRejectedConsumed, "", err)
	}
	tok, _ := b.sign(b.template(q, b.AuthName), b.AuthKey)
	if first, err := b.book(ctx, b.Ops, q.QuoteID, tok); err != nil || first != "" {
		return verdict("replay_settled", errs.MandateRejectedConsumed, first, err)
	}
	code, err := b.book(ctx, b.Ops, q.QuoteID, tok) // fresh DPoP proof, spent mandate
	return verdict("replay_settled", errs.MandateRejectedConsumed, code, err)
}

func (b *Battery) canonicalizationProbe(ctx context.Context) Result {
	q, err := b.FreshQuote(ctx, schema.ModeUplink, time.Hour, 8*time.Minute)
	if err != nil {
		return verdict("canonicalization_probe", errs.MandateParseError, "", err)
	}
	tok, _ := b.sign(b.template(q, b.AuthName), b.AuthKey)
	// Rewrite an integer as a float in the payload; strict decode rejects it.
	parts := strings.Split(tok, ".")
	raw, _ := jose.B64Decode(parts[1])
	floaty := strings.Replace(string(raw), fmt.Sprintf("\"max_amount_cents\":%d", q.AmountCents),
		fmt.Sprintf("\"max_amount_cents\":%d.0", q.AmountCents), 1)
	probe := parts[0] + "." + jose.B64Encode([]byte(floaty)) + "." + parts[2]
	code, err := b.book(ctx, b.Ops, q.QuoteID, probe)
	return verdict("canonicalization_probe", errs.MandateParseError, code, err)
}

func (b *Battery) payToBinding(ctx context.Context) Result {
	r := Result{Name: "payto_binding_check"}
	card, err := b.get(ctx, b.StationURL+wellknown.PathCard)
	if err != nil {
		r.Verdict, r.Detail = Inconclusive, err.Error()
		return r
	}
	var c struct {
		XPayment struct {
			PayTo string `json:"payTo"`
		} `json:"x-payment"`
	}
	_ = json.Unmarshal(card, &c)
	q, err := b.FreshQuote(ctx, schema.ModeUplink, time.Hour, 8*time.Minute)
	if err != nil {
		r.Verdict, r.Detail = Inconclusive, err.Error()
		return r
	}
	switch {
	case c.XPayment.PayTo == "":
		r.Verdict, r.Detail = Vulnerable, "the card declares no payTo"
	case len(q.Accepts) == 0 || q.Accepts[0].PayTo != c.XPayment.PayTo:
		r.Verdict, r.Detail = Vulnerable, "quote payTo is not the one in the signed card"
	default:
		r.Verdict, r.Detail = Blocked, "quote payTo "+c.XPayment.PayTo+" is attested in the signed card"
	}
	return r
}

func (b *Battery) cardDrift(ctx context.Context) Result {
	r := Result{Name: "card_drift_watch"}
	first, err := b.get(ctx, b.StationURL+wellknown.PathCard)
	if err != nil {
		r.Verdict, r.Detail = Inconclusive, err.Error()
		return r
	}
	second, err := b.get(ctx, b.StationURL+wellknown.PathCard)
	if err != nil {
		r.Verdict, r.Detail = Inconclusive, err.Error()
		return r
	}
	if sha256.Sum256(first) != sha256.Sum256(second) {
		r.Verdict, r.Detail = Vulnerable, "the served card changed between fetches"
		return r
	}
	r.Verdict, r.Detail = Blocked, "card hash stable: "+hex.EncodeToString(sha256Sum(first))
	return r
}

func sha256Sum(b []byte) []byte { s := sha256.Sum256(b); return s[:] }

// --- Overpass-only attacks ---------------------------------------------------

func (b *Battery) notOwner(ctx context.Context) Result {
	q, err := b.FreshQuote(ctx, schema.ModeUplink, time.Hour, 8*time.Minute)
	if err != nil {
		return verdict("not_owner", errs.MandateRejectedNotOwner, "", err)
	}
	m := b.template(q, b.OtherAuthName) // a registered authority that does not own the satellite
	tok, _ := b.sign(m, b.OtherAuthKey)
	code, err := b.book(ctx, b.Ops, q.QuoteID, tok)
	return verdict("not_owner", errs.MandateRejectedNotOwner, code, err)
}

func (b *Battery) overlap(ctx context.Context) Result {
	first, err := b.FreshQuote(ctx, schema.ModeUplink, 3*time.Hour, 8*time.Minute)
	if err != nil {
		return verdict("overlap", errs.BookingRejectedOverlap, "", err)
	}
	tok, _ := b.sign(b.template(first, b.AuthName), b.AuthKey)
	if code, err := b.book(ctx, b.Ops, first.QuoteID, tok); err != nil || code != "" {
		return verdict("overlap", errs.BookingRejectedOverlap, code, err)
	}
	// A second quote whose window overlaps the first, then book it.
	aos := first.AOS + 60
	data, qcode, err := b.call(ctx, b.StationURL, b.Ops, "get_pass_quote", map[string]any{
		"norad_id": b.NoradID, "aos": aos, "los": first.LOS + 60, "mode": schema.ModeUplink, "max_elevation_deg": 45})
	if err != nil || qcode != "" {
		return verdict("overlap", errs.BookingRejectedOverlap, qcode, err)
	}
	var second schema.Quote
	_ = json.Unmarshal(data, &second)
	tok2, _ := b.sign(b.template(second, b.AuthName), b.AuthKey)
	code, err := b.book(ctx, b.Ops, second.QuoteID, tok2)
	return verdict("overlap", errs.BookingRejectedOverlap, code, err)
}

func (b *Battery) typConfusion(ctx context.Context) Result {
	q, err := b.FreshQuote(ctx, schema.ModeUplink, time.Hour, 8*time.Minute)
	if err != nil {
		return verdict("typ_confusion", errs.TypRejected, "", err)
	}
	// A command JWS presented where a mandate is expected.
	cmd, _ := schema.SignCommand(schema.Command{NoradID: b.NoradID, Counter: 1, MandateID: "m-x", Class: "telemetry",
		Body: json.RawMessage(`{"op":"dump"}`), IssuedAt: b.now().Unix()}, b.AuthKey)
	code, err := b.book(ctx, b.Ops, q.QuoteID, cmd)
	return verdict("typ_confusion", errs.TypRejected, code, err)
}

// jkuInjection forges a card whose signature jku points at another host and
// checks the verifier rejects it before any fetch (CARD_REJECTED:jku).
func (b *Battery) jkuInjection(ctx context.Context) Result {
	payload := []byte(fmt.Sprintf(`{"name":"gs","url":"https://%s"}`, b.StationHost))
	tok, err := jose.SignDetached(jose.TypAgentCard, payload, b.AuthKey, "https://attacker.example"+wellknown.PathTrustCard)
	if err != nil {
		return Result{Name: "jku_injection", Verdict: Inconclusive, Detail: err.Error()}
	}
	parts := strings.Split(tok, "..")
	card := fmt.Sprintf(`{"name":"gs","url":"https://%s","signatures":[{"header":{"kid":"x"},"protected":%q,"signature":%q}]}`,
		b.StationHost, parts[0], parts[1])
	fetched := false
	err = wellknown.VerifyCard([]byte(card), wellknown.Expect{Host: b.StationHost}, func(string) ([]byte, error) {
		fetched = true
		return nil, fmt.Errorf("should not be reached")
	})
	code := codeOf(err)
	r := verdict("jku_injection", errs.CardRejectedJKU, code, nil)
	if fetched {
		r.Verdict, r.Detail = Vulnerable, "the verifier fetched the attacker's jku"
	}
	return r
}

func codeOf(err error) errs.Code {
	var e *errs.Error
	if errs.As(err, &e) {
		return e.Code
	}
	return ""
}
