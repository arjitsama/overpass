// Package a2a is a minimal A2A JSON-RPC 2.0 server at the agent URL.
//
// It speaks A2A 1.0 (`SendMessage`, role `ROLE_*`, member-discriminated
// parts) and 0.3 (`message/send`, role `user`/`agent`, `kind` parts), and
// replies in the dialect of the request. A data part {"skill": id, ...args}
// calls that skill; a message with no skill gets a text reply listing skills.
// Shapes follow the A2A specification (a2aproject/A2A, docs/specification.md
// section 9 and docs/whats-new-v1.md).
package a2a

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/arjitsama/overpass/internal/errs"
)

// Method names.
const (
	MethodSendV1  = "SendMessage"
	MethodSendV03 = "message/send"
)

// JSON-RPC and A2A error codes (A2A spec sections 5.4 and 9.5).
const (
	CodeParse       = -32700
	CodeInvalidReq  = -32600
	CodeNoMethod    = -32601
	CodeParams      = -32602
	CodeInternal    = -32603
	CodeUnsupported = -32004
)

// MaxBodyBytes caps a JSON-RPC request.
const MaxBodyBytes = 1 << 20

// Handler runs one skill. args is the whole data part object. It returns a
// JSON-encodable result, or an *errs.Error to reject.
type Handler func(ctx context.Context, args json.RawMessage) (any, error)

// SkillInfo is what the text reply says about a skill.
type SkillInfo struct {
	ID, Name, Description string
}

// Server dispatches A2A messages to skills.
type Server struct {
	name     string
	skills   []SkillInfo
	handlers map[string]Handler
	sec      Security
	log      *slog.Logger
}

// NewServer returns a server for the given skills. Skills without a handler
// answer with UnsupportedOperation until a later phase provides one.
func NewServer(name string, skills []SkillInfo, handlers map[string]Handler, sec Security, log *slog.Logger) *Server {
	return &Server{name: name, skills: skills, handlers: handlers, sec: sec, log: log}
}

// Handler is the HTTP handler for POST /, wrapped in the HTTP guards.
func (s *Server) Handler() http.Handler {
	var h http.Handler = http.HandlerFunc(s.serve)
	for i := len(s.sec.HTTP) - 1; i >= 0; i-- {
		h = s.sec.HTTP[i].Wrap(h)
	}
	return h
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    []any  `json:"data,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

var nullID = json.RawMessage("null")

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		errs.Write(w, http.StatusMethodNotAllowed, errs.MethodNotAllowed, "A2A JSON-RPC takes POST")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		writeRPC(w, response{ID: nullID, Error: &rpcError{Code: CodeInvalidReq, Message: "Request payload validation error: body too large or unreadable"}})
		return
	}
	resp, notify := s.handle(r.Context(), body)
	if notify {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeRPC(w, resp)
}

// handle turns one request body into a response. It never panics.
func (s *Server) handle(ctx context.Context, body []byte) (resp response, notify bool) {
	resp.ID = nullID
	defer func() {
		if v := recover(); v != nil {
			s.log.Error("a2a handler panic", "panic", v)
			resp, notify = response{ID: resp.ID, Error: &rpcError{Code: CodeInternal, Message: "Internal error"}}, false
		}
	}()
	if t := bytes.TrimLeft(body, " \t\r\n"); len(t) > 0 && t[0] == '[' {
		return response{ID: nullID, Error: &rpcError{Code: CodeInvalidReq, Message: "Batch requests are not supported"}}, false
	}
	if !json.Valid(body) {
		return response{ID: nullID, Error: &rpcError{Code: CodeParse, Message: "Invalid JSON payload"}}, false
	}
	var req request
	if t := bytes.TrimLeft(body, " \t\r\n"); len(t) == 0 || t[0] != '{' || json.Unmarshal(body, &req) != nil ||
		req.JSONRPC != "2.0" || req.Method == "" {
		invalid := response{ID: nullID, Error: &rpcError{Code: CodeInvalidReq, Message: "Request payload validation error"}}
		if req.ID != nil && validID(req.ID) {
			invalid.ID = req.ID
		}
		return invalid, false
	}
	if req.ID == nil {
		return response{}, true // valid JSON-RPC notification: no response
	}
	if !validID(req.ID) {
		return response{ID: nullID, Error: &rpcError{Code: CodeInvalidReq, Message: "Request payload validation error: bad id"}}, false
	}
	resp.ID = req.ID
	v03 := req.Method == MethodSendV03
	if req.Method != MethodSendV1 && !v03 {
		resp.Error = &rpcError{Code: CodeNoMethod, Message: "Method not found: " + truncate(req.Method, 64)}
		return resp, false
	}
	result, rerr := s.send(ctx, req.Params, v03)
	resp.Result, resp.Error = result, rerr
	return resp, false
}

func validID(id json.RawMessage) bool {
	var v any
	if json.Unmarshal(id, &v) != nil {
		return false
	}
	switch x := v.(type) {
	case string, nil:
		return true
	case float64:
		return x == float64(int64(x)) // JSON-RPC: numbers SHOULD NOT have fractions
	}
	return false
}

// part covers both dialects: 0.3 sets kind, 1.0 uses which member is present.
type part struct {
	Kind string          `json:"kind"`
	Text *string         `json:"text"`
	Data json.RawMessage `json:"data"`
	Raw  json.RawMessage `json:"raw"`
	URL  json.RawMessage `json:"url"`
	File json.RawMessage `json:"file"`
}

// dataObject returns the part's data object if the part is a data part in
// the request's dialect: 0.3 needs kind "data"; 1.0 needs data as the only
// content member.
func (p part) dataObject(v03 bool) (json.RawMessage, bool) {
	if len(p.Data) == 0 || p.Data[0] != '{' {
		return nil, false
	}
	if v03 {
		return p.Data, p.Kind == "data"
	}
	return p.Data, p.Kind == "" && p.Text == nil && p.Raw == nil && p.URL == nil && p.File == nil
}

type sendParams struct {
	Message *struct {
		MessageID string `json:"messageId"`
		Role      string `json:"role"`
		Parts     []part `json:"parts"`
	} `json:"message"`
}

func (s *Server) send(ctx context.Context, raw json.RawMessage, v03 bool) (any, *rpcError) {
	var p sendParams
	if err := json.Unmarshal(raw, &p); err != nil || p.Message == nil || len(p.Message.Parts) == 0 {
		return nil, &rpcError{Code: CodeParams, Message: "Invalid parameters: message with at least one part is required"}
	}
	for _, pt := range p.Message.Parts {
		data, ok := pt.dataObject(v03)
		if !ok {
			continue
		}
		var sel struct {
			Skill string `json:"skill"`
		}
		if json.Unmarshal(data, &sel) == nil && sel.Skill != "" {
			return s.call(ctx, sel.Skill, data, v03)
		}
	}
	return reply(v03, textPart(v03, s.skillList())), nil
}

func (s *Server) call(ctx context.Context, skill string, args json.RawMessage, v03 bool) (any, *rpcError) {
	if !s.declared(skill) {
		return nil, &rpcError{Code: CodeParams, Message: "Invalid parameters: unknown skill " + truncate(skill, 64)}
	}
	for _, g := range s.sec.Skill[skill] {
		if err := g.Check(ctx, args); err != nil {
			return nil, rejection(err)
		}
	}
	h, ok := s.handlers[skill]
	if !ok {
		return nil, &rpcError{Code: CodeUnsupported, Message: "Unsupported operation: skill " + skill + " is not available yet"}
	}
	out, err := h(ctx, args)
	if err != nil {
		var e *errs.Error
		if !errors.As(err, &e) {
			s.log.Error("skill failed", "skill", skill, "err", err)
		}
		return nil, rejection(err)
	}
	return reply(v03, dataPart(v03, out)), nil
}

func (s *Server) declared(id string) bool {
	for _, sk := range s.skills {
		if sk.ID == id {
			return true
		}
	}
	return false
}

// rejection maps a named error to Invalid parameters with a google.rpc.ErrorInfo
// detail carrying the code (A2A spec 9.5). Anything unnamed is Internal, with
// no detail, so internals never leak.
func rejection(err error) *rpcError {
	var e *errs.Error
	if !errors.As(err, &e) {
		return &rpcError{Code: CodeInternal, Message: "Internal error"}
	}
	return &rpcError{Code: CodeParams, Message: string(e.Code), Data: []any{map[string]any{
		"@type":    "type.googleapis.com/google.rpc.ErrorInfo",
		"reason":   string(e.Code),
		"domain":   "overpass",
		"metadata": map[string]string{"detail": e.Detail},
	}}}
}

func (s *Server) skillList() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s. Skills (send a data part {\"skill\": \"<id>\", ...}):", s.name)
	for _, sk := range s.skills {
		fmt.Fprintf(&b, "\n- %s: %s. %s", sk.ID, sk.Name, sk.Description)
	}
	if len(s.skills) == 0 {
		b.WriteString("\n(none)")
	}
	return b.String()
}

func textPart(v03 bool, text string) map[string]any {
	if v03 {
		return map[string]any{"kind": "text", "text": text}
	}
	return map[string]any{"text": text, "mediaType": "text/plain"}
}

func dataPart(v03 bool, data any) map[string]any {
	if v03 {
		return map[string]any{"kind": "data", "data": data}
	}
	return map[string]any{"data": data, "mediaType": "application/json"}
}

// reply wraps one part as the agent's message: a bare Message in 0.3, a
// SendMessageResponse {"message": ...} in 1.0.
func reply(v03 bool, p map[string]any) any {
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	if v03 {
		return map[string]any{"kind": "message", "messageId": hex.EncodeToString(id), "role": "agent", "parts": []any{p}}
	}
	return map[string]any{"message": map[string]any{"messageId": hex.EncodeToString(id), "role": "ROLE_AGENT", "parts": []any{p}}}
}

func writeRPC(w http.ResponseWriter, resp response) {
	resp.JSONRPC = "2.0"
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = json.NewEncoder(w).Encode(resp)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
