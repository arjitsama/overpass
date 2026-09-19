package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
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
	"github.com/arjitsama/overpass/internal/wellknown"
)

// c2sp encodes a public key as a C2SP root-key string (name+hex kid+base64
// SPKI), with the kid demokit's log uses: the first 4 bytes of SHA-256(SPKI).
func c2sp(t *testing.T, pub *ecdsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return "demo-log+" + hex.EncodeToString(sum[:4]) + "+" + base64.StdEncoding.EncodeToString(der)
}

func writePub(t *testing.T, dir string, pub *ecdsa.PublicKey) string {
	der, _ := x509.MarshalPKIXPublicKey(pub)
	p := filepath.Join(dir, "authority.pub.pem")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// bookingStation starts a station that trusts demokit's log, knows one
// authority, and has a signed satellite registry naming the demo Ops agent.
func bookingStation(t *testing.T) (*running, *ecdsa.PrivateKey, *demokit.Bundle) {
	t.Helper()
	tlKey, bundle, err := demokit.Provision(demokit.DemoAnsName, demokit.DemoAgentID)
	if err != nil {
		t.Fatal(err)
	}
	return bookingStationWith(t, tlKey, bundle, nil)
}

// bookingStationWith is bookingStation with a given log key and Ops bundle,
// and a hook to adjust the config.
func bookingStationWith(t *testing.T, tlKey *ecdsa.PrivateKey, bundle *demokit.Bundle, adjust func(*config.Config)) (*running, *ecdsa.PrivateKey, *demokit.Bundle) {
	t.Helper()
	authKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	dir := t.TempDir()
	authName := "ans://v0.1.0.authority.localhost"
	reg, err := schema.SignSatRegistry(schema.SatRegistry{Iss: authName, Iat: time.Now().Unix(), Satellites: []schema.Satellite{
		{NoradID: 27844, AuthorityANSName: authName, OpsANSNames: []string{demokit.DemoAnsName}}}}, authKey)
	if err != nil {
		t.Fatal(err)
	}
	regPath := filepath.Join(dir, "registry.jws")
	_ = os.WriteFile(regPath, []byte(reg), 0o600)
	pub := writePub(t, dir, &authKey.PublicKey)

	c := withSkills(testConfig("station"))
	c.TrustRoots = []string{c2sp(t, &tlKey.PublicKey)}
	c.DBPath = filepath.Join(dir, "station.db")
	c.SatReg = config.SatReg{File: regPath, SignerKeyFile: pub}
	c.AuthorityKeys = []config.AuthorityKey{{ANSName: authName, KeyFile: pub}}
	c.Pricing = config.Pricing{PerMinuteCents: 125, PayTo: "0xGroundStationBlacksburg", Network: "base-sepolia", Asset: "USDC", AssetDecimals: 6}
	if adjust != nil {
		adjust(&c)
	}
	return start(t, c), authKey, bundle
}

// call sends one JSON-RPC SendMessage with a data part, signed with DPoP.
func call(t *testing.T, r *running, out *verify.Outbound, data map[string]any) (int, []byte, http.Header) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "SendMessage",
		"params": map[string]any{"message": map[string]any{"messageId": "m", "role": "ROLE_USER", "parts": []any{map[string]any{"data": data}}}}})
	req, _ := http.NewRequest(http.MethodPost, r.agent.cfg.PublicURL+"/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if out != nil {
		if err := out.Attach(req); err != nil {
			t.Fatal(err)
		}
	}
	return send(t, r, req)
}

func send(t *testing.T, r *running, req *http.Request) (int, []byte, http.Header) {
	t.Helper()
	addr := strings.TrimPrefix(r.url, "https://")
	pool := x509.NewCertPool()
	pool.AddCert(r.agent.tls.Certificates[0].Leaf)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost"},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, req.Header
}

func result(t *testing.T, body []byte, v any) {
	t.Helper()
	var resp struct {
		Result struct {
			Message struct {
				Parts []struct {
					Data json.RawMessage `json:"data"`
				} `json:"parts"`
			} `json:"message"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.Error != nil || len(resp.Result.Message.Parts) == 0 {
		t.Fatalf("rpc: %s", body)
	}
	if err := json.Unmarshal(resp.Result.Message.Parts[0].Data, v); err != nil {
		t.Fatal(err)
	}
}

// Over the real routes, with real DPoP proofs: quote, book once, the replayed
// proof is DPOP_REJECTED:replay, the spent mandate is MANDATE_REJECTED:consumed,
// and the quote's payTo is the one in the signed card (acceptance 6).
func TestBookingOverHTTP(t *testing.T) {
	r, authKey, bundle := bookingStation(t)
	out, err := verify.NewOutboundStatic(bundle.AgentKey, bundle.CertDER, bundle.Receipt, bundle.StatusToken)
	if err != nil {
		t.Fatal(err)
	}
	aos := time.Now().Unix() + 3600
	code, body, _ := call(t, r, out, map[string]any{"skill": "get_pass_quote", "norad_id": 27844, "aos": aos, "los": aos + 540, "mode": "uplink", "max_elevation_deg": 61})
	if code != 200 {
		t.Fatalf("quote: %d %s", code, body)
	}
	var q schema.Quote
	result(t, body, &q)

	_, cardRaw := get(t, r, wellknown.PathCard)
	var card struct {
		XPayment struct {
			PayTo string `json:"payTo"`
		} `json:"x-payment"`
	}
	_ = json.Unmarshal(cardRaw, &card)
	if card.XPayment.PayTo == "" || q.Accepts[0].PayTo != card.XPayment.PayTo {
		t.Fatalf("quote payTo %q, card payTo %q", q.Accepts[0].PayTo, card.XPayment.PayTo)
	}

	m := schema.Mandate{MandateID: "m-e2e", Iss: "ans://v0.1.0.authority.localhost", Sub: demokit.DemoAnsName, Aud: q.Station,
		QuoteID: q.QuoteID, Scope: schema.Scope(q.Mode, q.NoradID), CommandClasses: []string{"telemetry"},
		MaxAmountCents: q.AmountCents, Nbf: q.AOS, Exp: q.LOS, JKT: out.JKT(), Nonce: "bm9uY2Utbm9uY2Utbm9uY2Ux"}
	tok, _ := schema.SignMandate(m, authKey)
	args := map[string]any{"skill": "book_pass", "quote_id": q.QuoteID, "mandate": tok}
	code, body, hdr := call(t, r, out, args)
	if code != 200 {
		t.Fatalf("book: %d %s", code, body)
	}
	var res struct {
		BookingID string `json:"booking_id"`
		Receipt   string `json:"receipt"`
	}
	result(t, body, &res)
	if res.BookingID == "" || res.Receipt == "" {
		t.Fatalf("booking %s", body)
	}

	// Replay the exact request (same DPoP proof): refused before the mandate is read.
	reqBody, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "SendMessage",
		"params": map[string]any{"message": map[string]any{"messageId": "m", "role": "ROLE_USER", "parts": []any{map[string]any{"data": args}}}}})
	replay, _ := http.NewRequest(http.MethodPost, r.agent.cfg.PublicURL+"/", bytes.NewReader(reqBody))
	replay.Header = hdr.Clone()
	code, body, _ = send(t, r, replay)
	if code != 401 || !strings.Contains(string(body), string(errs.DPoPRejectedReplay)) {
		t.Fatalf("replayed proof: %d %s", code, body)
	}
	// A fresh proof with the spent mandate: consumed.
	_, body, _ = call(t, r, out, args)
	if !strings.Contains(string(body), string(errs.MandateRejectedConsumed)) {
		t.Fatalf("spent mandate: %s", body)
	}
	// No proof at all.
	code, body, _ = call(t, r, nil, args)
	if code != 401 || !strings.Contains(string(body), string(errs.CallerRejected)) {
		t.Fatalf("no proof: %d %s", code, body)
	}
}
