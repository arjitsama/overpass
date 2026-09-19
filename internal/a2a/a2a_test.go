package a2a

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentnameservice/ans-sdk-go/pop"
	"github.com/agentnameservice/ans-sdk-go/verify/scitt"

	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/schema"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func testServer(sec Security, handlers map[string]Handler) http.Handler {
	skills := []SkillInfo{
		{ID: "get_pass_quote", Name: "Pass quote", Description: "Price a pass"},
		{ID: "book_pass", Name: "Book pass", Description: "Book with a mandate"},
	}
	return NewServer("gs-test", skills, handlers, sec, quiet).Handler()
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    []struct {
			Reason string `json:"reason"`
		} `json:"data"`
	} `json:"error"`
}

func post(t *testing.T, h http.Handler, body string) (int, rpcResp) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	var r rpcResp
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
			t.Fatalf("response not JSON: %s", rec.Body.String())
		}
		if r.JSONRPC != "2.0" {
			t.Fatalf("jsonrpc = %q", r.JSONRPC)
		}
	}
	return rec.Code, r
}

const textV1 = `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"what can you do?"}]}}}`
const textV03 = `{"jsonrpc":"2.0","id":"a","method":"message/send","params":{"message":{"kind":"message","messageId":"m1","role":"user","parts":[{"kind":"text","text":"hi"}]}}}`

// Acceptance 5: send-message (1.0 and 0.3) returns the skill list.
func TestSendMessageListsSkills(t *testing.T) {
	h := testServer(Security{}, nil)
	code, r := post(t, h, textV1)
	var v1 struct {
		Message struct {
			Role  string `json:"role"`
			Parts []struct {
				Text string `json:"text"`
				Kind string `json:"kind"`
			} `json:"parts"`
		} `json:"message"`
	}
	if code != 200 || r.Error != nil || json.Unmarshal(r.Result, &v1) != nil {
		t.Fatalf("v1: %d %+v", code, r)
	}
	if v1.Message.Role != "ROLE_AGENT" || v1.Message.Parts[0].Kind != "" ||
		!strings.Contains(v1.Message.Parts[0].Text, "get_pass_quote") || !strings.Contains(v1.Message.Parts[0].Text, "book_pass") {
		t.Fatalf("v1 reply %+v", v1)
	}

	code, r = post(t, h, textV03)
	var v03 struct {
		Kind  string `json:"kind"`
		Role  string `json:"role"`
		Parts []struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		} `json:"parts"`
	}
	if code != 200 || r.Error != nil || json.Unmarshal(r.Result, &v03) != nil || string(r.ID) != `"a"` {
		t.Fatalf("v03: %d %+v", code, r)
	}
	if v03.Kind != "message" || v03.Role != "agent" || v03.Parts[0].Kind != "text" || !strings.Contains(v03.Parts[0].Text, "book_pass") {
		t.Fatalf("v03 reply %+v", v03)
	}
}

// Acceptance 5: unknown method is a JSON-RPC error, not a 500.
func TestJSONRPCErrors(t *testing.T) {
	h := testServer(Security{}, map[string]Handler{
		"get_pass_quote": func(context.Context, json.RawMessage) (any, error) { panic("boom") },
	})
	cases := map[string]struct {
		body string
		code int
	}{
		"unknown method": {`{"jsonrpc":"2.0","id":1,"method":"tasks/get","params":{}}`, CodeNoMethod},
		"parse error":    {`{"jsonrpc":`, CodeParse},
		"batch":          {`[` + textV1 + `]`, CodeInvalidReq},
		"wrong version":  {`{"jsonrpc":"1.0","id":1,"method":"SendMessage"}`, CodeInvalidReq},
		"object id":      {`{"jsonrpc":"2.0","id":{},"method":"SendMessage"}`, CodeInvalidReq},
		"no message":     {`{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{}}`, CodeParams},
		"no parts":       {`{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"parts":[]}}}`, CodeParams},
		"unknown skill":  {skillCall("launch_missiles", ""), CodeParams},
		"no handler yet": {skillCall("book_pass", ""), CodeUnsupported},
		"handler panics": {skillCall("get_pass_quote", ""), CodeInternal},
		"null body":      {`null`, CodeInvalidReq},
		"empty object":   {`{}`, CodeInvalidReq},
		"string body":    {`"x"`, CodeInvalidReq},
		"v1 no id":       {`{"jsonrpc":"1.0","method":"x"}`, CodeInvalidReq},
		"fraction id":    {`{"jsonrpc":"2.0","id":1.5,"method":"SendMessage"}`, CodeInvalidReq},
		"oversize":       {`{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"x":"` + strings.Repeat("a", MaxBodyBytes) + `"}}`, CodeInvalidReq},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			status, r := post(t, h, c.body)
			if status != 200 || r.Error == nil || r.Error.Code != c.code {
				t.Fatalf("status %d resp %+v, want code %d", status, r, c.code)
			}
		})
	}
	// A notification (no id) gets no body.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"jsonrpc":"2.0","method":"SendMessage"}`)))
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("notification: %d %q", rec.Code, rec.Body.String())
	}
	// GET is a named 405, not JSON-RPC.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed || !strings.Contains(rec.Body.String(), `"method_not_allowed"`) {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
	}
}

func skillCall(skill, extra string) string {
	return `{"jsonrpc":"2.0","id":7,"method":"SendMessage","params":{"message":{"messageId":"m","role":"ROLE_USER","parts":[{"data":{"skill":"` + skill + `"` + extra + `}}]}}}`
}

// A data member only dispatches when it is the part's content in that dialect.
func TestPartShapeStrict(t *testing.T) {
	called := false
	h := testServer(Security{}, map[string]Handler{"get_pass_quote": func(context.Context, json.RawMessage) (any, error) {
		called = true
		return map[string]int{}, nil
	}})
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"kind":"message","messageId":"m","role":"user","parts":[{"kind":"text","text":"x","data":{"skill":"get_pass_quote"}}]}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m","role":"ROLE_USER","parts":[{"text":"x","data":{"skill":"get_pass_quote"}}]}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m","role":"ROLE_USER","parts":[{"kind":"data","data":{"skill":"get_pass_quote"}}]}}}`,
	} {
		if _, r := post(t, h, body); r.Error != nil || called {
			t.Fatalf("dispatched from a non-data part: %s", body)
		}
	}
}

func TestSkillDispatchBothDialects(t *testing.T) {
	var got json.RawMessage
	h := testServer(Security{}, map[string]Handler{
		"get_pass_quote": func(_ context.Context, args json.RawMessage) (any, error) {
			got = args
			return map[string]int{"amount_cents": 1200}, nil
		},
	})
	_, r := post(t, h, skillCall("get_pass_quote", `,"norad_id":25544`))
	if r.Error != nil || !strings.Contains(string(r.Result), `"data":{"amount_cents":1200}`) || !strings.Contains(string(got), `25544`) {
		t.Fatalf("v1 result %s args %s", r.Result, got)
	}
	v03 := `{"jsonrpc":"2.0","id":2,"method":"message/send","params":{"message":{"kind":"message","messageId":"m","role":"user","parts":[{"kind":"data","data":{"skill":"get_pass_quote"}}]}}}`
	_, r = post(t, h, v03)
	if r.Error != nil || !strings.Contains(string(r.Result), `"kind":"data"`) {
		t.Fatalf("v03 result %s", r.Result)
	}
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// The mandate guard runs before the handler and rejects with named codes.
func TestMandateGuard(t *testing.T) {
	authority := newKey(t)
	var keys []*ecdsa.PublicKey
	called := false
	sec := Security{Skill: map[string][]SkillGuard{"book_pass": {MandateGuard(func() []*ecdsa.PublicKey { return keys })}}}
	h := testServer(sec, map[string]Handler{"book_pass": func(context.Context, json.RawMessage) (any, error) {
		called = true
		return map[string]string{"ok": "yes"}, nil
	}})
	m := schema.Mandate{MandateID: "m-1", Iss: "authority.x", Sub: "ops.x", Aud: "gs.x", QuoteID: "q-1",
		Scope: schema.Scope("uplink", 25544), CommandClasses: []string{"telemetry"}, MaxAmountCents: 1,
		Nbf: 1760000000, Exp: 1760000480, JKT: strings.Repeat("A", 43), Nonce: "bm9uY2Utbm9uY2Utbm9uY2U"}
	tok, _ := schema.SignMandate(m, authority)

	for name, c := range map[string]struct {
		extra string
		keys  []*ecdsa.PublicKey
		code  string
	}{
		"missing":       {"", nil, string(errs.MandateParseError)},
		"garbage":       {`,"mandate":"x.y.z"`, nil, string(errs.MandateParseError)},
		"no trust keys": {`,"mandate":"` + tok + `"`, nil, string(errs.MandateRejectedSignature)},
		"wrong key":     {`,"mandate":"` + tok + `"`, []*ecdsa.PublicKey{&newKey(t).PublicKey}, string(errs.MandateRejectedSignature)},
	} {
		keys = c.keys
		_, r := post(t, h, skillCall("book_pass", c.extra))
		if r.Error == nil || r.Error.Code != CodeParams || len(r.Error.Data) != 1 || r.Error.Data[0].Reason != c.code {
			t.Fatalf("%s: %+v", name, r.Error)
		}
	}
	if called {
		t.Fatal("handler ran despite guard rejection")
	}
	keys = []*ecdsa.PublicKey{&authority.PublicKey}
	if _, r := post(t, h, skillCall("book_pass", `,"mandate":"`+tok+`"`)); r.Error != nil || !called {
		t.Fatalf("valid mandate: %+v", r.Error)
	}
}

// The DPoP guard is ans-sdk-go's pop.Middleware; its rejection comes back as
// a named code, and the handler never runs.
func TestDPoPGuardRejectsUnauthenticated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	keys, _ := scitt.NewKeyStore(nil)
	g := DPoPGuard(keys, pop.NewMemoryReplayCache(ctx, 100), "gs.example:8444", quiet)
	called := false
	h := g.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	for name, hdr := range map[string]string{"no proof": "", "junk proof": "a.b.c"} {
		req := httptest.NewRequest(http.MethodPost, "https://gs.example:8444/", bytes.NewReader([]byte(textV1)))
		if hdr != "" {
			req.Header.Set("DPoP", hdr)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var body errs.Error
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != http.StatusUnauthorized || body.Code != errs.CallerRejected || rec.Header().Get("WWW-Authenticate") != "DPoP" {
			t.Fatalf("%s: %d %q %v", name, rec.Code, rec.Body.String(), rec.Header())
		}
	}
	if called {
		t.Fatal("handler ran without authentication")
	}
}

func TestSecurityDeclarations(t *testing.T) {
	open := Security{}
	if _, ok := open.Schemes()["noAuth"]; !ok || len(open.Schemes()) != 1 {
		t.Fatalf("open: %v", open.Schemes())
	}
	g := HTTPGuard{Scheme: Scheme{Name: SchemeDPoP}}
	sg := SkillGuard{Scheme: Scheme{Name: SchemeMandate}}
	station := Security{HTTP: []HTTPGuard{g}, Skill: map[string][]SkillGuard{"book_pass": {sg}}}
	if _, ok := station.Schemes()["noAuth"]; ok {
		t.Fatal("station declares noAuth")
	}
	req, _ := json.Marshal(station.SkillRequirements("book_pass"))
	if string(req) != `[{"ansDPoP":[],"overpassMandate":[]}]` {
		t.Fatalf("book_pass requirements %s", req)
	}
	req, _ = json.Marshal(station.SkillRequirements("get_pass_quote"))
	if string(req) != `[{"ansDPoP":[]}]` {
		t.Fatalf("quote requirements %s", req)
	}
}
