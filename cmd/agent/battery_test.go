package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentnameservice/ans-sdk-go/examples/a2a-no-mtls/demokit"

	"github.com/arjitsama/overpass/internal/auditor"
	"github.com/arjitsama/overpass/internal/battery"
	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/schema"
	"github.com/arjitsama/overpass/internal/verify"
	"github.com/arjitsama/overpass/internal/wellknown"
)

func hostPort(u string) string { return strings.TrimPrefix(u, "https://") }

func ecKey(t *testing.T) *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func writePubNamed(t *testing.T, path string, pub *ecdsa.PublicKey) string {
	der, _ := x509.MarshalPKIXPublicKey(pub)
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// batteryTarget starts a station (and a spacecraft) that trust two Ops
// identities and one authority key, and returns a Battery aimed at them.
func batteryTarget(t *testing.T, rogue bool) (*battery.Battery, *running) {
	t.Helper()
	dir := t.TempDir()
	tl1, b1, err := demokit.Provision(demokit.DemoAnsName, demokit.DemoAgentID)
	if err != nil {
		t.Fatal(err)
	}
	tl2, b2, err := demokit.Provision("ans://v0.1.0.ops2.localhost", "ops2-agent")
	if err != nil {
		t.Fatal(err)
	}
	authKey, otherKey, unknownKey := ecKey(t), ecKey(t), ecKey(t)
	authName, otherName := "ans://v0.1.0.authority.localhost", "ans://v0.1.0.authority.other.localhost"
	const norad = int64(27844)

	reg, err := schema.SignSatRegistry(schema.SatRegistry{Iss: authName, Iat: time.Now().Unix(), Satellites: []schema.Satellite{
		{NoradID: norad, AuthorityANSName: authName, OpsANSNames: []string{demokit.DemoAnsName}}}}, authKey)
	if err != nil {
		t.Fatal(err)
	}
	regPath := filepath.Join(dir, "registry.jws")
	_ = os.WriteFile(regPath, []byte(reg), 0o600)
	authPub := writePubNamed(t, filepath.Join(dir, "auth.pub"), &authKey.PublicKey)
	otherPub := writePubNamed(t, filepath.Join(dir, "other.pub"), &otherKey.PublicKey)

	// The spacecraft trusts the primary Ops key.
	opsPub := writePubNamed(t, filepath.Join(dir, "ops.pub"), &b1.AgentKey.PublicKey)
	scCfg := testConfig("spacecraft")
	scCfg.DBPath = filepath.Join(dir, "sc.db")
	scCfg.Spacecraft = config.SpacecraftCfg{NoradID: norad, OpsKeyFile: opsPub}
	scCfg.Card.Skills = []config.Skill{{ID: "uplink", Name: "Uplink"}}
	sc := start(t, scCfg)
	scCA := filepath.Join(dir, "sc.pem")
	_ = os.WriteFile(scCA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: sc.agent.tls.Certificates[0].Leaf.Raw}), 0o600)

	// A stub transparency log so the station can check the Ops agent's status
	// token during relay_command (the class/window checks come after it).
	logSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/agents/"+demokit.DemoAgentID+"/status-token" {
			_, _ = w.Write(b1.StatusToken)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(logSrv.Close)

	gs := bookingStationOn(t, dir, tl1, tl2, authName, otherName, authPub, otherPub, regPath, rogue, func(c *config.Config) {
		c.Card.Skills = append(c.Card.Skills, config.Skill{ID: "relay_command", Name: "Relay"}, config.Skill{ID: "session_evidence", Name: "Evidence"})
		c.Session = config.SessionCfg{SpacecraftURL: sc.url, SpacecraftCA: scCA, OpsEnv: "local"}
		c.Environments = map[string]config.Environment{"local": {RegistryURL: logSrv.URL, LogURL: logSrv.URL,
			InsecureHTTP: true, RootKeys: []string{c2sp(t, &tl1.PublicKey), c2sp(t, &tl2.PublicKey)}}}
	})

	out1, _ := verify.NewOutboundStatic(b1.AgentKey, b1.CertDER, b1.Receipt, b1.StatusToken)
	out2, _ := verify.NewOutboundStatic(b2.AgentKey, b2.CertDER, b2.Receipt, b2.StatusToken)
	pool := x509.NewCertPool()
	pool.AddCert(gs.agent.tls.Certificates[0].Leaf)
	pool.AddCert(sc.agent.tls.Certificates[0].Leaf)
	// The DPoP htu binds to the public URL host, so requests go to the public
	// URL; a dialer maps each public host:port to its real listener address.
	gsURL, _ := url.Parse(gs.agent.cfg.PublicURL)
	scURL, _ := url.Parse(sc.agent.cfg.PublicURL)
	addrs := map[string]string{gsURL.Host: hostPort(gs.url), scURL.Host: hostPort(sc.url)}
	hc := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if real, ok := addrs[addr]; ok {
				addr = real
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}}}

	b := &battery.Battery{
		HTTP: hc, StationURL: gs.agent.cfg.PublicURL + "/", StationANS: wellknown.ANSName(gs.agent.cfg), StationHost: gs.agent.cfg.Host,
		SpaceURL: sc.agent.cfg.PublicURL + "/",
		Ops:      out1, OpsANS: demokit.DemoAnsName, OpsJKT: out1.JKT(), OpsKey: b1.AgentKey,
		OpsWrong: out2, OpsWrong2: ecKey(t),
		AuthKey: authKey, AuthName: authName, OtherAuthKey: otherKey, OtherAuthName: otherName, UnknownKey: unknownKey,
		NoradID: norad, OtherStation: "ans://v0.1.0.gs-svalbard.localhost",
	}
	return b, gs
}

// bookingStationOn is bookingStationWith generalised for the battery: two
// trusted TL roots, two authority keys, and an optional rogue flag.
func bookingStationOn(t *testing.T, dir string, tl1, tl2 *ecdsa.PrivateKey, authName, otherName, authPub, otherPub, regPath string, rogue bool, adjust func(*config.Config)) *running {
	c := withSkills(testConfig("station"))
	c.DBPath = filepath.Join(dir, "station.db")
	c.SatReg = config.SatReg{File: regPath, SignerKeyFile: authPub}
	c.AuthorityKeys = []config.AuthorityKey{{ANSName: authName, KeyFile: authPub}, {ANSName: otherName, KeyFile: otherPub}}
	c.Pricing = config.Pricing{PerMinuteCents: 125, PayTo: "0xGroundStationBlacksburg", Network: "base-sepolia", Asset: "USDC", AssetDecimals: 6}
	if rogue {
		c.Rogue = true
		c.RogueAck = config.RogueAck
	}
	if adjust != nil {
		adjust(&c)
	}
	return start(t, c)
}

// Acceptance 1: every attack against an honest station is BLOCKED with the
// master-plan code.
func TestBatteryHonest(t *testing.T) {
	b, _ := batteryTarget(t, false)
	results := b.Run(context.Background())
	if len(results) < 20 {
		t.Fatalf("only %d attacks ran", len(results))
	}
	for _, r := range results {
		t.Logf("%-24s %-12s %s %s", r.Name, r.Verdict, r.Observed, r.Detail)
		if r.Verdict != battery.Blocked {
			t.Errorf("%s: %s (observed %s, expected %s): %s", r.Name, r.Verdict, r.Observed, r.Expected, r.Detail)
		}
	}
	// Acceptance 7: the gate passes for an honest station.
	if !battery.AllBlocked(results) {
		t.Fatal("AllBlocked is false for an honest station")
	}
}

// Acceptance 2 and 7: a rogue station is VULNERABLE to at least tamper_mandate
// and wrong_dpop_key_attack, and the gate fails.
func TestBatteryRogue(t *testing.T) {
	b, _ := batteryTarget(t, true)
	results := b.Run(context.Background())
	vuln := map[string]bool{}
	for _, r := range results {
		if r.Verdict == battery.Vulnerable {
			vuln[r.Name] = true
		}
	}
	for _, name := range []string{"tamper_mandate", "wrong_dpop_key_attack"} {
		if !vuln[name] {
			t.Errorf("%s not VULNERABLE against the rogue station", name)
		}
	}
	if battery.AllBlocked(results) {
		t.Fatal("AllBlocked is true for a rogue station")
	}
}

// Acceptance 5 (live): an honest station rejects both canary probes; a rogue
// station accepts them, and the auditor's report says CANARY_ACCEPTED.
func TestCanaryLive(t *testing.T) {
	honest, gs := batteryTarget(t, false)
	aud := &auditor.Auditor{ANSName: "ans://v0.1.0.auditor.localhost", Key: ecKey(t),
		Peers: passingPeers{}, AuthorityKeys: nil, StationKeys: func(string) []*ecdsa.PublicKey { return nil }}
	rep, _, err := aud.Canary(context.Background(), gs.agent.cfg.Host, honest.StationANS, honest)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verdict != schema.Pass || len(rep.Canaries) != 2 {
		t.Fatalf("honest canary report %+v", rep)
	}
	for _, c := range rep.Canaries {
		if c.Result != schema.Rejected {
			t.Errorf("honest station accepted %s", c.Probe)
		}
	}
	rogue, rgs := batteryTarget(t, true)
	rrep, _, err := aud.Canary(context.Background(), rgs.agent.cfg.Host, rogue.StationANS, rogue)
	if err != nil {
		t.Fatal(err)
	}
	accepted := 0
	for _, c := range rrep.Canaries {
		if c.Result == schema.Accepted && c.Code == "CANARY_ACCEPTED" {
			accepted++
		}
	}
	if rrep.Verdict != schema.Fail || accepted == 0 {
		t.Fatalf("rogue canary report %+v", rrep)
	}
}

type passingPeers struct{}

func (passingPeers) VerifyPeer(_ context.Context, host string) verify.Result {
	return verify.Result{Host: host, ANSName: "ans://v0.1.0." + host, Verdict: verify.Pass}
}

// Acceptance 3: an unregistered impostor is refused at verification, and no
// quote request is ever sent to it.
func TestImpostorRefusedNoQuote(t *testing.T) {
	var quoteHits int32
	impostor := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			atomic.AddInt32(&quoteHits, 1)
		}
		http.NotFound(w, r)
	}))
	defer impostor.Close()
	u, _ := url.Parse(impostor.URL)
	cfg := config.Config{Environments: map[string]config.Environment{"local": {
		RegistryURL: "https://ra.invalid", LogURL: "https://tl.invalid", DNSServer: "127.0.0.1:1", InsecureHTTP: false}},
		Peers: []config.Peer{{Name: "gs-sva1bard", URL: "https://gs-sva1bard.localhost", Env: "local", Dial: u.Host}}}
	v, err := verify.New(cfg, verify.Options{Self: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	res := v.VerifyPeer(context.Background(), "gs-sva1bard.localhost")
	if res.OK() {
		t.Fatalf("impostor verified: %+v", res)
	}
	if res.Checks[0].Name != "registered" || res.Checks[0].Verdict != verify.Fail {
		t.Fatalf("first check %+v", res.Checks[0])
	}
	if n := atomic.LoadInt32(&quoteHits); n != 0 {
		t.Fatalf("%d requests reached the impostor before it was refused", n)
	}
}
