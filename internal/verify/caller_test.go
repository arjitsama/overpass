package verify

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agentnameservice/ans-sdk-go/examples/a2a-no-mtls/demokit"
	"github.com/agentnameservice/ans-sdk-go/pop"

	"github.com/arjitsama/overpass/internal/a2a"
	"github.com/arjitsama/overpass/internal/errs"
)

// Acceptance 5: the outbound helper's requests pass the inbound middleware
// (with the proven identity available through Caller); a request with no
// valid proof gets a named rejection; replaying a proof is rejected.
func TestOutboundInboundAndReplay(t *testing.T) {
	tlKey, bundle, err := demokit.Provision(demokit.DemoAnsName, demokit.DemoAgentID)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := demokit.KeyLookup(&tlKey.PublicKey)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	guard := a2a.DPoPGuard(keys, pop.NewMemoryReplayCache(ctx, 100), "gs.example", quiet)
	var who string
	srv := httptest.NewTLSServer(guard.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := Caller(r.Context()); ok {
			who = id.AnsName
		}
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()

	out, err := NewOutboundStatic(bundle.AgentKey, bundle.CertDER, bundle.Receipt, bundle.StatusToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.JKT()) != 43 {
		t.Fatalf("jkt %q", out.JKT())
	}
	send := func(req *http.Request) (int, string) {
		req.URL.Host = srv.Listener.Addr().String()
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	signed := func() *http.Request {
		req, _ := http.NewRequest(http.MethodPost, "https://gs.example/", bytes.NewReader([]byte(`{"a":1}`)))
		req.Host = "gs.example"
		if err := out.Attach(req); err != nil {
			t.Fatal(err)
		}
		return req
	}

	if code, body := send(signed()); code != 200 || who != demokit.DemoAnsName {
		t.Fatalf("signed request: %d %s who=%q", code, body, who)
	}
	unsigned, _ := http.NewRequest(http.MethodPost, "https://gs.example/", bytes.NewReader([]byte(`{"a":1}`)))
	unsigned.Host = "gs.example"
	if code, body := send(unsigned); code != 401 || !bytes.Contains([]byte(body), []byte(errs.CallerRejected)) {
		t.Fatalf("unsigned: %d %s", code, body)
	}
	// Replay: the same proof and body, sent twice.
	first := signed()
	replay, _ := http.NewRequest(http.MethodPost, "https://gs.example/", bytes.NewReader([]byte(`{"a":1}`)))
	replay.Host = "gs.example"
	replay.Header = first.Header.Clone()
	if code, _ := send(first); code != 200 {
		t.Fatalf("first use: %d", code)
	}
	if code, body := send(replay); code != 401 || !bytes.Contains([]byte(body), []byte(errs.CallerRejected)) {
		t.Fatalf("replay: %d %s", code, body)
	}
}
