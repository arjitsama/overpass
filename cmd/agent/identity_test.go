package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/arjitsama/overpass/internal/a2a"
	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/wellknown"
)

func withSkills(c config.Config) config.Config {
	c.Card.Skills = []config.Skill{
		{ID: "get_pass_quote", Name: "Pass quote", Description: "Price a pass.", Tags: []string{"uplink-uhf"}},
		{ID: "book_pass", Name: "Book pass", Description: "Book with a mandate.", Tags: []string{"uplink-uhf"}},
	}
	return c
}

func get(t *testing.T, r *running, path string) (int, []byte) {
	t.Helper()
	resp, err := r.client.Get(r.url + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func postJSON(t *testing.T, r *running, body string) (int, []byte) {
	t.Helper()
	resp, err := r.client.Post(r.url+"/", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

type servedCard struct {
	SecuritySchemes      map[string]json.RawMessage `json:"securitySchemes"`
	SecurityRequirements []map[string][]string      `json:"securityRequirements"`
	Skills               []struct {
		ID                   string                `json:"id"`
		SecurityRequirements []map[string][]string `json:"securityRequirements"`
	} `json:"skills"`
}

// Acceptance 4: a station card never contains noAuth, and every scheme it
// claims is one the running server has mounted, and is enforced.
func TestStationCardMatchesMounted(t *testing.T) {
	r := start(t, withSkills(testConfig("station")))
	_, raw := get(t, r, wellknown.PathCard)
	if strings.Contains(string(raw), "noAuth") {
		t.Fatal("station card declares noAuth")
	}
	var card servedCard
	if err := json.Unmarshal(raw, &card); err != nil {
		t.Fatal(err)
	}
	mounted := r.agent.sec.Schemes()
	if len(card.SecuritySchemes) != len(mounted) {
		t.Fatalf("card schemes %v, mounted %v", wellknownKeys(card.SecuritySchemes), wellknownKeys(mounted))
	}
	for name := range card.SecuritySchemes {
		if _, ok := mounted[name]; !ok {
			t.Errorf("card claims %s, not mounted", name)
		}
	}
	if len(r.agent.sec.HTTP) != 1 || r.agent.sec.HTTP[0].Scheme.Name != a2a.SchemeDPoP {
		t.Fatalf("station HTTP guards: %+v", r.agent.sec.HTTP)
	}
	for _, sk := range card.Skills {
		want := map[string]bool{a2a.SchemeDPoP: true}
		if sk.ID == "book_pass" {
			want[a2a.SchemeMandate] = true
		}
		got := sk.SecurityRequirements[0]
		if len(got) != len(want) {
			t.Errorf("%s requirements %v", sk.ID, got)
		}
		for n := range want {
			if _, ok := got[n]; !ok {
				t.Errorf("%s missing %s", sk.ID, n)
			}
		}
	}

	// Claimed DPoP is enforced: an unauthenticated A2A call is refused.
	code, body := postJSON(t, r, `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m","role":"ROLE_USER","parts":[{"text":"hi"}]}}}`)
	var rej errs.Error
	_ = json.Unmarshal(body, &rej)
	if code != http.StatusUnauthorized || rej.Code != errs.CallerRejected {
		t.Fatalf("unauthenticated call: %d %s", code, body)
	}
	// Claimed mandate is enforced on book_pass: the same skill guards the
	// server mounted reject a call without one (DPoP bypassed here only).
	inner := a2a.NewServer("x", []a2a.SkillInfo{{ID: "book_pass"}}, nil, a2a.Security{Skill: r.agent.sec.Skill}, quiet).Handler()
	rec := &recorder{header: http.Header{}}
	req, _ := http.NewRequest(http.MethodPost, "/", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m","role":"ROLE_USER","parts":[{"data":{"skill":"book_pass"}}]}}}`))
	inner.ServeHTTP(rec, req)
	if !strings.Contains(rec.body.String(), string(errs.MandateParseError)) {
		t.Fatalf("book_pass without mandate: %s", rec.body.String())
	}
}

func wellknownKeys[V any](m map[string]V) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}

type recorder struct {
	header http.Header
	body   strings.Builder
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) WriteHeader(int)             {}
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }

// Acceptance 5 over the real routes: an open agent answers send-message with
// its skills; an unknown method is a JSON-RPC error with HTTP 200.
func TestSendMessageOverHTTP(t *testing.T) {
	r := start(t, withSkills(testConfig("ops")))
	code, body := postJSON(t, r, `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m","role":"ROLE_USER","parts":[{"text":"what can you do?"}]}}}`)
	if code != 200 || !strings.Contains(string(body), "get_pass_quote") || !strings.Contains(string(body), "ROLE_AGENT") {
		t.Fatalf("SendMessage: %d %s", code, body)
	}
	code, body = postJSON(t, r, `{"jsonrpc":"2.0","id":2,"method":"message/send","params":{"message":{"kind":"message","messageId":"m","role":"user","parts":[{"kind":"text","text":"hi"}]}}}`)
	if code != 200 || !strings.Contains(string(body), `"kind":"message"`) {
		t.Fatalf("message/send: %d %s", code, body)
	}
	code, body = postJSON(t, r, `{"jsonrpc":"2.0","id":3,"method":"nope","params":{}}`)
	if code != 200 || !strings.Contains(string(body), `"code":-32601`) {
		t.Fatalf("unknown method: %d %s", code, body)
	}
	_, raw := get(t, r, wellknown.PathCard)
	if !strings.Contains(string(raw), `"noAuth"`) {
		t.Fatal("open agent card should declare noAuth")
	}
}

// Acceptance 2 end to end: the served card verifies with the key served at
// its own jku.
func TestServedCardVerifies(t *testing.T) {
	r := start(t, withSkills(testConfig("station")))
	_, raw := get(t, r, wellknown.PathCard)
	err := wellknown.VerifyCard(raw, wellknown.Expect{Host: "station.localhost:8443"}, func(u string) ([]byte, error) {
		p, _ := url.Parse(u)
		_, body := get(t, r, p.Path)
		return body, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Acceptance 6: tier 2 can be switched off and the agent still starts.
func TestTier2OffStarts(t *testing.T) {
	c := testConfig("station")
	off := false
	c.Card.Tier2 = &off
	r := start(t, c)
	if code, _ := get(t, r, wellknown.PathHealth); code != 200 {
		t.Fatalf("health %d", code)
	}
	for _, p := range []string{wellknown.PathJWKS, wellknown.PathDID, wellknown.PathCatalog, wellknown.PathRobots} {
		if code, _ := get(t, r, p); code != http.StatusNotFound {
			t.Errorf("%s: %d with tier2 off", p, code)
		}
	}
	if code, _ := get(t, r, wellknown.PathCard); code != 200 {
		t.Fatalf("card %d", code)
	}
}

func TestIndexIsHTML(t *testing.T) {
	r := start(t, testConfig("ops"))
	resp, err := r.client.Get(r.url + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("content-type %q", resp.Header.Get("Content-Type"))
	}
}
