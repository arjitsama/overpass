// Package supplieradapter is a separate conformance surface mounted on a
// station at /mcp/: an MCP (2025-03-26, streamable HTTP) server exposing the
// supplier-shaped tools get_quote and book_flight that GoDaddy's fraud battery
// (fraud.webmesh.ai) attacks. It shares nothing with the Overpass A2A flow.
//
// Every rejection is a real check with a named code (internal/errs), returned
// as an MCP tool error {code, detail}; the verifier recovers panics and never
// answers 5xx. Trust anchor: the Spending Authority's published keys, fetched
// from its trust card / JWKS and pinned by host; a key carried in the request
// is never used to verify a mandate.
package supplieradapter

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arjitsama/overpass/internal/errs"
)

const (
	ProtocolVersion = "2025-03-26"
	maxBody         = 256 << 10
	quoteTTL        = 30 * time.Minute
	toolQuote       = "get_quote"
	toolBook        = "book_flight"
)

// Config is what the adapter needs from the station.
type Config struct {
	Host, ANSName, PublicURL string
	PayTo, Network, Asset    string // x402 accepts block (Network is the CAIP-2 id, Asset the contract)
	AuthorityHost, Authority string // key source host and, if given, expected issuer ANS name
	Log                      *slog.Logger
	Now                      func() time.Time
	Keys                     KeySource // nil -> HTTP fetch from AuthorityHost
}

// Adapter is the MCP server. Safe for concurrent use.
type Adapter struct {
	cfg      Config
	keys     KeySource
	mu       sync.Mutex
	quotes   map[string]*Quote
	usedJTI  map[string]time.Time // DPoP jti -> exp
	used     map[string]time.Time // mandate id -> exp
	sessions map[string]struct{}
}

// New builds the adapter; Log and Now default sensibly.
func New(c Config) *Adapter {
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	a := &Adapter{cfg: c, quotes: map[string]*Quote{}, usedJTI: map[string]time.Time{}, used: map[string]time.Time{}, sessions: map[string]struct{}{}}
	a.keys = c.Keys
	if a.keys == nil {
		a.keys = NewHTTPKeySource(c.AuthorityHost, c.Log)
	}
	return a
}

// --- MCP transport ---------------------------------------------------------

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (a *Adapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if p := recover(); p != nil {
			a.cfg.Log.Error("supplieradapter: recovered", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": nil,
				"error": rpcErr{Code: -32603, Message: "internal error (recovered)"}})
		}
	}()
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		errs.Write(w, http.StatusMethodNotAllowed, errs.MethodNotAllowed, "MCP endpoint: POST JSON-RPC only")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		errs.Write(w, http.StatusBadRequest, errs.BadRequest, "read body: "+err.Error())
		return
	}
	// Full request log for this surface: method, args and every header.
	hdr := map[string]string{}
	for k, v := range r.Header {
		hdr[k] = strings.Join(v, ", ")
	}
	var req rpcReq
	if err := json.Unmarshal(body, &req); err != nil {
		a.cfg.Log.Info("mcp_request", "path", r.URL.Path, "headers", hdr, "body", string(body), "error", "bad json")
		writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcErr{Code: -32700, Message: "parse error"}})
		return
	}
	a.cfg.Log.Info("mcp_request", "path", r.URL.Path, "method", req.Method, "headers", hdr, "params", string(req.Params))
	switch req.Method {
	case "initialize":
		sid := randomID("s-")
		a.mu.Lock()
		a.sessions[sid] = struct{}{}
		a.mu.Unlock()
		w.Header().Set("Mcp-Session-Id", sid)
		a.result(w, req.ID, map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "overpass-supplier-adapter", "version": "0.1.0"},
		})
	case "notifications/initialized", "notifications/cancelled":
		w.WriteHeader(http.StatusAccepted)
	case "ping":
		a.result(w, req.ID, map[string]any{})
	case "tools/list":
		a.result(w, req.ID, map[string]any{"tools": toolList()})
	case "tools/call":
		var p struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			a.rpcError(w, req.ID, -32602, "invalid params")
			return
		}
		out, isErr := a.call(r, p.Name, p.Args)
		a.cfg.Log.Info("mcp_result", "tool", p.Name, "isError", isErr, "result", truncate(mustJSON(out), 600))
		// Rejections use the envelope captured from supplier.webmesh.ai
		// (FastMCP tool error): text "Error executing tool <tool>: <CODE>: <detail>",
		// isError true, no structuredContent. The x402 challenge (get_quote
		// unpaid) keeps the supplier's structured shape.
		if rj, ok := out.(map[string]any); ok && isErr {
			if code, has := rj["code"].(string); has && rj["x402Version"] == nil {
				family, sub := splitCode(code)
				detail, _ := rj["detail"].(string)
				if sub != "" {
					detail = sub + ": " + detail
				}
				a.result(w, req.ID, map[string]any{
					"content": []map[string]any{{"type": "text", "text": "Error executing tool " + p.Name + ": " + family + ": " + detail}},
					"isError": true,
				})
				return
			}
		}
		text, _ := json.Marshal(out)
		a.result(w, req.ID, map[string]any{
			"content":           []map[string]any{{"type": "text", "text": string(text)}},
			"structuredContent": out,
			"isError":           isErr,
		})
	default:
		a.rpcError(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

func (a *Adapter) result(w http.ResponseWriter, id json.RawMessage, res any) {
	if id == nil {
		id = json.RawMessage("null")
	}
	writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "result": res})
}

func (a *Adapter) rpcError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	if id == nil {
		id = json.RawMessage("null")
	}
	writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "error": rpcErr{Code: code, Message: msg}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store, no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// toolList mirrors the supplier's tools/list (names, argument names, types).
func toolList() []map[string]any {
	return []map[string]any{
		{"name": toolQuote, "description": "Three deterministic flight options for a route and date. x402-gated (challenge only; no settlement is performed by this station).",
			"inputSchema": map[string]any{"type": "object", "title": "get_quoteArguments",
				"properties": map[string]any{
					"origin":      map[string]any{"title": "Origin", "type": "string"},
					"destination": map[string]any{"title": "Destination", "type": "string"},
					"date":        map[string]any{"title": "Date", "type": "string"},
					"party_size":  map[string]any{"title": "Party Size", "type": "integer", "default": 1}},
				"required": []string{"origin", "destination", "date"}}},
		{"name": toolBook, "description": "Book a quoted flight. AP2 mandate + DPoP proof required; verified against the Spending Authority's published key. This station does not settle on-chain: a fully valid booking returns PAYMENT_REQUIRED.",
			"inputSchema": map[string]any{"type": "object", "title": "book_flightArguments",
				"properties": map[string]any{
					"quote_id":     map[string]any{"title": "Quote Id", "type": "string"},
					"option_id":    map[string]any{"title": "Option Id", "type": "string"},
					"mandate":      map[string]any{"title": "Mandate", "type": "string"},
					"dpop_proof":   map[string]any{"title": "Dpop Proof", "type": "string"},
					"payment_auth": map[string]any{"title": "Payment Auth", "type": "string", "default": ""}},
				"required": []string{"quote_id", "option_id", "mandate", "dpop_proof"}}},
	}
}

// call dispatches a tool. The returned value is the tool's structured content;
// isError marks rejections (always with {code, detail}).
func (a *Adapter) call(r *http.Request, name string, args json.RawMessage) (out any, isError bool) {
	defer func() {
		if p := recover(); p != nil {
			a.cfg.Log.Error("supplieradapter: tool recovered", "tool", name, "panic", fmt.Sprint(p))
			out, isError = reject(errs.BadRequest, "malformed request (recovered)"), true
		}
	}()
	switch name {
	case toolQuote:
		return a.getQuote(r, args)
	case toolBook:
		return a.bookFlight(r, args)
	default:
		return reject(errs.NotFound, "unknown tool "+name), true
	}
}

// splitCode separates our family code from its sub-reason:
// "MANDATE_REJECTED:signature" -> ("MANDATE_REJECTED", "signature"). The
// family is the string their grader matches; the sub-reason stays in detail.
func splitCode(code string) (string, string) {
	if i := strings.Index(code, ":"); i > 0 {
		return code[:i], code[i+1:]
	}
	return code, ""
}

func reject(code errs.Code, detail string) map[string]any {
	return map[string]any{"code": string(code), "detail": detail}
}

// --- get_quote --------------------------------------------------------------

// Option is one flight option; prices are decimal strings in USD so the
// amount comparison in book_flight is numeric, not textual.
type Option struct {
	OptionID string `json:"option_id"`
	Carrier  string `json:"carrier"`
	Depart   string `json:"depart"`
	Arrive   string `json:"arrive"`
	Stops    int    `json:"stops"`
	Price    string `json:"price"`
	Currency string `json:"currency"`
}

// Quote is stored server-side by quote_id until Exp.
type Quote struct {
	QuoteID     string   `json:"quote_id"`
	MerchantANS string   `json:"merchant_ans"`
	Origin      string   `json:"origin"`
	Destination string   `json:"destination"`
	Date        string   `json:"date"`
	PartySize   int      `json:"party_size"`
	Scope       string   `json:"scope"`
	Options     []Option `json:"options"`
	Exp         int64    `json:"exp"`
	Note        string   `json:"note,omitempty"`
}

func (a *Adapter) x402Challenge(desc string) map[string]any {
	return map[string]any{
		"x402Version": 2,
		"accepts": []map[string]any{{
			"scheme": "exact", "network": a.cfg.Network, "asset": a.cfg.Asset,
			"amount": "10000", "payTo": a.cfg.PayTo, "maxTimeoutSeconds": 300,
			"extra": map[string]any{"name": "USDC", "version": "2"}}},
		"error":    "Payment Required",
		"resource": map[string]any{"url": "mcp://tool/get_quote", "description": desc},
	}
}

func (a *Adapter) getQuote(r *http.Request, raw json.RawMessage) (any, bool) {
	var in struct {
		Origin      string `json:"origin"`
		Destination string `json:"destination"`
		Date        string `json:"date"`
		PartySize   int    `json:"party_size"`
		Payment     string `json:"payment"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return reject(errs.BadRequest, "get_quote: bad arguments"), true
	}
	in.Origin, in.Destination = strings.ToUpper(strings.TrimSpace(in.Origin)), strings.ToUpper(strings.TrimSpace(in.Destination))
	if !iata(in.Origin) || !iata(in.Destination) || !isoDate(in.Date) {
		return reject(errs.BadRequest, "get_quote: origin/destination must be IATA codes and date YYYY-MM-DD"), true
	}
	if in.PartySize <= 0 {
		in.PartySize = 1
	}
	desc := fmt.Sprintf("Flight quote from %s to %s (x402-gated)", in.Origin, in.Destination)
	// x402 gate, mirroring the supplier: no payment -> the challenge. A payment
	// header/argument unlocks the quote; it is recorded but NEVER settled
	// (Honest limits: no facilitator, payTo is the null address).
	pay := firstNonEmpty(r.Header.Get("X-Payment"), r.Header.Get("Payment-Signature"), r.Header.Get("X-PAYMENT"), in.Payment)
	if pay == "" {
		return a.x402Challenge(desc), true
	}
	q := a.makeQuote(in.Origin, in.Destination, in.Date, in.PartySize)
	q.Note = "x402 payment header received; not verified or settled by this station (Honest limits)"
	a.mu.Lock()
	a.gcLocked()
	a.quotes[q.QuoteID] = q
	a.mu.Unlock()
	return q, false
}

// makeQuote is deterministic for (origin, destination, date): the same three
// options every time, so a captured quote can be replayed against a fresh one.
func (a *Adapter) makeQuote(o, d, date string, party int) *Quote {
	seed := sha256.Sum256([]byte(o + "-" + d + ":" + date))
	base := 400 + int(seed[0])%400 // 400..799 USD
	q := &Quote{
		QuoteID: "q-" + hex.EncodeToString(seed[:8]), MerchantANS: a.cfg.ANSName,
		Origin: o, Destination: d, Date: date, PartySize: party,
		Scope: fmt.Sprintf("purchase:flight:%s-%s:%s", o, d, date),
		Exp:   a.cfg.Now().Add(quoteTTL).Unix(),
	}
	for i, m := range []struct {
		carrier string
		stops   int
		mult    float64
	}{{"OP", 0, 1.0}, {"OP", 1, 0.82}, {"BB", 0, 1.35}} {
		price := float64(base*party) * m.mult
		q.Options = append(q.Options, Option{
			OptionID: fmt.Sprintf("opt-%s-%d", hex.EncodeToString(seed[8:12]), i+1),
			Carrier:  m.carrier, Depart: date + "T08:00:00Z", Arrive: date + "T20:30:00Z",
			Stops: m.stops, Price: fmt.Sprintf("%.2f", price), Currency: "USD",
		})
	}
	return q
}

// Quote returns a stored quote (test hook and book_flight lookup).
func (a *Adapter) Quote(id string) (*Quote, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	q, ok := a.quotes[id]
	if !ok || q.Exp < a.cfg.Now().Unix() {
		return nil, false
	}
	return q, true
}

func (a *Adapter) gcLocked() {
	now := a.cfg.Now().Unix()
	for id, q := range a.quotes {
		if q.Exp < now {
			delete(a.quotes, id)
		}
	}
	for k, exp := range a.usedJTI {
		if exp.Unix() < now {
			delete(a.usedJTI, k)
		}
	}
}

func iata(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, c := range s {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

func isoDate(s string) bool { _, err := time.Parse("2006-01-02", s); return err == nil }

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func randomID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// sortedKeys is used by the canonicalizer.
func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

var _ = context.Background
