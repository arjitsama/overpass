package wellknown

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"sort"
	"strings"

	gojose "github.com/go-jose/go-jose/v4"

	"github.com/arjitsama/overpass/internal/a2a"
	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/jose"
)

// Paths served.
const (
	PathCard      = "/.well-known/agent-card.json"
	PathTrustCard = "/.well-known/ans/trust-card.json"
	PathHealth    = "/health"
	PathIndex     = "/"
	PathJWKS      = "/.well-known/jwks.json"
	PathDID       = "/.well-known/did.json"
	PathARD       = "/.well-known/ard.json"
	PathCatalog   = "/.well-known/ai-catalog.json"
	PathRobots    = "/robots.txt"
	PathLLMs      = "/llms.txt"
)

// A2AProtocolVersion is the A2A version the agent speaks (0.3 is also accepted).
const A2AProtocolVersion = "1.0"

// TrustStackExt is the extension URI Webmesh uses for the ANS trust stack
// (docs/webmesh-spec.md 2.1). We declare only the anchors we serve.
const TrustStackExt = "https://webmesh.ai/ext/ans-trust-stack/v1"

// File is one served body.
type File struct {
	Body        []byte
	ContentType string
}

// Files maps a URL path to its body.
type Files map[string]File

// Input is everything Build needs.
type Input struct {
	Config   config.Config
	Identity Identity
	Security a2a.Security // what the server mounts; the card is built from it
}

// ANSName is ans://v<version>.<host>.
func ANSName(c config.Config) string { return "ans://v" + c.Card.Version + "." + c.Host }

// Build generates every file. It is deterministic for a given Input (no
// timestamps), so the card's hash only changes when its content does.
func Build(in Input) (Files, error) {
	c := in.Config
	if in.Identity.Key == nil || len(in.Identity.Chain) == 0 {
		return nil, errors.New("wellknown: identity key and chain required")
	}
	receipt, err := readReceipt(c.Card.ReceiptFile)
	if err != nil {
		return nil, err
	}
	kid, err := jose.Thumbprint(&in.Identity.Key.PublicKey)
	if err != nil {
		return nil, err
	}
	b := builder{c: c, id: in.Identity, sec: in.Security, kid: kid, receipt: receipt}
	files := Files{}
	steps := []func(Files) error{b.card, b.trustCard, b.health, b.index}
	if c.Card.Tier2On() {
		steps = append(steps, b.jwks, b.did, b.ard, b.robots, b.llms)
	}
	for _, step := range steps {
		if err := step(files); err != nil {
			return nil, err
		}
	}
	return files, nil
}

func readReceipt(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("receipt: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

type builder struct {
	c       config.Config
	id      Identity
	sec     a2a.Security
	kid     string
	receipt string
}

func (b builder) url(path string) string { return b.c.PublicURL + path }

func putJSON(files Files, path string, v any) error {
	body, err := jose.Canonicalize(v)
	if err != nil {
		return fmt.Errorf("wellknown %s: %w", path, err)
	}
	files[path] = File{Body: body, ContentType: "application/json"}
	return nil
}

// jwk returns the public JWK, with x5c when withChain is set.
func (b builder) jwk(withChain bool) (json.RawMessage, error) {
	k := gojose.JSONWebKey{Key: &b.id.Key.PublicKey, KeyID: b.kid, Use: "sig"}
	if withChain {
		k.Certificates = b.id.Chain
	}
	return k.MarshalJSON()
}

func (b builder) trustCard(files Files) error {
	key, err := b.jwk(true)
	if err != nil {
		return err
	}
	return putJSON(files, PathTrustCard, trustCard{
		ANSName: ANSName(b.c), AgentDisplayName: b.c.Card.DisplayName, Version: b.c.Card.Version,
		AgentHost: b.c.Host,
		Endpoints: []endpoint{{Protocol: "A2A", AgentURL: b.c.PublicURL, MetaDataURL: b.url(PathCard)}},
		Keys:      []json.RawMessage{key}, AgentID: b.c.Card.AgentID, TransparencyReceipt: b.receipt,
	})
}

type trustCard struct {
	ANSName             string            `json:"ansName"`
	AgentDisplayName    string            `json:"agentDisplayName"`
	Version             string            `json:"version"`
	AgentHost           string            `json:"agentHost"`
	Endpoints           []endpoint        `json:"endpoints"`
	Keys                []json.RawMessage `json:"keys"`
	AgentID             string            `json:"agentId,omitempty"`
	TransparencyReceipt string            `json:"transparencyReceipt,omitempty"`
}

type endpoint struct {
	Protocol    string `json:"protocol"`
	AgentURL    string `json:"agentUrl"`
	MetaDataURL string `json:"metaDataUrl"`
}

func (b builder) health(files Files) error {
	return putJSON(files, PathHealth, map[string]string{
		"status": "ok", "a2aProtocolVersion": A2AProtocolVersion, "role": b.c.Role, "host": b.c.Host,
	})
}

func (b builder) jwks(files Files) error {
	key, err := b.jwk(false)
	if err != nil {
		return err
	}
	return putJSON(files, PathJWKS, map[string]any{"keys": []json.RawMessage{key}})
}

// didWeb is did:web:<host>, with a port percent-encoded as did:web requires.
func (b builder) didWeb() string {
	u, _ := url.Parse(b.c.PublicURL)
	return "did:web:" + strings.ReplaceAll(u.Host, ":", "%3A")
}

func (b builder) did(files Files) error {
	key, err := b.jwk(false)
	if err != nil {
		return err
	}
	did := b.didWeb()
	return putJSON(files, PathDID, map[string]any{
		"@context": []string{"https://www.w3.org/ns/did/v1", "https://w3id.org/security/suites/jws-2020/v1"},
		"id":       did,
		"verificationMethod": []any{map[string]any{
			"id": did + "#ans-key", "type": "JsonWebKey2020", "controller": did, "publicKeyJwk": key,
		}},
		"authentication":  []string{did + "#ans-key"},
		"assertionMethod": []string{did + "#ans-key"},
		"service": []any{map[string]string{
			"id": did + "#a2a", "type": "AgentService", "serviceEndpoint": b.c.PublicURL + "/",
		}},
	})
}

func (b builder) tags() []string {
	set := map[string]bool{"a2a": true}
	for _, s := range b.c.Card.Skills {
		for _, t := range s.Tags {
			set[t] = true
		}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func (b builder) ard(files Files) error {
	name := b.c.Card.DisplayName
	doc := map[string]any{
		"specVersion": "1.0",
		"host": map[string]string{
			"displayName": name, "identifier": b.c.PublicURL, "documentationUrl": b.c.PublicURL,
		},
		"entries": []any{map[string]any{
			"identifier":  "urn:air:" + b.c.Host + ":" + b.c.Role + ":a2a",
			"type":        "application/a2a-agent-card+json",
			"displayName": name + " (A2A)",
			"version":     b.c.Card.Version,
			"url":         b.url(PathCard),
			"description": b.description(),
			"tags":        b.tags(),
			"publisher": map[string]string{
				"identifier": b.url(PathTrustCard), "displayName": name, "identityType": "https",
			},
		}},
		"extensions": map[string]string{"ansName": ANSName(b.c), "trustCardUrl": b.url(PathTrustCard)},
	}
	if err := putJSON(files, PathARD, doc); err != nil {
		return err
	}
	files[PathCatalog] = files[PathARD] // same bytes
	return nil
}

func (b builder) robots(files Files) error {
	body := "User-agent: *\nAllow: /\nAgentmap: " + b.url(PathCatalog) + "\n"
	files[PathRobots] = File{Body: []byte(body), ContentType: "text/plain; charset=utf-8"}
	return nil
}

func (b builder) llms(files Files) error {
	var s strings.Builder
	fmt.Fprintf(&s, "# %s\n\n%s\n\n", b.c.Card.DisplayName, b.description())
	fmt.Fprintf(&s, "- ANS name: %s\n- A2A endpoint: %s (JSON-RPC %s; 0.3 message/send also accepted)\n",
		ANSName(b.c), b.c.PublicURL, A2AProtocolVersion)
	fmt.Fprintf(&s, "- Access: %s\n- Agent card: %s\n- Trust card: %s\n\n## Skills\n",
		b.accessSummary(), b.url(PathCard), b.url(PathTrustCard))
	for _, sk := range b.c.Card.Skills {
		fmt.Fprintf(&s, "- %s: %s. %s\n", sk.ID, sk.Name, sk.Description)
	}
	files[PathLLMs] = File{Body: []byte(s.String()), ContentType: "text/plain; charset=utf-8"}
	return nil
}

func (b builder) description() string {
	if b.c.Card.Description != "" {
		return b.c.Card.Description
	}
	return "Overpass " + b.c.Role + " agent."
}

// accessSummary says in words what the card's securitySchemes say.
func (b builder) accessSummary() string {
	names := make([]string, 0)
	for n := range b.sec.Schemes() {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 1 && names[0] == a2a.NoAuth.Name {
		return "no credential required"
	}
	return "requires " + strings.Join(names, " + ") + " (see securitySchemes in the agent card)"
}

var indexTmpl = template.Must(template.New("index").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Name}}: Overpass {{.Role}} agent</title>
<style>
body{font:16px/1.5 system-ui,sans-serif;max-width:44rem;margin:0 auto;padding:1rem;color:#111;background:#fff}
a{color:#0645ad}a:focus{outline:3px solid #0645ad;outline-offset:2px}
dt{font-weight:600}code{word-break:break-all}
@media (prefers-color-scheme:dark){body{color:#eee;background:#111}a{color:#9cf}a:focus{outline-color:#9cf}}
</style>
</head>
<body>
<header><h1>{{.Name}}</h1><p>{{.Description}}</p></header>
<main>
<h2>Identity</h2>
<dl>
<dt>ANS name</dt><dd><code>{{.ANSName}}</code></dd>
<dt>Role</dt><dd>{{.Role}}</dd>
<dt>Access</dt><dd>{{.Access}}</dd>
<dt>A2A endpoint</dt><dd><code>{{.URL}}</code> (JSON-RPC, POST)</dd>
</dl>
<h2>Skills</h2>
{{if .Skills}}<ul>{{range .Skills}}<li><strong>{{.Name}}</strong> (<code>{{.ID}}</code>): {{.Description}}</li>{{end}}</ul>{{else}}<p>No skills are offered.</p>{{end}}
<h2>Files</h2>
<ul>{{range .Links}}<li><a href="{{.Href}}">{{.Label}}</a></li>{{end}}</ul>
</main>
</body>
</html>
`))

type link struct{ Href, Label string }

func (b builder) index(files Files) error {
	links := []link{{PathCard, "Agent card (signed)"}, {PathTrustCard, "ANS trust card"}, {PathHealth, "Health"}}
	if b.c.Card.Tier2On() {
		links = append(links, link{PathJWKS, "JWKS"}, link{PathDID, "DID document"},
			link{PathCatalog, "AI catalog (ARD)"}, link{PathLLMs, "llms.txt"})
	}
	var buf bytes.Buffer
	err := indexTmpl.Execute(&buf, map[string]any{
		"Name": b.c.Card.DisplayName, "Role": b.c.Role, "Description": b.description(),
		"ANSName": ANSName(b.c), "Access": b.accessSummary(), "URL": b.c.PublicURL,
		"Skills": b.c.Card.Skills, "Links": links,
	})
	if err != nil {
		return err
	}
	files[PathIndex] = File{Body: buf.Bytes(), ContentType: "text/html; charset=utf-8"}
	return nil
}
