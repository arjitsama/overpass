package battery

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"time"

	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/schema"
)

// bookLegit books a real pass and returns its booking id and mandate id.
func (b *Battery) bookLegit(ctx context.Context, lead, dur time.Duration) (bookingID, mandateID string, err error) {
	q, err := b.FreshQuote(ctx, schema.ModeUplink, lead, dur)
	if err != nil {
		return "", "", err
	}
	m := b.template(q, b.AuthName)
	tok, _ := b.sign(m, b.AuthKey)
	data, code, err := b.call(ctx, b.StationURL, b.Ops, "book_pass", map[string]any{"quote_id": q.QuoteID, "mandate": tok})
	if err != nil {
		return "", "", err
	}
	if code != "" {
		return "", "", fmt.Errorf("legitimate booking refused: %s", code)
	}
	var res struct {
		BookingID string `json:"booking_id"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return "", "", err
	}
	return res.BookingID, m.MandateID, nil
}

// signCmd signs a spacecraft command with the given key.
func (b *Battery) signCmd(counter int64, mandateID, class string, key *ecdsa.PrivateKey) string {
	tok, _ := schema.SignCommand(schema.Command{NoradID: b.NoradID, Counter: counter, MandateID: mandateID, Class: class,
		Body: json.RawMessage(`{"op":"dump"}`), IssuedAt: b.now().Unix()}, key)
	return tok
}

// uplink calls the spacecraft's uplink skill and returns the Ack.
func (b *Battery) uplink(ctx context.Context, cmdTok string) (schema.Ack, errs.Code, error) {
	data, code, err := b.call(ctx, b.SpaceURL, nil, "uplink", map[string]any{"command": cmdTok})
	if err != nil || code != "" {
		return schema.Ack{}, code, err
	}
	var ack schema.Ack
	return ack, "", json.Unmarshal(data, &ack)
}

func (b *Battery) forgedCommand(ctx context.Context) Result {
	tok := b.signCmd(b.now().UnixMilli(), "m-x", "telemetry", b.OpsWrong2) // not the Ops key the spacecraft trusts
	ack, code, err := b.uplink(ctx, tok)
	if err != nil {
		return verdict("forged_command", errs.CommandRejectedSignature, code, err)
	}
	return ackVerdict("forged_command", errs.CommandRejectedSignature, ack, code)
}

func (b *Battery) replayedCommand(ctx context.Context) Result {
	c := b.now().UnixMilli()
	tok := b.signCmd(c, "m-x", "telemetry", b.OpsKey)
	if ack, code, err := b.uplink(ctx, tok); err != nil || code != "" || ack.Result != schema.AckAccepted {
		return Result{Name: "replayed_command", Expected: errs.CommandRejectedCounter, Verdict: Inconclusive,
			Detail: fmt.Sprintf("the first command was not accepted (code %s, ack %+v)", code, ack)}
	}
	ack, code, err := b.uplink(ctx, tok) // same counter again
	if err != nil {
		return verdict("replayed_command", errs.CommandRejectedCounter, code, err)
	}
	return ackVerdict("replayed_command", errs.CommandRejectedCounter, ack, code)
}

// ackVerdict reads a spacecraft rejection out of the Ack.
func ackVerdict(name string, expected errs.Code, ack schema.Ack, code errs.Code) Result {
	observed := code
	if observed == "" && ack.Result == schema.AckRejected {
		observed = errs.Code(ack.Reason)
	}
	r := verdict(name, expected, observed, nil)
	if observed == "" && ack.Result == schema.AckAccepted {
		r.Verdict, r.Detail = Vulnerable, "the spacecraft accepted the command"
	}
	return r
}

func (b *Battery) lateCommand(ctx context.Context) Result {
	// A booking far in the future: its window has not opened.
	bookingID, mandateID, err := b.bookLegit(ctx, 40*time.Hour, 8*time.Minute)
	if err != nil {
		return verdict("late_command", errs.WindowClosed, "", err)
	}
	tok := b.signCmd(1, mandateID, "telemetry", b.OpsKey)
	_, code, err := b.call(ctx, b.StationURL, b.Ops, "relay_command", map[string]any{"booking_id": bookingID, "command": tok})
	return verdict("late_command", errs.WindowClosed, code, err)
}

func (b *Battery) classEscalation(ctx context.Context) Result {
	// A booking whose window is open now: quote 5 s out, so nbf-30 is in the past.
	bookingID, mandateID, err := b.bookLegit(ctx, 5*time.Second, time.Minute)
	if err != nil {
		return verdict("class_escalation", errs.ClassRejected, "", err)
	}
	tok := b.signCmd(1, mandateID, "reboot", b.OpsKey) // "reboot" is not in the mandate's classes
	_, code, err := b.call(ctx, b.StationURL, b.Ops, "relay_command", map[string]any{"booking_id": bookingID, "command": tok})
	return verdict("class_escalation", errs.ClassRejected, code, err)
}

// attacks returns every attack by name. Command and relay attacks are present
// always; Run skips them when no spacecraft URL is configured.
func (b *Battery) attacks() map[string]func(context.Context) Result {
	return map[string]func(context.Context) Result{
		"replay_booking": b.replayBooking, "underpay_booking": b.underpayBooking, "tamper_mandate": b.tamperMandate,
		"underpay_valid_sig": b.underpayValidSig, "quote_swap_attack": b.quoteSwap, "wrong_audience_attack": b.wrongAudience,
		"wrong_scope_attack": b.wrongScope, "wrong_dpop_key_attack": b.wrongDPoPKey, "corrupt_jws_attack": b.corruptJWS,
		"superseded_format_attack": b.supersededFormat, "unknown_key_mandate": b.unknownKey, "replay_settled": b.replaySettled,
		"canonicalization_probe": b.canonicalizationProbe, "payto_binding_check": b.payToBinding, "card_drift_watch": b.cardDrift,
		"not_owner": b.notOwner, "overlap": b.overlap, "typ_confusion": b.typConfusion, "jku_injection": b.jkuInjection,
		"forged_command": b.forgedCommand, "replayed_command": b.replayedCommand, "late_command": b.lateCommand,
		"class_escalation": b.classEscalation,
	}
}

// runOrder is the fixed order Run reports in.
var runOrder = []string{
	"replay_booking", "underpay_booking", "tamper_mandate", "underpay_valid_sig", "quote_swap_attack",
	"wrong_audience_attack", "wrong_scope_attack", "wrong_dpop_key_attack", "corrupt_jws_attack",
	"superseded_format_attack", "unknown_key_mandate", "replay_settled", "canonicalization_probe",
	"payto_binding_check", "card_drift_watch", "not_owner", "overlap", "typ_confusion", "jku_injection",
	"forged_command", "replayed_command", "late_command", "class_escalation",
}

// Names lists every attack, in run order.
func Names() []string { return append([]string(nil), runOrder...) }

// Attack runs one named attack.
func (b *Battery) Attack(ctx context.Context, name string) (Result, bool) {
	fn, ok := b.attacks()[name]
	if !ok {
		return Result{}, false
	}
	r := fn(ctx)
	b.emit(r)
	return r, true
}

// Run runs every attack the target supports, in order. Command and relay
// attacks run only when a spacecraft URL is configured.
func (b *Battery) Run(ctx context.Context) []Result {
	fns := b.attacks()
	out := make([]Result, 0, len(runOrder))
	for _, name := range runOrder {
		if b.SpaceURL == "" && isCommandAttack(name) {
			continue
		}
		r := fns[name](ctx)
		b.emit(r)
		out = append(out, r)
	}
	return out
}

func isCommandAttack(name string) bool {
	switch name {
	case "forged_command", "replayed_command", "late_command", "class_escalation":
		return true
	}
	return false
}

// AllBlocked reports whether every result is BLOCKED: the deploy gate. An
// empty set is not "all blocked" — a battery that ran nothing must not pass.
func AllBlocked(rs []Result) bool {
	if len(rs) == 0 {
		return false
	}
	for _, r := range rs {
		if r.Verdict != Blocked {
			return false
		}
	}
	return true
}

// Canary runs the auditor's two active probes against the target station: a
// mandate with a flipped signature byte, and a valid mandate presented with
// the wrong DPoP key. A correct station BLOCKs both; a VULNERABLE verdict is
// a CANARY_ACCEPTED (master plan 9.10, 10 rogue).
func (b *Battery) Canary(ctx context.Context) []Result {
	out := []Result{b.corruptJWS(ctx), b.wrongDPoPKey(ctx)}
	for _, r := range out {
		b.emit(r)
	}
	return out
}
