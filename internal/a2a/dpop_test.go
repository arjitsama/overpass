package a2a

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentnameservice/ans-sdk-go/examples/a2a-no-mtls/demokit"
	"github.com/agentnameservice/ans-sdk-go/pop"
	"github.com/agentnameservice/ans-sdk-go/verify/scitt"

	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/schema"
)

// TestDPoPGuardAcceptsProvenCaller is the success path the reject-only tests
// cannot show: with a real transparency-log key (minted by ans-sdk-go's
// demokit), a caller holding a receipt, status token and DPoP proof gets
// through the mounted guards, the handler sees the proven identity, and a
// mandate-guarded skill still demands its mandate.
func TestDPoPGuardAcceptsProvenCaller(t *testing.T) {
	tlKey, bundle, err := demokit.Provision(demokit.DemoAnsName, demokit.DemoAgentID)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := demokit.KeyLookup(&tlKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	authority := newKey(t)
	var caller string
	sec := Security{
		HTTP:  []HTTPGuard{DPoPGuard(keys, pop.NewMemoryReplayCache(ctx, 100), "gs.example", quiet)},
		Skill: map[string][]SkillGuard{"book_pass": {MandateDeclared()}},
	}
	h := testServer(sec, map[string]Handler{
		"get_pass_quote": func(ctx context.Context, _ json.RawMessage) (any, error) {
			if id, ok := pop.CallerFromContext(ctx); ok {
				caller = id.AnsName
			}
			return map[string]int{"amount_cents": 1200}, nil
		},
		// The real book_pass (internal/station) runs all mandate checks; this
		// stand-in verifies the signature so the test sees the whole path.
		"book_pass": func(_ context.Context, args json.RawMessage) (any, error) {
			var a struct {
				Mandate string `json:"mandate"`
			}
			_ = json.Unmarshal(args, &a)
			if a.Mandate == "" {
				return nil, errs.New(errs.MandateParseError, "mandate is missing")
			}
			if _, err := schema.VerifyMandate(a.Mandate, []*ecdsa.PublicKey{&authority.PublicKey}); err != nil {
				return nil, err
			}
			return map[string]string{"booking_id": "b-1"}, nil
		},
	})
	srv := httptest.NewTLSServer(h)
	defer srv.Close()
	signer, err := pop.NewSigner(bundle.AgentKey, bundle.CertDER)
	if err != nil {
		t.Fatal(err)
	}
	headers := scitt.GenerateHeaders(bundle.Receipt, bundle.StatusToken)

	call := func(body string, proven bool) (int, []byte) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader(body))
		req.Host = "gs.example" // the trusted authority the proof's htu binds to
		req.Header.Set("Content-Type", "application/json")
		if proven {
			req.URL.Host = "gs.example"
			if err := pop.AttachIdentity(req, signer, headers); err != nil {
				t.Fatal(err)
			}
			req.URL.Host = strings.TrimPrefix(srv.URL, "https://")
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}

	code, body := call(skillCall("get_pass_quote", ""), true)
	if code != 200 || !bytes.Contains(body, []byte(`"amount_cents":1200`)) || caller != demokit.DemoAnsName {
		t.Fatalf("proven call: %d %s caller=%q", code, body, caller)
	}
	code, body = call(skillCall("get_pass_quote", ""), false)
	if code != http.StatusUnauthorized || !bytes.Contains(body, []byte(errs.CallerRejected)) {
		t.Fatalf("unproven call: %d %s", code, body)
	}
	// Past DPoP, book_pass still needs a mandate from the authority.
	code, body = call(skillCall("book_pass", ""), true)
	if code != 200 || !bytes.Contains(body, []byte(errs.MandateParseError)) {
		t.Fatalf("book_pass without mandate: %d %s", code, body)
	}
	m := schema.Mandate{MandateID: "m-1", Iss: "authority.x", Sub: "ops.x", Aud: "gs.x", QuoteID: "q-1",
		Scope: schema.Scope("uplink", 25544), CommandClasses: []string{"telemetry"}, MaxAmountCents: 1,
		Nbf: 1760000000, Exp: 1760000480, JKT: strings.Repeat("A", 43), Nonce: "bm9uY2Utbm9uY2Utbm9uY2U"}
	tok, _ := schema.SignMandate(m, authority)
	code, body = call(skillCall("book_pass", `,"mandate":"`+tok+`"`), true)
	if code != 200 || !bytes.Contains(body, []byte(`"booking_id":"b-1"`)) {
		t.Fatalf("book_pass with mandate: %d %s", code, body)
	}
}
