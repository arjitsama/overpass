package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arjitsama/overpass/internal/errs"
)

const validYAML = `
role: station
host: gs-blacksburg.localhost
port: 8444
cert:
  cert_file: certs/gs/cert.pem
  key_file: certs/gs/key.pem
peers:
  - name: ops
    url: https://localhost:8443
trust_roots: [certs/roots.pem]
`

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func wantCode(t *testing.T, err error, code errs.Code) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

func TestLoadValid(t *testing.T) {
	c, err := Load(writeFile(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if c.Role != "station" || c.Port != 8444 || c.Host != "gs-blacksburg.localhost" {
		t.Fatalf("got %+v", c)
	}
	if len(c.Peers) != 1 || c.Peers[0].Name != "ops" || c.Cert.KeyFile != "certs/gs/key.pem" {
		t.Fatalf("got %+v", c)
	}
	if c.RegistryURL != DefaultRegistryURL || c.LogURL != DefaultLogURL {
		t.Fatalf("defaults not applied: %+v", c)
	}
	if len(c.Sites) != 3 || c.Sites[1].Name != "Svalbard" || c.Satellite.NoradID != 27844 {
		t.Fatalf("site defaults: %+v %+v", c.Sites, c.Satellite)
	}
	if c.PublicURL != "https://gs-blacksburg.localhost:8444" || c.Card.Version != "0.1.0" || !c.Card.Tier2On() {
		t.Fatalf("card defaults: %+v", c)
	}
	r := Config{Role: "station", Host: "gs.example", Port: 443, Rogue: true, RogueAck: RogueAck}
	r.ApplyDefaults()
	if err := r.Validate(); err != nil {
		t.Fatalf("acknowledged rogue station: %v", err)
	}
	d := Config{Role: "ops", Host: "ops.example", Port: 443, PublicURL: "https://ops.example/"}
	d.ApplyDefaults()
	if d.PublicURL != "https://ops.example" || d.Validate() != nil {
		t.Fatalf("trailing slash: %q %v", d.PublicURL, d.Validate())
	}
	e := Config{Role: "ops", Host: "ops.example", Port: 443}
	e.ApplyDefaults()
	if e.PublicURL != "https://ops.example" {
		t.Fatalf("port 443 default: %q", e.PublicURL)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	_, err := Load(writeFile(t, validYAML+"surprise: 1\n"))
	wantCode(t, err, errs.BadRequest)
}

func TestLoadRejectsSecondDocument(t *testing.T) {
	_, err := Load(writeFile(t, validYAML+"---\nrole: ops\n"))
	wantCode(t, err, errs.BadRequest)
}

func TestLoadRejectsOversize(t *testing.T) {
	_, err := Load(writeFile(t, validYAML+"#"+strings.Repeat("x", MaxFileBytes)+"\n"))
	wantCode(t, err, errs.PayloadTooLarge)
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	wantCode(t, err, errs.BadRequest)
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("OVERPASS_ROLE", "ops")
	t.Setenv("OVERPASS_PORT", "9000")
	t.Setenv("OVERPASS_LOG_URL", "https://log.example")
	c, err := Load(writeFile(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if c.Role != "ops" || c.Port != 9000 || c.LogURL != "https://log.example" {
		t.Fatalf("overrides not applied: %+v", c)
	}
}

func TestEnvBadPort(t *testing.T) {
	t.Setenv("OVERPASS_PORT", "eighty")
	_, err := Load(writeFile(t, validYAML))
	wantCode(t, err, errs.BadRequest)
}

func TestValidate(t *testing.T) {
	base := Config{Role: "ops", Port: 8443}
	base.ApplyDefaults()
	cases := map[string]func(*Config){
		"bad role":          func(c *Config) { c.Role = "rogue" },
		"port zero":         func(c *Config) { c.Port = 0 },
		"port too big":      func(c *Config) { c.Port = 70000 },
		"cert without key":  func(c *Config) { c.Cert.CertFile = "a.pem" },
		"http registry":     func(c *Config) { c.RegistryURL = "http://api.godaddy.com" },
		"peer no name":      func(c *Config) { c.Peers = []Peer{{URL: "https://x"}} },
		"peer bad url":      func(c *Config) { c.Peers = []Peer{{Name: "x", URL: "::"}} },
		"identity half":     func(c *Config) { c.Identity.KeyFile = "k.pem" },
		"bad version":       func(c *Config) { c.Card.Version = "1.0" },
		"dup skill":         func(c *Config) { c.Card.Skills = []Skill{{ID: "a", Name: "A"}, {ID: "a", Name: "B"}} },
		"http public url":   func(c *Config) { c.PublicURL = "http://x" },
		"bad site lat":      func(c *Config) { c.Sites = []Site{{Name: "x", LatDeg: 91}} },
		"unnamed site":      func(c *Config) { c.Sites = []Site{{LatDeg: 1}} },
		"dup site host":     func(c *Config) { c.Sites = []Site{{Name: "a", Host: "h"}, {Name: "b", Host: "h"}} },
		"rogue without ack": func(c *Config) { c.Role = "station"; c.Rogue = true },
		"rogue not station": func(c *Config) { c.Rogue = true; c.RogueAck = RogueAck },
		"public url path":   func(c *Config) { c.PublicURL = "https://localhost:8443/a2a" },
		"public url query":  func(c *Config) { c.PublicURL = "https://localhost:8443?x=1" },
		"public url user":   func(c *Config) { c.PublicURL = "https://u@localhost:8443" },
		"public url host":   func(c *Config) { c.PublicURL = "https://evil.example:8443" },
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("base invalid: %v", err)
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			c := base
			mut(&c)
			wantCode(t, c.Validate(), errs.BadRequest)
		})
	}
}
