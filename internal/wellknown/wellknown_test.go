package wellknown

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arjitsama/overpass/internal/a2a"
	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/errs"
)

func stationConfig(t *testing.T) config.Config {
	t.Helper()
	c := config.Config{Role: "station", Host: "gs-blacksburg.overpass.example", Port: 443}
	c.Card = config.Card{Version: "1.2.3", DisplayName: "GS Blacksburg", Description: "UHF ground station.",
		OrgName: "Overpass", OrgURL: "https://overpass.example",
		Skills: []config.Skill{
			{ID: "get_pass_quote", Name: "Pass quote", Description: "Price a pass.", Tags: []string{"uplink-uhf", "downlink-uhf"}},
			{ID: "book_pass", Name: "Book pass", Description: "Book with a mandate.", Tags: []string{"uplink-uhf"}},
		}}
	c.ApplyDefaults()
	return c
}

func stationSecurity() a2a.Security {
	return a2a.Security{
		HTTP:  []a2a.HTTPGuard{{Scheme: a2a.Scheme{Name: a2a.SchemeDPoP, Type: "http", Scheme: "DPoP", Description: "d"}}},
		Skill: map[string][]a2a.SkillGuard{"book_pass": {{Scheme: a2a.Scheme{Name: a2a.SchemeMandate, Type: "mandate", Description: "m"}}}},
	}
}

func build(t *testing.T, c config.Config, sec a2a.Security) (Files, Identity) {
	t.Helper()
	id, err := LoadIdentity(c.Identity, ANSName(c))
	if err != nil {
		t.Fatal(err)
	}
	files, err := Build(Input{Config: c, Identity: id, Security: sec})
	if err != nil {
		t.Fatal(err)
	}
	return files, id
}

func decode(t *testing.T, f File) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(f.Body, &m); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	return m
}

func requireFields(t *testing.T, name string, m map[string]any, fields ...string) {
	t.Helper()
	for _, f := range fields {
		if _, ok := m[f]; !ok {
			t.Errorf("%s: missing %q", name, f)
		}
	}
}

// Acceptance 1: every JSON file parses and has the webmesh-spec section 6
// fields we can truthfully serve. Deliberately absent (rule 6): WIMSE in
// x-identity, the MCP extension, botProfile, ARD trustManifest.
func TestRequiredFields(t *testing.T) {
	c := stationConfig(t)
	dir := t.TempDir()
	c.Card.AgentID = "de02d013-138e-4e50-9cae-daa20bc3dc37"
	c.Card.ReceiptFile = filepath.Join(dir, "receipt.b64")
	_ = os.WriteFile(c.Card.ReceiptFile, []byte("ZmFrZS1yZWNlaXB0\n"), 0o600)
	files, _ := build(t, c, stationSecurity())

	card := decode(t, files[PathCard])
	requireFields(t, "agent card", card, "name", "url", "description", "version", "protocolVersion", "provider",
		"supportedInterfaces", "capabilities", "securitySchemes", "securityRequirements", "defaultInputModes",
		"defaultOutputModes", "skills", "x-identity", "x-discovery", "signatures")
	requireFields(t, "provider", card["provider"].(map[string]any), "organization", "did")
	requireFields(t, "x-identity.ans", card["x-identity"].(map[string]any)["ans"].(map[string]any), "uri", "trustCard")

	tc := decode(t, files[PathTrustCard])
	requireFields(t, "trust card", tc, "ansName", "agentDisplayName", "version", "agentHost", "endpoints", "keys",
		"agentId", "transparencyReceipt")
	key := tc["keys"].([]any)[0].(map[string]any)
	requireFields(t, "trust card key", key, "kty", "crv", "x", "y", "kid", "use", "x5c")
	if key["kty"] != "EC" || key["crv"] != "P-256" || tc["ansName"] != "ans://v1.2.3.gs-blacksburg.overpass.example" {
		t.Errorf("trust card key/name: %v %v", key, tc["ansName"])
	}

	requireFields(t, "did", decode(t, files[PathDID]), "@context", "id", "verificationMethod", "authentication", "assertionMethod", "service")
	requireFields(t, "ard", decode(t, files[PathARD]), "specVersion", "host", "entries", "extensions")
	requireFields(t, "jwks", decode(t, files[PathJWKS]), "keys")
	requireFields(t, "health", decode(t, files[PathHealth]), "status", "a2aProtocolVersion")
	if string(files[PathARD].Body) != string(files[PathCatalog].Body) {
		t.Error("ard.json and ai-catalog.json differ")
	}
	if !strings.Contains(string(files[PathRobots].Body), "Agentmap: https://gs-blacksburg.overpass.example/.well-known/ai-catalog.json") {
		t.Errorf("robots: %s", files[PathRobots].Body)
	}
	html := string(files[PathIndex].Body)
	for _, want := range []string{`<html lang="en">`, "<title>", "<h1>", "ans://v1.2.3.gs-blacksburg.overpass.example", "book_pass"} {
		if !strings.Contains(html, want) {
			t.Errorf("index missing %q", want)
		}
	}
	for path, f := range files {
		if strings.HasSuffix(path, ".json") || path == PathHealth {
			if !json.Valid(f.Body) {
				t.Errorf("%s is not valid JSON", path)
			}
		}
	}
}

// Unconfigured facts are left out, never faked.
func TestOmitsUnconfigured(t *testing.T) {
	files, _ := build(t, stationConfig(t), stationSecurity())
	tc := decode(t, files[PathTrustCard])
	for _, f := range []string{"agentId", "transparencyReceipt", "botProfile"} {
		if _, ok := tc[f]; ok {
			t.Errorf("trust card has %q without config", f)
		}
	}
	card := string(files[PathCard].Body)
	for _, f := range []string{"ans_registered", "tl_badge", "trust_index", "dns_aid_svcb", "wimse", "modelcontextprotocol", "noAuth"} {
		if strings.Contains(card, f) {
			t.Errorf("card mentions %q without config", f)
		}
	}
}

// A2A 8.4 strips default-valued properties before canonicalizing, so our
// card must contain none or its signature would not verify for such clients.
func TestCardHasNoDefaultValues(t *testing.T) {
	files, _ := build(t, stationConfig(t), stationSecurity())
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case bool:
			if !x {
				t.Errorf("%s is false", path)
			}
		case string:
			if x == "" {
				t.Errorf("%s is empty", path)
			}
		case float64:
			if x == 0 {
				t.Errorf("%s is 0", path)
			}
		case map[string]any:
			for k, e := range x {
				walk(path+"."+k, e)
			}
		case []any:
			for i, e := range x {
				walk(path+"["+string(rune('0'+i))+"]", e)
			}
		}
	}
	var card map[string]any
	_ = json.Unmarshal(files[PathCard].Body, &card)
	for k, v := range card {
		// Empty arrays mean "no extra requirement" in securityRequirements.
		if k == "securityRequirements" || k == "skills" {
			continue
		}
		walk(k, v)
	}
}

func fetcherFor(files Files, host string) (TrustFetcher, *[]string) {
	var fetched []string
	return func(u string) ([]byte, error) {
		fetched = append(fetched, u)
		if u == "https://"+host+PathTrustCard {
			return files[PathTrustCard].Body, nil
		}
		return nil, errors.New("not found")
	}, &fetched
}

// Acceptance 2: the signature verifies with only the key from the card's jku.
func TestCardVerifiesViaJKU(t *testing.T) {
	c := stationConfig(t)
	files, _ := build(t, c, stationSecurity())
	fetch, fetched := fetcherFor(files, c.Host)
	if err := VerifyCard(files[PathCard].Body, Expect{Host: c.Host}, fetch); err != nil {
		t.Fatal(err)
	}
	if len(*fetched) != 1 || (*fetched)[0] != "https://gs-blacksburg.overpass.example/.well-known/ans/trust-card.json" {
		t.Fatalf("fetched %v", *fetched)
	}
	// A trust card from a different identity must not verify it.
	other, _ := build(t, c, stationSecurity())
	err := VerifyCard(files[PathCard].Body, Expect{Host: c.Host}, func(string) ([]byte, error) { return other[PathTrustCard].Body, nil })
	if !errs.Is(err, errs.CardRejectedSignature) {
		t.Fatalf("foreign trust card: %v", err)
	}
}

// Acceptance 3: changing any card field without re-signing fails.
func TestCardTamper(t *testing.T) {
	c := stationConfig(t)
	files, _ := build(t, c, stationSecurity())
	fetch, _ := fetcherFor(files, c.Host)
	var card map[string]any
	_ = json.Unmarshal(files[PathCard].Body, &card)
	for field := range card {
		if field == "signatures" || field == "url" {
			continue // url changes the jku pin instead; covered below
		}
		mut := clone(card)
		mut[field] = "tampered"
		raw, _ := json.Marshal(mut)
		if err := VerifyCard(raw, Expect{Host: c.Host}, fetch); !errs.Is(err, errs.CardRejectedSignature) {
			t.Errorf("tampered %s: %v", field, err)
		}
	}
	// A nested change: add a tag to a skill.
	mut := clone(card)
	mut["skills"].([]any)[0].(map[string]any)["tags"] = []any{"uplink-uhf", "uplink-sband"}
	raw, _ := json.Marshal(mut)
	if err := VerifyCard(raw, Expect{Host: c.Host}, fetch); !errs.Is(err, errs.CardRejectedSignature) {
		t.Errorf("tampered skill tags: %v", err)
	}
	// Removing noAuth-free security: drop securitySchemes entirely.
	mut = clone(card)
	delete(mut, "securitySchemes")
	raw, _ = json.Marshal(mut)
	if err := VerifyCard(raw, Expect{Host: c.Host}, fetch); !errs.Is(err, errs.CardRejectedSignature) {
		t.Errorf("dropped securitySchemes: %v", err)
	}
}

func clone(m map[string]any) map[string]any {
	raw, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// jku injection: a card whose url moved to another host no longer matches its
// jku, and a jku to another host is rejected before any fetch. The pin is the
// ANS-verified host, not anything the card says about itself.
func TestJKUPinning(t *testing.T) {
	c := stationConfig(t)
	files, _ := build(t, c, stationSecurity())
	fetch, fetched := fetcherFor(files, c.Host)
	var card map[string]any
	_ = json.Unmarshal(files[PathCard].Body, &card)
	card["url"] = "https://gs-svalbard.evil.example"
	raw, _ := json.Marshal(card)
	if err := VerifyCard(raw, Expect{Host: c.Host}, fetch); !errs.Is(err, errs.CardRejectedJKU) || len(*fetched) != 0 {
		t.Fatalf("moved url: %v fetched %v", err, *fetched)
	}
	for _, bad := range []string{"http://gs-blacksburg.overpass.example/.well-known/ans/trust-card.json",
		"https://evil.example/.well-known/ans/trust-card.json",
		"https://gs-blacksburg.overpass.example/keys.json",
		"https://user@gs-blacksburg.overpass.example/.well-known/ans/trust-card.json",
		"https://gs-blacksburg.overpass.example/.well-known/ans%2Ftrust-card.json",
		"https://gs-blacksburg.overpass.example/.well-known/ans/trust-card.json?",
		"https://gs-blacksburg.overpass.example:444/.well-known/ans/trust-card.json"} {
		if err := pinJKU(bad, "https://gs-blacksburg.overpass.example", "gs-blacksburg.overpass.example"); !errs.Is(err, errs.CardRejectedJKU) {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestVerifyCardMalformed(t *testing.T) {
	fetch := func(string) ([]byte, error) { return nil, errors.New("no") }
	for _, raw := range []string{``, `[]`, `{}`, `{"signatures":[]}`, `{"signatures":[{"protected":"!!","signature":"x"}]}`,
		`{"url":"https://a","signatures":[{"protected":"e30","signature":"x"}]}`, `{"a":1.5}`} {
		err := VerifyCard([]byte(raw), Expect{Host: "a"}, fetch)
		if !errs.Is(err, errs.CardParseError) && !errs.Is(err, errs.CardRejectedJKU) {
			t.Errorf("%q: %v", raw, err)
		}
	}
}

// Acceptance 6 (generator side): tier 2 off serves tier 1 only and the card
// stops claiming did:web and ARD.
func TestTier2Off(t *testing.T) {
	c := stationConfig(t)
	off := false
	c.Card.Tier2 = &off
	files, _ := build(t, c, stationSecurity())
	for _, p := range []string{PathJWKS, PathDID, PathARD, PathCatalog, PathRobots, PathLLMs} {
		if _, ok := files[p]; ok {
			t.Errorf("%s served with tier2 off", p)
		}
	}
	for _, p := range []string{PathCard, PathTrustCard, PathHealth, PathIndex} {
		if _, ok := files[p]; !ok {
			t.Errorf("%s missing with tier2 off", p)
		}
	}
	card := string(files[PathCard].Body)
	if strings.Contains(card, "did:web") || strings.Contains(card, "/.well-known/ard.json") {
		t.Errorf("card claims tier-2 anchors: %s", card)
	}
}

func TestLoadIdentityFromFiles(t *testing.T) {
	c := stationConfig(t)
	local, err := LoadIdentity(config.Identity{}, ANSName(c))
	if err != nil || !local.Local || local.Chain[0].URIs[0].String() != ANSName(c) {
		t.Fatalf("local identity: %+v %v", local, err)
	}
	dir := t.TempDir()
	keyDER, _ := x509.MarshalPKCS8PrivateKey(local.Key)
	write := func(name, typ string, der []byte) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600)
		return p
	}
	cfg := config.Identity{KeyFile: write("k.pem", "PRIVATE KEY", keyDER), ChainFile: write("c.pem", "CERTIFICATE", local.Chain[0].Raw)}
	id, err := LoadIdentity(cfg, ANSName(c))
	if err != nil || id.Local || !id.Key.Equal(local.Key) {
		t.Fatalf("from files: %v", err)
	}
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	otherDER, _ := x509.MarshalPKCS8PrivateKey(other)
	cfg.KeyFile = write("other.pem", "PRIVATE KEY", otherDER)
	if _, err := LoadIdentity(cfg, ANSName(c)); err == nil {
		t.Fatal("key/cert mismatch accepted")
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	c := stationConfig(t)
	id, _ := LoadIdentity(c.Identity, ANSName(c))
	a, _ := Build(Input{Config: c, Identity: id, Security: stationSecurity()})
	b, _ := Build(Input{Config: c, Identity: id, Security: stationSecurity()})
	for p := range a {
		if p == PathCard {
			continue // ECDSA signatures are randomized; the payload is compared below
		}
		if string(a[p].Body) != string(b[p].Body) {
			t.Errorf("%s differs between builds", p)
		}
	}
	strip := func(f File) string {
		var m map[string]any
		_ = json.Unmarshal(f.Body, &m)
		delete(m, "signatures")
		raw, _ := json.Marshal(m)
		return string(raw)
	}
	if strip(a[PathCard]) != strip(b[PathCard]) {
		t.Error("card payload differs between builds")
	}
}

// The review's high finding: an attacker's fully self-consistent card (own
// host, own jku, own key) must not verify for the host ANS verified.
func TestSelfConsistentForgeryRejected(t *testing.T) {
	real := stationConfig(t)
	evil := stationConfig(t)
	evil.Host = "gs-blacksburg.evil.example"
	evil.PublicURL = ""
	evil.ApplyDefaults()
	files, _ := build(t, evil, stationSecurity())
	fetch, fetched := fetcherFor(files, evil.Host)
	if err := VerifyCard(files[PathCard].Body, Expect{Host: evil.Host}, fetch); err != nil {
		t.Fatalf("control: evil card verifies for its own host: %v", err)
	}
	*fetched = nil
	err := VerifyCard(files[PathCard].Body, Expect{Host: real.Host}, fetch)
	if !errs.Is(err, errs.CardRejectedJKU) || len(*fetched) != 0 {
		t.Fatalf("forgery for %s: %v fetched %v", real.Host, err, *fetched)
	}
	if err := VerifyCard(files[PathCard].Body, Expect{}, fetch); !errs.Is(err, errs.CardRejectedJKU) {
		t.Fatalf("no expected host: %v", err)
	}
}

// Trust-card keys count only when the JWK is its own x5c leaf's key, and the
// leaf matches the attested fingerprint when one is given.
func TestTrustCardKeyBinding(t *testing.T) {
	c := stationConfig(t)
	files, id := build(t, c, stationSecurity())
	fetch, _ := fetcherFor(files, c.Host)
	sum := sha256.Sum256(id.Chain[0].Raw)
	if err := VerifyCard(files[PathCard].Body, Expect{Host: c.Host, LeafSHA256: hex.EncodeToString(sum[:])}, fetch); err != nil {
		t.Fatalf("attested leaf: %v", err)
	}
	err := VerifyCard(files[PathCard].Body, Expect{Host: c.Host, LeafSHA256: strings.Repeat("0", 64)}, fetch)
	if !errs.Is(err, errs.CardRejectedSignature) {
		t.Fatalf("wrong attested leaf: %v", err)
	}
	// Swap in another identity's x5c under the same JWK: the key no longer
	// matches its leaf, so it is not trusted.
	other, _ := LoadIdentity(config.Identity{}, ANSName(c))
	var tc map[string]any
	_ = json.Unmarshal(files[PathTrustCard].Body, &tc)
	tc["keys"].([]any)[0].(map[string]any)["x5c"] = []any{base64.StdEncoding.EncodeToString(other.Chain[0].Raw)}
	swapped, _ := json.Marshal(tc)
	err = VerifyCard(files[PathCard].Body, Expect{Host: c.Host}, func(string) ([]byte, error) { return swapped, nil })
	if !errs.Is(err, errs.CardRejectedSignature) {
		t.Fatalf("mismatched x5c: %v", err)
	}
}

func TestIdentityChainChecks(t *testing.T) {
	c := stationConfig(t)
	dir := t.TempDir()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	write := func(name, typ string, ders ...[]byte) string {
		p := filepath.Join(dir, name)
		var out []byte
		for _, d := range ders {
			out = append(out, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: d})...)
		}
		_ = os.WriteFile(p, out, 0o600)
		return p
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	expired := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(-24 * time.Hour)}
	expDER, _ := x509.CreateCertificate(rand.Reader, expired, expired, &key.PublicKey, key)
	_, err := LoadIdentity(config.Identity{KeyFile: write("k.pem", "PRIVATE KEY", keyDER), ChainFile: write("exp.pem", "CERTIFICATE", expDER)}, ANSName(c))
	if err == nil {
		t.Fatal("expired leaf accepted")
	}
	valid := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	leafDER, _ := x509.CreateCertificate(rand.Reader, valid, valid, &key.PublicKey, key)
	stranger, _ := LoadIdentity(config.Identity{}, ANSName(c))
	_, err = LoadIdentity(config.Identity{KeyFile: write("k.pem", "PRIVATE KEY", keyDER),
		ChainFile: write("chain.pem", "CERTIFICATE", leafDER, stranger.Chain[0].Raw)}, ANSName(c))
	if err == nil {
		t.Fatal("unlinked chain accepted")
	}
}
