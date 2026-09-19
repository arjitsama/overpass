package verify

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentnameservice/ans-sdk-go/verify/scitt"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/jose"
	"github.com/arjitsama/overpass/internal/wellknown"
)

// Acceptance 4: the status token policy table.
func TestTokenPolicyTable(t *testing.T) {
	now := time.Unix(1_760_000_000, 0)
	p := Policy{MaxAge: DefaultMaxAge}
	active := func(age time.Duration) *Token { return &Token{Status: scitt.StatusActive, Iat: now.Add(-age)} }
	fetchErr := errors.New("log unreachable")
	cases := []struct {
		name          string
		held, fresh   *Token
		err           error
		allow, warned bool
	}{
		{"fresh ACTIVE passes", nil, active(0), nil, true, false},
		{"fetch error, 5-minute-old token passes with a warning", active(5 * time.Minute), nil, fetchErr, true, true},
		{"fetch error, 11-minute-old token fails", active(11 * time.Minute), nil, fetchErr, false, false},
		{"non-ACTIVE token fails at once", active(time.Minute), &Token{Status: scitt.StatusRevoked, Iat: now}, nil, false, false},
		{"fetch error and nothing held fails", nil, nil, fetchErr, false, false},
		{"WARNING is not ACTIVE", nil, &Token{Status: scitt.StatusWarning, Iat: now}, nil, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := p.Decide(c.held, c.fresh, c.err, now)
			if d.Allow != c.allow || (d.Warning != "") != c.warned || (!d.Allow && d.Reason == "") {
				t.Fatalf("decision %+v", d)
			}
		})
	}
}

// A keeper forgets a held token once the log says the agent is revoked, so
// a later outage cannot fall back to the pre-revocation token.
func TestKeeperDropsRevoked(t *testing.T) {
	now := time.Unix(1_760_000_000, 0)
	next := []struct {
		tok *Token
		err error
	}{
		{&Token{Status: scitt.StatusActive, Iat: now}, nil},
		{&Token{Status: scitt.StatusRevoked, Iat: now}, nil},
		{nil, errors.New("outage")},
	}
	i := 0
	k := NewKeeper(Policy{MaxAge: DefaultMaxAge}, func(context.Context, string) (*Token, error) {
		n := next[i]
		i++
		return n.tok, n.err
	})
	k.now = func() time.Time { return now.Add(time.Minute) }
	if d := k.Check(context.Background(), "a"); !d.Allow {
		t.Fatal("active denied")
	}
	if d := k.Check(context.Background(), "a"); d.Allow {
		t.Fatal("revoked allowed")
	}
	if d := k.Check(context.Background(), "a"); d.Allow {
		t.Fatal("outage after revocation fell back to the old token")
	}
}

// Fetches can finish out of order: an ACTIVE token issued before a known
// revocation is refused, and never becomes the held fallback.
func TestKeeperOutOfOrderFetch(t *testing.T) {
	now := time.Unix(1_760_000_000, 0)
	seq := []*Token{
		{Status: scitt.StatusRevoked, Iat: now},
		{Status: scitt.StatusActive, Iat: now.Add(-time.Minute)}, // slow, older fetch lands late
		nil,
	}
	i := 0
	k := NewKeeper(Policy{MaxAge: DefaultMaxAge}, func(context.Context, string) (*Token, error) {
		tok := seq[i]
		i++
		if tok == nil {
			return nil, errors.New("outage")
		}
		return tok, nil
	})
	k.now = func() time.Time { return now.Add(time.Minute) }
	for n := 0; n < 3; n++ {
		if d := k.Check(context.Background(), "a"); d.Allow {
			t.Fatalf("check %d allowed after revocation: %+v", n, d)
		}
	}
}

// Acceptance 3: a forged card whose jku points elsewhere is rejected with
// CARD_REJECTED:jku, and no HTTP request reaches that host.
func TestForgedJKUNoRequest(t *testing.T) {
	var evilHits atomic.Int32
	evil := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		evilHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer evil.Close()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	payload := []byte(`{"name":"gs-blacksburg","url":"https://gs-blacksburg.example"}`)
	tok, _ := jose.SignDetached(jose.TypAgentCard, payload, key, evil.URL+wellknown.PathTrustCard)
	parts := strings.Split(tok, "..")
	card := `{"name":"gs-blacksburg","signatures":[{"header":{"kid":"x"},"protected":"` + parts[0] + `","signature":"` + parts[1] + `"}],"url":"https://gs-blacksburg.example"}`
	fetch := func(u string) ([]byte, error) {
		resp, err := evil.Client().Get(u)
		if err != nil {
			return nil, err
		}
		resp.Body.Close()
		return nil, errors.New("fetched")
	}
	err := wellknown.VerifyCard([]byte(card), wellknown.Expect{Host: "gs-blacksburg.example"}, fetch)
	if !errs.Is(err, errs.CardRejectedJKU) {
		t.Fatalf("err = %v", err)
	}
	if n := evilHits.Load(); n != 0 {
		t.Fatalf("%d request(s) reached the forged jku host", n)
	}
}

// Acceptance 7: every check emits an event, including skipped ones, plus one
// summary event. An unreachable DNS server fails registration fast.
func TestEveryCheckEmits(t *testing.T) {
	cfg := config.Config{Environments: map[string]config.Environment{"local": {
		RegistryURL: "http://127.0.0.1:1", LogURL: "http://127.0.0.1:1", DNSServer: "127.0.0.1:1", InsecureHTTP: true}},
		Peers: []config.Peer{{Name: "x", URL: "https://x.example", Env: "local", Dial: "127.0.0.1:1"}}}
	var events []bus.Event
	v, err := New(cfg, Options{Self: "ops", Emit: func(e bus.Event) { events = append(events, e) }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res := v.VerifyPeer(ctx, "x.example")
	if res.OK() || len(res.Checks) != 8 {
		t.Fatalf("result %+v", res)
	}
	if len(events) != 9 || events[8].Kind != "verify_peer" || events[8].Result != Fail {
		t.Fatalf("events %+v", events)
	}
	seen := map[string]bool{}
	for _, e := range events[:8] {
		if e.Kind != "verify_check" || e.Agent != "ops" || e.Subject != "x.example" {
			t.Fatalf("event %+v", e)
		}
		seen[e.Data["check"].(string)] = true
	}
	for _, c := range []string{CheckRegistered, CheckBadge, CheckReceipt, CheckStatusToken, CheckCertChain, CheckTLSA, CheckCardHash, CheckCardSignature} {
		if !seen[c] {
			t.Errorf("no event for %s", c)
		}
	}
	if !strings.Contains(res.Checks[0].Reason, "badge") {
		t.Errorf("registration reason %q", res.Checks[0].Reason)
	}
}

func TestUnknownEnvironmentFailsClosed(t *testing.T) {
	v, _ := New(config.Config{Peers: []config.Peer{{Name: "y", URL: "https://y.example", Env: "nowhere"}}}, Options{})
	res := v.VerifyPeer(context.Background(), "y.example")
	if res.OK() || !strings.Contains(res.Failed()[0], "unknown environment") {
		t.Fatalf("%+v", res)
	}
}

func TestBadgeURLMustBeOnTheLog(t *testing.T) {
	e := &env{cfg: config.Environment{LogURL: "http://localhost:18081", LogPublicURL: "https://localhost:18081"}}
	for u, want := range map[string]bool{
		"https://localhost:18081/v1/agents/abc":  true,
		"http://localhost:18081/v1/agents/abc":   true,
		"https://evil.example/v1/agents/abc":     false,
		"https://localhost:18081.evil.example/x": false,
		"https://localhost:9999/v1/agents/abc":   false,
		"https://x@localhost:18081/v1/agents/a":  false,
		"https://localhost:18081":                false,
	} {
		if got := e.onLog(u); got != want {
			t.Errorf("onLog(%s) = %v", u, got)
		}
	}
}

func TestFindByTagWithoutFinder(t *testing.T) {
	v, _ := New(config.Config{Environments: map[string]config.Environment{"prod": {RegistryURL: "https://a", LogURL: "https://b"}}}, Options{})
	if _, err := v.FindByTag(context.Background(), "prod", "uplink-uhf"); !errs.Is(err, errs.Unavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestFinderSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/search" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"identifier":"urn:air:gs-a.example:gs:a2a"},{"identifier":"urn:air:gs-a.example:gs:mcp"},{"identifier":"urn:air:GS-B.example:x:a2a"},{"identifier":"not-a-urn"}]}`))
	}))
	defer srv.Close()
	e := &env{cfg: config.Environment{FinderURL: srv.URL + "/v1"}, http: srv.Client()}
	hosts, err := e.searchTag(context.Background(), "uplink-uhf")
	if err != nil || strings.Join(hosts, ",") != "gs-a.example,gs-b.example" {
		t.Fatalf("%v %v", hosts, err)
	}
}

// Untrusted hosts never panic VerifyPeer, and Finder URNs with bad hosts are dropped.
func TestMalformedHostsFailClosed(t *testing.T) {
	v, _ := New(config.Config{Environments: map[string]config.Environment{"prod": {RegistryURL: "https://a", LogURL: "https://b"}}}, Options{})
	for _, h := range []string{"%zz", "", "a b", "evil.example/path", "x@y"} {
		res := v.VerifyPeer(context.Background(), h)
		if res.OK() || len(res.Checks) == 0 {
			t.Errorf("%q: %+v", h, res)
		}
	}
	for _, u := range []string{"urn:air:%zz:ns:n", "urn:air::ns:n", "urn:air:a b:ns:n"} {
		if publisherHost(u) != "" {
			t.Errorf("%q accepted", u)
		}
	}
	if (Result{}).OK() {
		t.Error("empty result is OK")
	}
}

func TestRewriteOnlyMatchesOrigin(t *testing.T) {
	rt, _ := newRewrite("https://localhost:18081", "http://localhost:18081")
	var got []string
	rt.next = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = append(got, r.URL.String())
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	})
	for _, u := range []string{"https://localhost:18081/v1/agents/a", "https://localhost:18081@evil.example/v1/agents/a", "https://localhost:18082/x"} {
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = rt.RoundTrip(req)
	}
	want := "http://localhost:18081/v1/agents/a,https://localhost:18081@evil.example/v1/agents/a,https://localhost:18082/x"
	if strings.Join(got, ",") != want {
		t.Fatalf("rewrote %v", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
