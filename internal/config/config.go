// Package config loads one YAML file per agent and applies environment
// overrides. Secrets never live in these files; only paths to them do.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"

	"go.yaml.in/yaml/v3"

	"github.com/arjitsama/overpass/internal/errs"
)

// MaxFileBytes caps the size of a config file.
const MaxFileBytes = 64 << 10

// Default registry and transparency log (production, see prompts section 1).
const (
	DefaultRegistryURL = "https://api.godaddy.com"
	DefaultLogURL      = "https://transparency.ans.godaddy.com"
)

// Roles accepted by --role.
var Roles = []string{"ops", "authority", "station", "auditor", "spacecraft"}

// Cert holds paths to the agent's TLS certificate and key.
type Cert struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// Peer is another agent this one talks to.
type Peer struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
}

// Config is one agent's configuration.
type Config struct {
	Role        string   `yaml:"role"`
	Host        string   `yaml:"host"`
	Port        int      `yaml:"port"`
	Cert        Cert     `yaml:"cert"`
	Peers       []Peer   `yaml:"peers"`
	TrustRoots  []string `yaml:"trust_roots"`
	RegistryURL string   `yaml:"registry_url"`
	LogURL      string   `yaml:"log_url"`
}

// Load reads path, applies env overrides and defaults, and validates.
func Load(path string) (Config, error) {
	raw, err := readCapped(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, errs.New(errs.BadRequest, fmt.Sprintf("config %s: %v", path, err))
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Config{}, errs.New(errs.BadRequest, fmt.Sprintf("config %s: exactly one YAML document allowed", path))
	}
	if err := c.applyEnv(os.LookupEnv); err != nil {
		return Config{}, err
	}
	c.applyDefaults()
	return c, c.Validate()
}

func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errs.New(errs.BadRequest, fmt.Sprintf("config: %v", err))
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, errs.New(errs.BadRequest, fmt.Sprintf("config: %v", err))
	}
	if len(raw) > MaxFileBytes {
		return nil, errs.New(errs.PayloadTooLarge, fmt.Sprintf("config %s exceeds %d bytes", path, MaxFileBytes))
	}
	return raw, nil
}

// applyEnv overrides fields from OVERPASS_* variables.
func (c *Config) applyEnv(lookup func(string) (string, bool)) error {
	strs := map[string]*string{
		"OVERPASS_ROLE":         &c.Role,
		"OVERPASS_HOST":         &c.Host,
		"OVERPASS_CERT_FILE":    &c.Cert.CertFile,
		"OVERPASS_KEY_FILE":     &c.Cert.KeyFile,
		"OVERPASS_REGISTRY_URL": &c.RegistryURL,
		"OVERPASS_LOG_URL":      &c.LogURL,
	}
	for k, dst := range strs {
		if v, ok := lookup(k); ok {
			*dst = v
		}
	}
	if v, ok := lookup("OVERPASS_PORT"); ok {
		p, err := strconv.Atoi(v)
		if err != nil {
			return errs.New(errs.BadRequest, "OVERPASS_PORT is not an integer")
		}
		c.Port = p
	}
	return nil
}

func (c *Config) applyDefaults() {
	if c.RegistryURL == "" {
		c.RegistryURL = DefaultRegistryURL
	}
	if c.LogURL == "" {
		c.LogURL = DefaultLogURL
	}
	if c.Host == "" {
		c.Host = "localhost"
	}
}

// Validate reports the first invalid field as a bad_request error.
func (c Config) Validate() error {
	if !validRole(c.Role) {
		return errs.New(errs.BadRequest, fmt.Sprintf("role %q is not one of %v", c.Role, Roles))
	}
	if c.Port < 1 || c.Port > 65535 {
		return errs.New(errs.BadRequest, fmt.Sprintf("port %d out of range 1-65535", c.Port))
	}
	if (c.Cert.CertFile == "") != (c.Cert.KeyFile == "") {
		return errs.New(errs.BadRequest, "cert_file and key_file must both be set or both be empty")
	}
	for _, u := range []string{c.RegistryURL, c.LogURL} {
		if err := checkHTTPSURL(u); err != nil {
			return err
		}
	}
	for _, p := range c.Peers {
		if p.Name == "" {
			return errs.New(errs.BadRequest, "peer name is empty")
		}
		if err := checkHTTPSURL(p.URL); err != nil {
			return err
		}
	}
	return nil
}

func validRole(r string) bool {
	for _, v := range Roles {
		if r == v {
			return true
		}
	}
	return false
}

func checkHTTPSURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errs.New(errs.BadRequest, fmt.Sprintf("url %q must be an absolute https URL", s))
	}
	return nil
}
