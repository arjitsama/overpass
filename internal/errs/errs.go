// Package errs defines the named rejection codes every Overpass endpoint
// returns, and the JSON body that carries them.
package errs

import (
	"encoding/json"
	"errors"
	"net/http"
)

// Code is a stable, machine-readable rejection reason.
type Code string

// Rejection codes. Later phases add their own; names never change once shipped.
const (
	BadRequest       Code = "bad_request"
	NotFound         Code = "not_found"
	MethodNotAllowed Code = "method_not_allowed"
	PayloadTooLarge  Code = "payload_too_large"
	Unavailable      Code = "unavailable"
	Internal         Code = "internal"
)

// Protocol rejection codes from master plan sections 8, 9 and 10. The text
// after ":" names which check failed.
const (
	// Any JWS whose typ is not the one the verifier expects (checked first).
	TypRejected Code = "TYP_REJECTED"

	// Inbound caller authentication (DPoP + SCITT receipt + status token) failed.
	CallerRejected Code = "CALLER_REJECTED"

	// book_pass checks, in order (section 8.5).
	MandateParseError        Code = "MANDATE_PARSE_ERROR"
	MandateRejectedSignature Code = "MANDATE_REJECTED:signature"
	MandateRejectedNotOwner  Code = "MANDATE_REJECTED:not_owner"
	MandateRejectedAudience  Code = "MANDATE_REJECTED:audience"
	MandateRejectedQuote     Code = "MANDATE_REJECTED:quote"
	MandateRejectedScope     Code = "MANDATE_REJECTED:scope"
	MandateRejectedAmount    Code = "MANDATE_REJECTED:amount"
	MandateRejectedWindow    Code = "MANDATE_REJECTED:window"
	DPoPRejectedKey          Code = "DPOP_REJECTED:key"
	DPoPRejectedReplay       Code = "DPOP_REJECTED:replay"
	BookingRejectedOverlap   Code = "BOOKING_REJECTED:overlap"
	MandateRejectedConsumed  Code = "MANDATE_REJECTED:consumed"

	// Pass session and audit (section 9).
	WindowClosed   Code = "WINDOW_CLOSED"
	ClassRejected  Code = "CLASS_REJECTED"
	CanaryAccepted Code = "CANARY_ACCEPTED"
	ChainMismatch  Code = "CHAIN_MISMATCH"
	AckMissing     Code = "ACK_MISSING"

	// Trust and cards (section 10).
	PolicyRefusedTier Code = "POLICY_REFUSED:tier"

	// Authority flight-rule refusals (issue_mandate).
	PolicyRefusedCaller     Code = "POLICY_REFUSED:caller"
	PolicyRefusedUnverified Code = "POLICY_REFUSED:unverified"
	PolicyRefusedStation    Code = "POLICY_REFUSED:station"
	PolicyRefusedClasses    Code = "POLICY_REFUSED:classes"
	PolicyRefusedAmount     Code = "POLICY_REFUSED:amount"
	PolicyRefusedDailyLimit Code = "POLICY_REFUSED:daily_limit"
	PolicyRefusedWindow     Code = "POLICY_REFUSED:window"

	// Station quote refusals (get_pass_quote).
	QuoteRejectedWindow Code = "QUOTE_REJECTED:window"
	QuoteRejectedNorad  Code = "QUOTE_REJECTED:norad_id"

	// Planner reasons for passes it did not select.
	PlanUnverified    Code = "PLAN_SKIPPED:unverified"
	PlanNoQuote       Code = "PLAN_SKIPPED:no_quote"
	PlanBadPrice      Code = "PLAN_SKIPPED:price"
	PlanOverlap       Code = "PLAN_SKIPPED:overlap"
	PlanGoalMet       Code = "PLAN_SKIPPED:goal_met"
	PlanExcluded      Code = "PLAN_SKIPPED:replan"
	PlanUnknownHost   Code = "PLAN_SKIPPED:unknown_station"
	CardRejectedJKU   Code = "CARD_REJECTED:jku"
	CardClaimMismatch Code = "CARD_CLAIM_MISMATCH"
)

// Per-object parse and signature codes for the other signed and wire objects.
// The spacecraft's own checks (section 9.1) use the COMMAND_REJECTED family.
const (
	CommandParseError        Code = "COMMAND_PARSE_ERROR"
	CommandRejectedSignature Code = "COMMAND_REJECTED:signature"
	CommandRejectedNorad     Code = "COMMAND_REJECTED:norad_id"
	CommandRejectedCounter   Code = "COMMAND_REJECTED:counter"
	SatRegParseError         Code = "SATREG_PARSE_ERROR"
	SatRegRejectedSignature  Code = "SATREG_REJECTED:signature"
	AuditParseError          Code = "AUDIT_PARSE_ERROR"
	AuditRejectedSignature   Code = "AUDIT_REJECTED:signature"
	ReceiptParseError        Code = "RECEIPT_PARSE_ERROR"
	ReceiptRejectedSignature Code = "RECEIPT_REJECTED:signature"
	CardParseError           Code = "CARD_PARSE_ERROR"
	CardRejectedSignature    Code = "CARD_REJECTED:signature"
	QuoteParseError          Code = "QUOTE_PARSE_ERROR"
	AckParseError            Code = "ACK_PARSE_ERROR"
	RecordParseError         Code = "RECORD_PARSE_ERROR"
)

// As is errors.As, re-exported so callers need one import.
func As(err error, target any) bool { return errors.As(err, target) }

// Is reports whether err is an *Error carrying code.
func Is(err error, code Code) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// Error is a rejection with a named code. It is also the JSON body.
type Error struct {
	Code   Code   `json:"code"`
	Detail string `json:"detail"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Detail }

// New returns an *Error with the given code and detail.
func New(code Code, detail string) *Error {
	return &Error{Code: code, Detail: detail}
}

// Write sends a rejection body {code, detail} with the given HTTP status.
func Write(w http.ResponseWriter, status int, code Code, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Error{Code: code, Detail: detail})
}
