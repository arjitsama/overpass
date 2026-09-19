package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentnameservice/ans-sdk-go/examples/a2a-no-mtls/demokit"

	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/schema"
	"github.com/arjitsama/overpass/internal/verify"
)

// Over the real routes: Ops books a pass on a station, then relays signed
// commands through it to a real spacecraft agent over A2A. The station checks
// the Ops agent's status token against a log (here a stub serving demokit's
// TL-signed token), enforces the mandate's classes, and keeps evidence.
func TestPassOverHTTP(t *testing.T) {
	tlKey, bundle, err := demokit.Provision(demokit.DemoAnsName, demokit.DemoAgentID)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// The spacecraft holds the Ops identity key's public half.
	opsPub, _ := x509.MarshalPKIXPublicKey(&bundle.AgentKey.PublicKey)
	opsPubPath := filepath.Join(dir, "ops.pub.pem")
	_ = os.WriteFile(opsPubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: opsPub}), 0o600)
	sc := testConfig("spacecraft")
	sc.DBPath = filepath.Join(dir, "sc.db")
	sc.Spacecraft = config.SpacecraftCfg{NoradID: 27844, OpsKeyFile: opsPubPath}
	sc.Card.Skills = []config.Skill{{ID: "uplink", Name: "Uplink"}}
	scAgent := start(t, sc)
	scCA := filepath.Join(dir, "sc.pem")
	_ = os.WriteFile(scCA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: scAgent.agent.tls.Certificates[0].Leaf.Raw}), 0o600)

	// A stub transparency log serving the Ops agent's status token.
	logSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/agents/"+demokit.DemoAgentID+"/status-token" {
			_, _ = w.Write(bundle.StatusToken)
			return
		}
		http.NotFound(w, r)
	}))
	defer logSrv.Close()

	gs, authKey, _ := bookingStationWith(t, tlKey, bundle, func(c *config.Config) {
		c.Card.Skills = append(c.Card.Skills, config.Skill{ID: "relay_command", Name: "Relay"}, config.Skill{ID: "session_evidence", Name: "Evidence"})
		c.Session = config.SessionCfg{SpacecraftURL: scAgent.url, SpacecraftCA: scCA, OpsEnv: "local"}
		c.Environments = map[string]config.Environment{"local": {RegistryURL: logSrv.URL, LogURL: logSrv.URL,
			InsecureHTTP: true, RootKeys: []string{c2sp(t, &tlKey.PublicKey)}}}
	})
	out, _ := verify.NewOutboundStatic(bundle.AgentKey, bundle.CertDER, bundle.Receipt, bundle.StatusToken)
	now := time.Now().Unix()
	_, body, _ := call(t, gs, out, map[string]any{"skill": "get_pass_quote", "norad_id": 27844, "aos": now + 20, "los": now + 320, "mode": "uplink", "max_elevation_deg": 40})
	var q schema.Quote
	result(t, body, &q)
	m := schema.Mandate{MandateID: "m-pass-e2e", Iss: "ans://v0.1.0.authority.localhost", Sub: demokit.DemoAnsName, Aud: q.Station,
		QuoteID: q.QuoteID, Scope: schema.Scope(q.Mode, q.NoradID), CommandClasses: []string{"telemetry"},
		MaxAmountCents: q.AmountCents, Nbf: q.AOS, Exp: q.LOS, JKT: out.JKT(), Nonce: "bm9uY2Utbm9uY2Utbm9uY2Uy"}
	mtok, _ := schema.SignMandate(m, authKey)
	_, body, _ = call(t, gs, out, map[string]any{"skill": "book_pass", "quote_id": q.QuoteID, "mandate": mtok})
	var booked struct {
		BookingID string `json:"booking_id"`
	}
	result(t, body, &booked)

	cmd := func(counter int64, class string) string {
		tok, _ := schema.SignCommand(schema.Command{NoradID: 27844, Counter: counter, MandateID: m.MandateID, Class: class,
			Body: json.RawMessage(`{"op":"dump"}`), IssuedAt: time.Now().Unix()}, bundle.AgentKey)
		return tok
	}
	_, body, _ = call(t, gs, out, map[string]any{"skill": "relay_command", "booking_id": booked.BookingID, "command": cmd(1, "telemetry")})
	var ack schema.Ack
	result(t, body, &ack)
	if ack.Result != schema.AckAccepted || ack.Counter != 1 {
		t.Fatalf("ack %+v", ack)
	}
	_, body, _ = call(t, gs, out, map[string]any{"skill": "relay_command", "booking_id": booked.BookingID, "command": cmd(2, "attitude")})
	if !strings.Contains(string(body), string(errs.ClassRejected)) {
		t.Fatalf("class outside mandate: %s", body)
	}
	_, body, _ = call(t, gs, out, map[string]any{"skill": "session_evidence", "booking_id": booked.BookingID})
	var ev struct {
		Head    string `json:"chain_head"`
		Records []any  `json:"records"`
		Acks    []any  `json:"acks"`
	}
	result(t, body, &ev)
	if len(ev.Records) != 1 || len(ev.Acks) != 1 || len(ev.Head) != 64 {
		t.Fatalf("evidence %s", body)
	}
}
