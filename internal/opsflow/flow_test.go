package opsflow

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/arjitsama/overpass/internal/jose"
	"github.com/arjitsama/overpass/internal/schema"
	"github.com/arjitsama/overpass/internal/verify"
)

// fakeA2A serves the A2A SendMessage envelope: it decodes the request's
// data.skill and returns the mapped response as the reply's data part. It counts
// calls per skill so a test can prove ordering (e.g. no quote before verify).
type fakeA2A struct {
	resp  map[string]any
	calls map[string]*int32
}

func newFakeA2A(resp map[string]any) *fakeA2A {
	f := &fakeA2A{resp: resp, calls: map[string]*int32{}}
	for k := range resp {
		var n int32
		f.calls[k] = &n
	}
	return f
}

func (f *fakeA2A) count(skill string) int32 {
	if c, ok := f.calls[skill]; ok {
		return atomic.LoadInt32(c)
	}
	return 0
}

func (f *fakeA2A) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Params struct {
				Message struct {
					Parts []struct {
						Data map[string]any `json:"data"`
					} `json:"parts"`
				} `json:"message"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		skill, _ := req.Params.Message.Parts[0].Data["skill"].(string)
		if c, ok := f.calls[skill]; ok {
			atomic.AddInt32(c, 1)
		}
		data, ok := f.resp[skill]
		if !ok {
			http.Error(w, `{"code":"NOT_FOUND","detail":"no such skill"}`, http.StatusNotFound)
			return
		}
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "1",
			"result": map[string]any{"message": map[string]any{"parts": []any{map[string]any{"data": data}}}}})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	})
}

// stubPeers returns a fixed verify.Result for VerifyPeer.
type stubPeers struct{ res verify.Result }

func (s stubPeers) VerifyPeer(context.Context, string) verify.Result { return s.res }

func newFlow(peers PeerVerifier) *Flow {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	return &Flow{Peers: peers, Sign: noSign{}, HTTP: http.DefaultClient, OpsANS: "ans://v0.1.0.ops.example",
		CommandKey: key}
}

type noSign struct{}

func (noSign) Attach(*http.Request) error { return nil }

// TestRunHappyPath: verify -> quote -> mandate (authority) -> book -> relay, each
// over A2A, and the result carries every id and the ack.
func TestRunHappyPath(t *testing.T) {
	// A real authority-signed mandate so VerifyMandate succeeds in the flow.
	authKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	mandate := signTestMandate(t, authKey)

	station := newFakeA2A(map[string]any{
		"get_pass_quote": map[string]any{"quote_id": "q-1", "station": "ans://v0.1.0.gs.example",
			"norad_id": 27844, "aos": 100, "los": 500, "mode": "uplink", "amount_cents": 1200},
		"book_pass":     map[string]any{"booking_id": "b-1"},
		"relay_command": map[string]any{"counter": 1, "result": schema.AckAccepted},
	})
	authority := newFakeA2A(map[string]any{"issue_mandate": map[string]any{"mandate": mandate}})
	sSrv := httptest.NewServer(station.handler())
	defer sSrv.Close()
	aSrv := httptest.NewServer(authority.handler())
	defer aSrv.Close()

	f := newFlow(stubPeers{verify.Result{Verdict: verify.Pass, ANSName: "ans://v0.1.0.gs.example"}})
	f.AuthKeys = []*ecdsa.PublicKey{&authKey.PublicKey}
	res, err := f.Run(context.Background(), Target{
		StationHost: "gs.example", StationURL: sSrv.URL, AuthorityURL: aSrv.URL,
		NoradID: 27844, Mode: "uplink", AOS: 100, LOS: 500, MaxElevDeg: 45,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.QuoteID != "q-1" || res.MandateID == "" || res.BookingID != "b-1" || res.Ack.Result != schema.AckAccepted {
		t.Fatalf("result = %+v", res)
	}
	if station.count("get_pass_quote") != 1 || authority.count("issue_mandate") != 1 ||
		station.count("book_pass") != 1 || station.count("relay_command") != 1 {
		t.Fatalf("call counts: quote=%d mandate=%d book=%d relay=%d",
			station.count("get_pass_quote"), authority.count("issue_mandate"),
			station.count("book_pass"), station.count("relay_command"))
	}
}

// TestUnverifiedPeerRefusedBeforeQuote: an unverified station is refused and NO
// quote is ever requested.
func TestUnverifiedPeerRefusedBeforeQuote(t *testing.T) {
	station := newFakeA2A(map[string]any{"get_pass_quote": map[string]any{"quote_id": "q-1"}})
	sSrv := httptest.NewServer(station.handler())
	defer sSrv.Close()

	f := newFlow(stubPeers{verify.Result{Verdict: verify.Fail,
		Checks: []verify.Check{{Name: "registered", Verdict: verify.Fail, Reason: "not in ANS"}}}})
	_, err := f.Run(context.Background(), Target{
		StationHost: "gs-sva1bard.example", StationURL: sSrv.URL, AuthorityURL: sSrv.URL,
		NoradID: 27844, Mode: "uplink", AOS: 100, LOS: 500,
	})
	if err == nil {
		t.Fatal("want a refusal for an unverified station")
	}
	if station.count("get_pass_quote") != 0 {
		t.Fatalf("a quote was requested from an unverified station (%d calls)", station.count("get_pass_quote"))
	}
}

func signTestMandate(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	jkt, err := jose.Thumbprint(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	m := schema.Mandate{MandateID: "m-1", Iss: "ans://v0.1.0.authority.example", Sub: "ans://v0.1.0.ops.example",
		Aud: "ans://v0.1.0.gs.example", QuoteID: "q-1", Scope: schema.Scope("uplink", 27844),
		CommandClasses: []string{"telemetry"}, MaxAmountCents: 1200, Nbf: 100, Exp: 500, JKT: jkt, Nonce: "0123456789abcdef"}
	tok, err := schema.SignMandate(m, key)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}
