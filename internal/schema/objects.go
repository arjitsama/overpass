package schema

import (
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/arjitsama/overpass/internal/errs"
)

// result returns *v only when err is nil, so a caller never holds a decoded
// but unverified object. It takes a pointer because Go leaves unspecified
// whether a plain variable argument is read before or after a sibling call.
func result[T any](v *T, err error) (T, error) {
	if err != nil {
		var zero T
		return zero, err
	}
	return *v, nil
}

// Accept is one x402-shaped payment option on a quote. Amount is an integer
// in the asset's smallest unit.
type Accept struct {
	Scheme  string `json:"scheme"`
	Network string `json:"network"`
	PayTo   string `json:"payTo"`
	Asset   string `json:"asset"`
	Amount  int64  `json:"amount"`
}

// Quote is a station's price for one pass. Unsigned; the mandate binds it by quote_id.
type Quote struct {
	QuoteID         string   `json:"quote_id"`
	Station         string   `json:"station"`
	NoradID         int64    `json:"norad_id"`
	AOS             int64    `json:"aos"`
	LOS             int64    `json:"los"`
	MaxElevationDeg int64    `json:"max_elevation_deg"`
	Mode            string   `json:"mode"`
	AmountCents     int64    `json:"amount_cents"`
	Accepts         []Accept `json:"accepts"`
	Exp             int64    `json:"exp"`
}

// Validate checks field shapes, not business rules.
func (q *Quote) Validate() error {
	if err := errors.Join(checkID("quote_id", q.QuoteID), checkName("station", q.Station),
		checkNorad(q.NoradID), checkWindow("aos", q.AOS, "los", q.LOS), checkMode(q.Mode),
		checkPositive("exp", q.Exp)); err != nil {
		return err
	}
	if q.MaxElevationDeg < 0 || q.MaxElevationDeg > 90 {
		return fmt.Errorf("max_elevation_deg %d outside 0-90", q.MaxElevationDeg)
	}
	if q.AmountCents < 0 {
		return errors.New("amount_cents must not be negative")
	}
	if len(q.Accepts) == 0 || len(q.Accepts) > 8 {
		return errors.New("accepts must have 1-8 entries")
	}
	for _, a := range q.Accepts {
		if err := errors.Join(checkName("scheme", a.Scheme), checkName("network", a.Network),
			checkName("payTo", a.PayTo), checkName("asset", a.Asset)); err != nil {
			return err
		}
		if a.Amount < 0 {
			return errors.New("accepts amount must not be negative")
		}
	}
	return nil
}

// DecodeQuote strictly decodes a quote.
func DecodeQuote(raw []byte) (Quote, error) {
	var q Quote
	return result(&q, Decode(raw, &q, errs.QuoteParseError))
}

// Mandate is the authority's signed permission for one pass (typ overpass-mandate+jws).
type Mandate struct {
	MandateID      string   `json:"mandate_id"`
	Iss            string   `json:"iss"`
	Sub            string   `json:"sub"`
	Aud            string   `json:"aud"`
	QuoteID        string   `json:"quote_id"`
	Scope          string   `json:"scope"`
	CommandClasses []string `json:"command_classes"`
	MaxAmountCents int64    `json:"max_amount_cents"`
	Nbf            int64    `json:"nbf"`
	Exp            int64    `json:"exp"`
	JKT            string   `json:"jkt"`
	Nonce          string   `json:"nonce"`
}

// Validate checks field shapes. Missing scope or jkt fails here, which is
// MANDATE_PARSE_ERROR in book_pass step 1.
func (m *Mandate) Validate() error {
	_, _, scopeErr := ParseScope(m.Scope)
	if err := errors.Join(checkID("mandate_id", m.MandateID), checkName("iss", m.Iss),
		checkName("sub", m.Sub), checkName("aud", m.Aud), checkID("quote_id", m.QuoteID),
		scopeErr, checkClasses(m.CommandClasses), checkWindow("nbf", m.Nbf, "exp", m.Exp),
		checkB64URL("jkt", m.JKT, 43, 43), checkB64URL("nonce", m.Nonce, 16, 128)); err != nil {
		return err
	}
	if m.MaxAmountCents < 0 {
		return errors.New("max_amount_cents must not be negative")
	}
	return nil
}

// SignMandate signs m with the authority's key.
func SignMandate(m Mandate, key *ecdsa.PrivateKey) (string, error) {
	return sign(MandateProfile, &m, key)
}

// VerifyMandate checks typ, parse and signature (book_pass steps 1-2) against
// the keys in the issuer's trust card.
func VerifyMandate(token string, keys []*ecdsa.PublicKey) (Mandate, error) {
	var m Mandate
	return result(&m, verify(token, MandateProfile, keys, &m))
}

// Satellite is one entry in the satellite registry.
type Satellite struct {
	NoradID          int64    `json:"norad_id"`
	AuthorityANSName string   `json:"authority_ans_name"`
	OpsANSNames      []string `json:"ops_ans_names"`
}

// SatRegistry says which authority and ops agents may act for each satellite
// (typ overpass-satreg+jws). Stations pin its signer at startup.
type SatRegistry struct {
	Iss        string      `json:"iss"`
	Iat        int64       `json:"iat"`
	Satellites []Satellite `json:"satellites"`
}

// Validate checks field shapes and that each NORAD ID appears once.
func (r *SatRegistry) Validate() error {
	if err := errors.Join(checkName("iss", r.Iss), checkPositive("iat", r.Iat)); err != nil {
		return err
	}
	if len(r.Satellites) == 0 || len(r.Satellites) > maxSatellites {
		return fmt.Errorf("satellites must have 1-%d entries", maxSatellites)
	}
	seen := map[int64]bool{}
	for _, s := range r.Satellites {
		if err := errors.Join(checkNorad(s.NoradID), checkName("authority_ans_name", s.AuthorityANSName)); err != nil {
			return err
		}
		if seen[s.NoradID] {
			return fmt.Errorf("norad_id %d listed twice", s.NoradID)
		}
		seen[s.NoradID] = true
		if len(s.OpsANSNames) == 0 || len(s.OpsANSNames) > maxOpsPerSat {
			return fmt.Errorf("ops_ans_names must have 1-%d entries", maxOpsPerSat)
		}
		for _, o := range s.OpsANSNames {
			if err := checkName("ops_ans_names", o); err != nil {
				return err
			}
		}
	}
	return nil
}

// SignSatRegistry signs r.
func SignSatRegistry(r SatRegistry, key *ecdsa.PrivateKey) (string, error) {
	return sign(SatRegProfile, &r, key)
}

// VerifySatRegistry checks r against the pinned signer keys.
func VerifySatRegistry(token string, keys []*ecdsa.PublicKey) (SatRegistry, error) {
	var r SatRegistry
	return result(&r, verify(token, SatRegProfile, keys, &r))
}

// Command is one Ops-signed spacecraft command (typ overpass-cmd+jws). The
// station relays it and never reads or changes body.
type Command struct {
	NoradID   int64           `json:"norad_id"`
	Counter   int64           `json:"counter"`
	MandateID string          `json:"mandate_id"`
	Class     string          `json:"class"`
	Body      json.RawMessage `json:"body"`
	IssuedAt  int64           `json:"issued_at"`
}

// Validate checks field shapes.
func (c *Command) Validate() error {
	return errors.Join(checkNorad(c.NoradID), checkPositive("counter", c.Counter),
		checkID("mandate_id", c.MandateID), checkClass(c.Class), checkBody(c.Body),
		checkPositive("issued_at", c.IssuedAt))
}

// SignCommand signs c with the Ops key.
func SignCommand(c Command, key *ecdsa.PrivateKey) (string, error) {
	return sign(CommandProfile, &c, key)
}

// VerifyCommand checks typ, parse and the Ops signature.
func VerifyCommand(token string, keys []*ecdsa.PublicKey) (Command, error) {
	var c Command
	return result(&c, verify(token, CommandProfile, keys, &c))
}

// CommandRecord is one link of the command hash chain. No timestamps, so Ops
// and the station compute identical records. See internal/chain.
type CommandRecord struct {
	Counter   int64  `json:"counter"`
	MandateID string `json:"mandate_id"`
	Class     string `json:"class"`
	CmdSHA256 string `json:"cmd_sha256"`
	PrevHash  string `json:"prev_hash"`
	Hash      string `json:"hash"`
}

// Validate checks field shapes; internal/chain checks the links.
func (r *CommandRecord) Validate() error {
	return errors.Join(checkPositive("counter", r.Counter), checkID("mandate_id", r.MandateID),
		checkClass(r.Class), checkHex64("cmd_sha256", r.CmdSHA256),
		checkHex64("prev_hash", r.PrevHash), checkHex64("hash", r.Hash))
}

// DecodeCommandRecord strictly decodes a record.
func DecodeCommandRecord(raw []byte) (CommandRecord, error) {
	var r CommandRecord
	return result(&r, Decode(raw, &r, errs.RecordParseError))
}

// Ack results.
const (
	AckAccepted = "accepted"
	AckRejected = "rejected"
)

// Ack is the spacecraft's reply to one command, relayed by the station.
type Ack struct {
	Counter         int64  `json:"counter"`
	Result          string `json:"result"`
	TelemetrySHA256 string `json:"telemetry_sha256,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

// Validate: accepted acks carry a telemetry digest; rejected ones a reason code.
func (a *Ack) Validate() error {
	if err := errors.Join(checkPositive("counter", a.Counter),
		checkOneOf("result", a.Result, AckAccepted, AckRejected)); err != nil {
		return err
	}
	if a.Result == AckAccepted {
		if a.Reason != "" {
			return errors.New("accepted ack must not carry reason")
		}
		return checkHex64("telemetry_sha256", a.TelemetrySHA256)
	}
	if a.TelemetrySHA256 != "" {
		return errors.New("rejected ack must not carry telemetry_sha256")
	}
	return checkName("reason", a.Reason)
}

// DecodeAck strictly decodes an ack.
func DecodeAck(raw []byte) (Ack, error) {
	var a Ack
	return result(&a, Decode(raw, &a, errs.AckParseError))
}

// Check is one named verdict in an audit report.
type Check struct {
	Name   string `json:"name"`
	Result string `json:"result"`
	Code   string `json:"code,omitempty"`
}

// Canary is one active probe the auditor sent.
type Canary struct {
	Probe  string `json:"probe"`
	Result string `json:"result"`
	Code   string `json:"code,omitempty"`
}

// Verdicts.
const (
	Pass     = "pass"
	Fail     = "fail"
	Rejected = "rejected"
	Accepted = "accepted"
)

// AuditReport is the auditor's signed finding for one pass (typ overpass-audit+jws).
type AuditReport struct {
	PassID           string   `json:"pass_id"`
	Iss              string   `json:"iss"`
	Station          string   `json:"station"`
	Iat              int64    `json:"iat"`
	Checks           []Check  `json:"checks"`
	Canaries         []Canary `json:"canaries"`
	ChainHead        string   `json:"chain_head"`
	StationChainHead string   `json:"station_chain_head"`
	MissingAcks      int64    `json:"missing_acks"`
	Verdict          string   `json:"verdict"`
}

// Validate checks field shapes.
func (a *AuditReport) Validate() error {
	if err := errors.Join(checkID("pass_id", a.PassID), checkName("iss", a.Iss),
		checkName("station", a.Station), checkPositive("iat", a.Iat),
		checkHex64("chain_head", a.ChainHead), checkHex64("station_chain_head", a.StationChainHead),
		checkOneOf("verdict", a.Verdict, Pass, Fail)); err != nil {
		return err
	}
	if a.MissingAcks < 0 {
		return errors.New("missing_acks must not be negative")
	}
	if a.Checks == nil || a.Canaries == nil {
		return errors.New("checks and canaries must be arrays (possibly empty), not null")
	}
	if len(a.Checks) > maxChecks || len(a.Canaries) > maxChecks {
		return fmt.Errorf("at most %d checks and %d canaries", maxChecks, maxChecks)
	}
	for _, c := range a.Checks {
		if err := errors.Join(checkID("check name", c.Name), checkOneOf("check result", c.Result, Pass, Fail)); err != nil {
			return err
		}
	}
	for _, c := range a.Canaries {
		if err := errors.Join(checkID("canary probe", c.Probe), checkOneOf("canary result", c.Result, Rejected, Accepted)); err != nil {
			return err
		}
	}
	return nil
}

// SignAuditReport signs a with the auditor's key.
func SignAuditReport(a AuditReport, key *ecdsa.PrivateKey) (string, error) {
	return sign(AuditProfile, &a, key)
}

// VerifyAuditReport checks a report against the auditor's keys.
func VerifyAuditReport(token string, keys []*ecdsa.PublicKey) (AuditReport, error) {
	var a AuditReport
	return result(&a, verify(token, AuditProfile, keys, &a))
}

// BookingReceipt is the station's signed record of a booking (typ
// overpass-receipt+jws), which the auditor verifies later.
type BookingReceipt struct {
	BookingID   string `json:"booking_id"`
	Station     string `json:"station"`
	MandateID   string `json:"mandate_id"`
	QuoteID     string `json:"quote_id"`
	NoradID     int64  `json:"norad_id"`
	Nbf         int64  `json:"nbf"`
	Exp         int64  `json:"exp"`
	AmountCents int64  `json:"amount_cents"`
	Iat         int64  `json:"iat"`
}

// Validate checks field shapes.
func (b *BookingReceipt) Validate() error {
	if err := errors.Join(checkID("booking_id", b.BookingID), checkName("station", b.Station),
		checkID("mandate_id", b.MandateID), checkID("quote_id", b.QuoteID), checkNorad(b.NoradID),
		checkWindow("nbf", b.Nbf, "exp", b.Exp), checkPositive("iat", b.Iat)); err != nil {
		return err
	}
	if b.AmountCents < 0 {
		return errors.New("amount_cents must not be negative")
	}
	return nil
}

// SignBookingReceipt signs b with the station's key.
func SignBookingReceipt(b BookingReceipt, key *ecdsa.PrivateKey) (string, error) {
	return sign(ReceiptProfile, &b, key)
}

// VerifyBookingReceipt checks a receipt against the station's keys.
func VerifyBookingReceipt(token string, keys []*ecdsa.PublicKey) (BookingReceipt, error) {
	var b BookingReceipt
	return result(&b, verify(token, ReceiptProfile, keys, &b))
}
