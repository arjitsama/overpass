package wellknown

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	gojose "github.com/go-jose/go-jose/v4"

	"github.com/arjitsama/overpass/internal/a2a"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/jose"
	"github.com/arjitsama/overpass/internal/schema"
)

// agentCard follows docs/webmesh-spec.md 2.1. Every field is omitted when
// empty: A2A section 8.4 strips default-valued properties before
// canonicalizing, so a card with none signs the same either way.
type agentCard struct {
	Name                 string                `json:"name"`
	URL                  string                `json:"url"`
	Description          string                `json:"description"`
	Version              string                `json:"version"`
	ProtocolVersion      string                `json:"protocolVersion"`
	Provider             *provider             `json:"provider,omitempty"`
	DocumentationURL     string                `json:"documentationUrl"`
	SupportedInterfaces  []iface               `json:"supportedInterfaces"`
	Capabilities         capabilities          `json:"capabilities"`
	SecuritySchemes      map[string]a2a.Scheme `json:"securitySchemes"`
	SecurityRequirements []map[string][]string `json:"securityRequirements"`
	DefaultInputModes    []string              `json:"defaultInputModes"`
	DefaultOutputModes   []string              `json:"defaultOutputModes"`
	Skills               []skill               `json:"skills"`
	XIdentity            xIdentity             `json:"x-identity"`
	XDiscovery           xDiscovery            `json:"x-discovery"`
	XSecurityNote        string                `json:"x-security-note"`
	XPayment             *xPayment             `json:"x-payment,omitempty"`
	Signatures           []signature           `json:"signatures,omitempty"`
}

// xPayment is where a station is paid. Quotes carry the same payTo in their
// x402-shaped accepts block (master plan 8.2).
type xPayment struct {
	Scheme  string `json:"scheme"`
	PayTo   string `json:"payTo"`
	Network string `json:"network,omitempty"`
	Asset   string `json:"asset,omitempty"`
	Note    string `json:"note,omitempty"` // e.g. "simulated; no settlement is performed"
}

type provider struct {
	Organization string `json:"organization"`
	URL          string `json:"url,omitempty"`
	DID          string `json:"did,omitempty"`
}

type iface struct {
	URL             string `json:"url"`
	ProtocolBinding string `json:"protocolBinding"`
	ProtocolVersion string `json:"protocolVersion"`
}

type capabilities struct {
	Extensions []extension `json:"extensions"`
}

type extension struct {
	URI         string         `json:"uri"`
	Description string         `json:"description"`
	Params      map[string]any `json:"params"`
}

type skill struct {
	ID                   string                `json:"id"`
	Name                 string                `json:"name"`
	Description          string                `json:"description"`
	Tags                 []string              `json:"tags"`
	Examples             []string              `json:"examples,omitempty"`
	SecurityRequirements []map[string][]string `json:"securityRequirements"`
}

type xIdentity struct {
	ANS xANS `json:"ans"`
}

type xANS struct {
	URI             string `json:"uri"`
	TrustCard       string `json:"trustCard"`
	TransparencyLog string `json:"transparencyLog,omitempty"`
}

type xDiscovery struct {
	ANSRegistered string      `json:"ans_registered,omitempty"`
	ANSName       string      `json:"ans_name"`
	TLBadge       string      `json:"tl_badge,omitempty"`
	TrustIndex    *trustIndex `json:"trust_index,omitempty"`
	DNSAIDSVCB    string      `json:"dns_aid_svcb,omitempty"`
}

type trustIndex struct {
	ScoreURL   string `json:"score_url"`
	ScoreField string `json:"score_field"`
	Auth       string `json:"auth"`
}

type signature struct {
	Protected string            `json:"protected"`
	Signature string            `json:"signature"`
	Header    map[string]string `json:"header"`
}

func (b builder) card(files Files) error {
	card := b.unsignedCard()
	payload, err := jose.Canonicalize(card)
	if err != nil {
		return err
	}
	if saved, ok := b.reuseSigned(payload); ok {
		files[PathCard] = File{Body: saved, ContentType: "application/json"}
		return nil
	}
	tok, err := jose.SignDetached(jose.TypAgentCard, payload, b.id.Key, b.url(PathTrustCard))
	if err != nil {
		return err
	}
	parts := strings.Split(tok, "..")
	card.Signatures = []signature{{Protected: parts[0], Signature: parts[1], Header: map[string]string{"kid": b.kid}}}
	if err := putJSON(files, PathCard, card); err != nil {
		return err
	}
	if b.c.Card.SignedFile != "" {
		if err := os.WriteFile(b.c.Card.SignedFile, files[PathCard].Body, 0o644); err != nil {
			return fmt.Errorf("card signed_file: %w", err)
		}
	}
	return nil
}

// reuseSigned returns the persisted signed card when its payload equals
// payload and its signature verifies under this agent's key. ECDSA
// signatures are randomized, so re-signing would change the card's bytes
// and break the registered metaDataHash (master plan 5A: freeze cards).
func (b builder) reuseSigned(payload []byte) ([]byte, bool) {
	if b.c.Card.SignedFile == "" {
		return nil, false
	}
	raw, err := os.ReadFile(b.c.Card.SignedFile)
	if err != nil || jose.StrictJSON(raw, schema.MaxObjectBytes) != nil {
		return nil, false
	}
	var card map[string]json.RawMessage
	var sigs []signature
	if json.Unmarshal(raw, &card) != nil || json.Unmarshal(card["signatures"], &sigs) != nil || len(sigs) != 1 {
		return nil, false
	}
	delete(card, "signatures")
	saved, err := jose.Canonicalize(card)
	if err != nil || !bytes.Equal(saved, payload) {
		return nil, false
	}
	_, err = jose.VerifyDetached(sigs[0].Protected+".."+sigs[0].Signature, payload, schema.CardProfile,
		[]*ecdsa.PublicKey{&b.id.Key.PublicKey}, nil)
	if err != nil {
		return nil, false
	}
	canon, err := jose.Transform(raw)
	return canon, err == nil
}

func (b builder) unsignedCard() agentCard {
	c := b.c
	card := agentCard{
		Name: c.Card.DisplayName, URL: c.PublicURL, Description: b.description(), Version: c.Card.Version,
		ProtocolVersion: A2AProtocolVersion, DocumentationURL: c.PublicURL,
		SupportedInterfaces: []iface{{URL: c.PublicURL, ProtocolBinding: "JSONRPC", ProtocolVersion: A2AProtocolVersion}},
		Capabilities:        capabilities{Extensions: b.extensions()},
		SecuritySchemes:     b.sec.Schemes(), SecurityRequirements: b.sec.Requirements(),
		DefaultInputModes:  []string{"application/json", "text/plain"},
		DefaultOutputModes: []string{"application/json", "text/plain"},
		XIdentity:          xIdentity{ANS: xANS{URI: ANSName(c), TrustCard: b.url(PathTrustCard), TransparencyLog: c.Card.TLAgentURL}},
		XDiscovery:         b.discovery(),
		XSecurityNote:      "This card declares only what the agent enforces: " + b.accessSummary() + ".",
	}
	if c.Role == "station" && c.Pricing.PayTo != "" {
		card.XPayment = &xPayment{Scheme: "exact", PayTo: c.Pricing.PayTo, Network: c.Pricing.Network, Asset: c.Pricing.Asset, Note: c.Pricing.Note}
	}
	if c.Card.OrgName != "" {
		card.Provider = &provider{Organization: c.Card.OrgName, URL: c.Card.OrgURL}
		if c.Card.Tier2On() {
			card.Provider.DID = b.didWeb()
		}
	}
	card.Skills = make([]skill, 0, len(c.Card.Skills))
	for _, s := range c.Card.Skills {
		tags := s.Tags
		if len(tags) == 0 {
			tags = []string{c.Role}
		}
		desc := s.Description
		if desc == "" {
			desc = s.Name
		}
		card.Skills = append(card.Skills, skill{ID: s.ID, Name: s.Name, Description: desc, Tags: tags,
			Examples: s.Examples, SecurityRequirements: b.sec.SkillRequirements(s.ID)})
	}
	return card
}

// x402USDCBaseSepolia is the USDC contract the x402 challenge names (the
// same value the supplier-conformance surface serves in `accepts[].asset`).
const x402USDCBaseSepolia = "0x036CbD53842c5426634e7929541eC2318f3dCF7e"

// extensions lists the trust-stack extension and, only when the station
// actually mounts it, the supplier-conformance MCP surface (rule 6).
func (b builder) extensions() []extension {
	exts := []extension{b.trustStackExt()}
	if b.c.Role == "station" && b.c.Supplier.Enabled {
		exts = append(exts, extension{
			URI:         "https://overpass.blacksburgbytes.club/ext/supplier-mcp/v1",
			Description: "Supplier-shaped MCP surface for the AP2 fraud battery: get_quote (x402-challenged; the fee is never settled) and book_flight (AP2 mandate + DPoP verified against the Spending Authority's published keys; no on-chain settlement, a valid booking returns PAYMENT_REQUIRED).",
			Params: map[string]any{
				"endpoint": b.c.PublicURL + "/mcp/", "transport": "streamable-http", "protocolVersion": "2025-03-26",
				"tools":         []string{"get_quote", "book_flight"},
				"authorityHost": b.c.Supplier.AuthorityHost,
				// payTo attested in the signed card, both flat and under x402,
				// so a verifier finds it whichever level it reads.
				"payTo": b.c.Pricing.PayTo, "network": "eip155:84532", "asset": x402USDCBaseSepolia, "scheme": "exact",
				"x402": map[string]any{"scheme": "exact", "network": "eip155:84532", "payTo": b.c.Pricing.PayTo, "asset": x402USDCBaseSepolia, "settlement": "none"},
			},
		})
	}
	return exts
}

// trustStackExt declares only the identity anchors this agent serves.
func (b builder) trustStackExt() extension {
	anchors := []string{"ans-x509"}
	params := map[string]any{"trustCard": b.url(PathTrustCard)}
	if b.c.Card.Tier2On() {
		anchors = append(anchors, "did:web")
		params["ard"] = b.url(PathARD)
	}
	if b.c.Card.DNSAID {
		anchors = append(anchors, "dns-aid")
	}
	params["identityAnchors"] = anchors
	return extension{URI: TrustStackExt, Params: params,
		Description: "ANS Trust Card (x5c chain" + map[bool]string{true: " + stapled SCITT receipt", false: ""}[b.receipt != ""] + ")."}
}

func (b builder) discovery() xDiscovery {
	c := b.c
	d := xDiscovery{ANSName: ANSName(c), TLBadge: c.Card.TLAgentURL}
	if c.Card.AgentID != "" {
		d.ANSRegistered = registryEnv(c.RegistryURL)
		d.TrustIndex = &trustIndex{ScoreURL: c.RegistryURL + "/v1/ans/registered-agents?query=" + url.QueryEscape(c.Host),
			ScoreField: "scores.trustScore", Auth: "sso-key"}
	}
	if c.Card.DNSAID {
		d.DNSAIDSVCB = c.Host + " IN SVCB 1 . alpn=a2a,h2"
	}
	return d
}

func registryEnv(u string) string {
	switch {
	case strings.Contains(u, "api.godaddy.com"):
		return "prod"
	case strings.Contains(u, "ote-godaddy.com"):
		return "ote"
	}
	return "local"
}

// TrustFetcher returns the body at a trust-card URL.
type TrustFetcher func(url string) ([]byte, error)

// maxTrustCardBytes caps a fetched trust card.
const maxTrustCardBytes = 64 << 10

// Expect is what the verifier already trusts about the agent, from ANS: its
// verified host and, when known, the SHA-256 (hex) of the identity cert the
// transparency log attests. Phase 3 fills these from the registry and log.
type Expect struct {
	Host        string   // ANS-verified agentHost (host[:port]); required
	LeafSHA256s []string // attested identity cert fingerprints (hex); empty skips the check
}

// VerifyCard checks a served agent card's signature using only the key found,
// by kid, in the trust card at the signature's jku (master plan 6.11):
//   - the card's url and the jku must both be on exp.Host, and the jku must be
//     exactly https://<host>/.well-known/ans/trust-card.json; anything else is
//     rejected before a fetch
//   - each trust-card key must be the public key of its own x5c leaf, and that
//     leaf must be one of exp.LeafSHA256s when any are given
//
// Returns CARD_* codes and never panics.
func VerifyCard(raw []byte, exp Expect, fetch TrustFetcher) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errs.New(errs.CardParseError, "malformed card")
		}
	}()
	if err := jose.StrictJSON(raw, schema.MaxObjectBytes); err != nil {
		return errs.New(errs.CardParseError, err.Error())
	}
	var card map[string]json.RawMessage
	if err := json.Unmarshal(raw, &card); err != nil {
		return errs.New(errs.CardParseError, "card is not an object")
	}
	var sigs []signature
	if err := json.Unmarshal(card["signatures"], &sigs); err != nil || len(sigs) == 0 {
		return errs.New(errs.CardParseError, "card has no signatures")
	}
	var cardURL string
	_ = json.Unmarshal(card["url"], &cardURL)
	delete(card, "signatures")
	payload, err := jose.Canonicalize(card)
	if err != nil {
		return errs.New(errs.CardParseError, err.Error())
	}
	jku, err := protectedJKU(sigs[0].Protected)
	if err != nil {
		return err
	}
	if err := pinJKU(jku, cardURL, exp.Host); err != nil {
		return err
	}
	body, err := fetch(jku)
	if err != nil {
		return errs.New(errs.CardRejectedJKU, "trust card fetch failed: "+err.Error())
	}
	keys, err := trustCardKeys(body, sigs[0].Header["kid"], exp.LeafSHA256s)
	if err != nil {
		return err
	}
	_, err = jose.VerifyDetached(sigs[0].Protected+".."+sigs[0].Signature, payload, schema.CardProfile, keys, nil)
	return err
}

func protectedJKU(protected string) (string, error) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(protected)
	if err != nil {
		return "", errs.New(errs.CardParseError, "protected header is not base64url")
	}
	var h jose.Header
	if err := json.Unmarshal(raw, &h); err != nil || h.JKU == "" {
		return "", errs.New(errs.CardParseError, "protected header has no jku")
	}
	return h.JKU, nil
}

// attestedLeaf reports whether SHA-256(der) is in fps (hex, case-insensitive,
// optional "SHA256:" prefix as transparency logs write it).
func attestedLeaf(der []byte, fps []string) bool {
	sum := sha256.Sum256(der)
	got := hex.EncodeToString(sum[:])
	for _, fp := range fps {
		if strings.EqualFold(strings.TrimPrefix(strings.TrimPrefix(fp, "SHA256:"), "sha256:"), got) {
			return true
		}
	}
	return false
}

// pinJKU accepts only https://<trusted host>/.well-known/ans/trust-card.json,
// and only for a card whose own url is https on that host. Hosts compare
// case-insensitively; paths compare in escaped form, so %2F tricks fail.
func pinJKU(jku, cardURL, host string) error {
	reject := func(why string) error {
		return errs.New(errs.CardRejectedJKU, fmt.Sprintf("jku %q rejected: %s", jku, why))
	}
	host = strings.ToLower(host)
	if host == "" {
		return reject("no trusted host to pin to")
	}
	c, err := url.Parse(cardURL)
	if err != nil || c.Scheme != "https" || strings.ToLower(c.Host) != host || c.User != nil {
		return reject("card url is not https on the verified host")
	}
	j, err := url.Parse(jku)
	if err != nil || j.Scheme != "https" || strings.ToLower(j.Host) != host || j.EscapedPath() != PathTrustCard ||
		j.RawQuery != "" || j.ForceQuery || j.Fragment != "" || j.User != nil || j.Opaque != "" {
		return reject("not the verified host's trust card")
	}
	return nil
}

// trustCardKeys returns the EC P-256 keys of a trust card whose JWK equals
// its own x5c leaf key (and whose leaf is one of attested, if any are given).
func trustCardKeys(body []byte, kid string, attested []string) ([]*ecdsa.PublicKey, error) {
	if err := jose.StrictJSON(body, maxTrustCardBytes); err != nil {
		return nil, errs.New(errs.CardRejectedJKU, "trust card: "+err.Error())
	}
	var tc struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(body, &tc); err != nil {
		return nil, errs.New(errs.CardRejectedJKU, "trust card is not an object")
	}
	var keys []*ecdsa.PublicKey
	for _, k := range tc.Keys {
		var jwk gojose.JSONWebKey
		if jwk.UnmarshalJSON(k) != nil || len(jwk.Certificates) == 0 || (kid != "" && jwk.KeyID != kid) {
			continue
		}
		pub, ok := jwk.Key.(*ecdsa.PublicKey)
		leaf, lok := jwk.Certificates[0].PublicKey.(*ecdsa.PublicKey)
		if !ok || !lok || !pub.Equal(leaf) {
			continue
		}
		if len(attested) > 0 && !attestedLeaf(jwk.Certificates[0].Raw, attested) {
			continue
		}
		keys = append(keys, pub)
	}
	if len(keys) == 0 {
		return nil, errs.New(errs.CardRejectedSignature, "trust card lists no EC key bound to an attested x5c leaf")
	}
	return keys, nil
}
