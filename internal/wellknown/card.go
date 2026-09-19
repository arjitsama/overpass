package wellknown

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
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
	Signatures           []signature           `json:"signatures,omitempty"`
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
	tok, err := jose.SignDetached(jose.TypAgentCard, payload, b.id.Key, b.url(PathTrustCard))
	if err != nil {
		return err
	}
	parts := strings.Split(tok, "..")
	card.Signatures = []signature{{Protected: parts[0], Signature: parts[1], Header: map[string]string{"kid": b.kid}}}
	return putJSON(files, PathCard, card)
}

func (b builder) unsignedCard() agentCard {
	c := b.c
	card := agentCard{
		Name: c.Card.DisplayName, URL: c.PublicURL, Description: b.description(), Version: c.Card.Version,
		ProtocolVersion: A2AProtocolVersion, DocumentationURL: c.PublicURL,
		SupportedInterfaces: []iface{{URL: c.PublicURL, ProtocolBinding: "JSONRPC", ProtocolVersion: A2AProtocolVersion}},
		Capabilities:        capabilities{Extensions: []extension{b.trustStackExt()}},
		SecuritySchemes:     b.sec.Schemes(), SecurityRequirements: b.sec.Requirements(),
		DefaultInputModes:  []string{"application/json", "text/plain"},
		DefaultOutputModes: []string{"application/json", "text/plain"},
		XIdentity:          xIdentity{ANS: xANS{URI: ANSName(c), TrustCard: b.url(PathTrustCard), TransparencyLog: c.Card.TLAgentURL}},
		XDiscovery:         b.discovery(),
		XSecurityNote:      "This card declares only what the agent enforces: " + b.accessSummary() + ".",
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
	Host       string // ANS-verified agentHost (host[:port]); required
	LeafSHA256 string // attested identity cert fingerprint; empty skips the check
}

// VerifyCard checks a served agent card's signature using only the key found,
// by kid, in the trust card at the signature's jku (master plan 6.11):
//   - the card's url and the jku must both be on exp.Host, and the jku must be
//     exactly https://<host>/.well-known/ans/trust-card.json; anything else is
//     rejected before a fetch
//   - each trust-card key must be the public key of its own x5c leaf, and that
//     leaf must match exp.LeafSHA256 when given
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
	keys, err := trustCardKeys(body, exp.LeafSHA256)
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
// its own x5c leaf key (and whose leaf matches leafSHA256, if given).
func trustCardKeys(body []byte, leafSHA256 string) ([]*ecdsa.PublicKey, error) {
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
		if jwk.UnmarshalJSON(k) != nil || len(jwk.Certificates) == 0 {
			continue
		}
		pub, ok := jwk.Key.(*ecdsa.PublicKey)
		leaf, lok := jwk.Certificates[0].PublicKey.(*ecdsa.PublicKey)
		if !ok || !lok || !pub.Equal(leaf) {
			continue
		}
		if leafSHA256 != "" {
			sum := sha256.Sum256(jwk.Certificates[0].Raw)
			if hex.EncodeToString(sum[:]) != strings.ToLower(leafSHA256) {
				continue
			}
		}
		keys = append(keys, pub)
	}
	if len(keys) == 0 {
		return nil, errs.New(errs.CardRejectedSignature, "trust card lists no EC key bound to an attested x5c leaf")
	}
	return keys, nil
}
