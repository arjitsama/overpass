// Package opsflow drives one pass end to end from Mission Ops across separate
// agent processes over A2A: verify the station, get a quote, ask the authority
// for a mandate, book the pass, and relay one command to the spacecraft (via the
// station). Every call is DPoP-signed and the station verifies Ops inbound, so
// this is the real cross-process path (master plan §8-§9), not an in-process
// shortcut. The greedy/LLM planners choose which station; this runs the pass.
package opsflow

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/arjitsama/overpass/internal/a2a"
	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/schema"
	"github.com/arjitsama/overpass/internal/verify"
)

// PeerVerifier is verify.Verifier's VerifyPeer.
type PeerVerifier interface {
	VerifyPeer(ctx context.Context, host string) verify.Result
}

// Signer authenticates an outbound request (verify.Outbound.Attach).
type Signer interface{ Attach(*http.Request) error }

// Flow holds what one pass needs.
type Flow struct {
	Peers      PeerVerifier
	Sign       Signer       // Ops DPoP signer
	HTTP       *http.Client // TLS + any local dial map
	OpsANS     string
	CommandKey *ecdsa.PrivateKey  // signs commands; the spacecraft holds its public half
	AuthKeys   []*ecdsa.PublicKey // to read the mandate id back
	Now        func() time.Time
	Emit       func(bus.Event)
}

// Target names the station and authority for one pass.
type Target struct {
	StationHost  string // e.g. gs-blacksburg.example
	StationURL   string // https URL (POST /)
	AuthorityURL string
	NoradID      int64
	Mode         string // schema.ModeUplink / ModeDownlink
	AOS, LOS     int64
	MaxElevDeg   int64
	Class        string // command class, e.g. "telemetry"
}

// Result is what one pass produced.
type Result struct {
	StationANS string
	QuoteID    string
	MandateID  string
	BookingID  string
	Ack        schema.Ack
}

func (f *Flow) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *Flow) emit(kind, result, reason string) {
	if f.Emit != nil {
		f.Emit(bus.Event{Agent: f.OpsANS, Kind: kind, Subject: "pass", Result: result, Reason: reason})
	}
}

func (f *Flow) client(url string) *a2a.Client {
	return &a2a.Client{URL: url, HTTP: f.HTTP, Sign: f.Sign.Attach}
}

// Run drives the whole pass. It verifies the station FIRST and returns before any
// quote is requested if verification fails (identity is the gate).
func (f *Flow) Run(ctx context.Context, t Target) (Result, error) {
	var res Result

	// 1. Verify the station. No quote is read unless it passes.
	vr := f.Peers.VerifyPeer(ctx, t.StationHost)
	if !vr.OK() {
		reason := strings.Join(vr.Failed(), "; ")
		if reason == "" {
			reason = vr.Verdict
		}
		f.emit("pass_verify", "refused", reason)
		return res, errs.New(errs.CardRejectedSignature, "station "+t.StationHost+" did not verify: "+reason)
	}
	res.StationANS = vr.ANSName
	f.emit("pass_verify", "ok", res.StationANS)

	// 2. Quote (A2A to the station).
	var quote schema.Quote
	if err := f.client(t.StationURL).Call(ctx, "get_pass_quote", map[string]any{
		"norad_id": t.NoradID, "aos": t.AOS, "los": t.LOS, "mode": t.Mode, "max_elevation_deg": t.MaxElevDeg,
	}, &quote); err != nil {
		return res, fmt.Errorf("get_pass_quote: %w", err)
	}
	res.QuoteID = quote.QuoteID
	f.emit("pass_quote", "ok", quote.QuoteID)

	// 3. Mandate (A2A to the authority) — the authority, not Ops, decides.
	var mres struct {
		Mandate string `json:"mandate"`
	}
	if err := f.client(t.AuthorityURL).Call(ctx, "issue_mandate", map[string]any{"quote": quote}, &mres); err != nil {
		f.emit("pass_mandate", "refused", err.Error())
		return res, fmt.Errorf("issue_mandate: %w", err)
	}
	m, err := schema.VerifyMandate(mres.Mandate, f.AuthKeys)
	if err != nil {
		return res, fmt.Errorf("mandate from authority did not verify: %w", err)
	}
	res.MandateID = m.MandateID
	f.emit("pass_mandate", "ok", m.MandateID)

	// 4. Book (A2A to the station).
	var bres struct {
		BookingID string `json:"booking_id"`
	}
	if err := f.client(t.StationURL).Call(ctx, "book_pass", map[string]any{
		"quote_id": quote.QuoteID, "mandate": mres.Mandate,
	}, &bres); err != nil {
		return res, fmt.Errorf("book_pass: %w", err)
	}
	res.BookingID = bres.BookingID
	f.emit("pass_book", "ok", bres.BookingID)

	// 5. Relay one signed command (the station relays it, untouched, to the spacecraft).
	class := t.Class
	if class == "" {
		class = "telemetry"
	}
	cmd := schema.Command{NoradID: t.NoradID, Counter: f.now().UnixMilli(), MandateID: m.MandateID,
		Class: class, Body: json.RawMessage(`{"op":"dump"}`), IssuedAt: f.now().Unix()}
	tok, err := schema.SignCommand(cmd, f.CommandKey)
	if err != nil {
		return res, err
	}
	var ack schema.Ack
	if err := f.client(t.StationURL).Call(ctx, "relay_command", map[string]any{
		"booking_id": bres.BookingID, "command": tok,
	}, &ack); err != nil {
		return res, fmt.Errorf("relay_command: %w", err)
	}
	res.Ack = ack
	f.emit("pass_relay", "ok", ack.Result)
	return res, nil
}
