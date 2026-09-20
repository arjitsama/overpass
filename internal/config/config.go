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
	"strings"
	"time"

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
	URL  string `yaml:"url"`  // https://<host>[:port], the peer's public URL
	Env  string `yaml:"env"`  // key into environments; empty = "prod"
	Dial string `yaml:"dial"` // optional host:port to connect to instead (local runs)
}

// Environment is one ANS deployment: production, or a local reference stack.
// One peer list may mix environments.
type Environment struct {
	RegistryURL  string   `yaml:"registry_url"`
	LogURL       string   `yaml:"log_url"`        // where the transparency log is reached
	LogPublicURL string   `yaml:"log_public_url"` // what badges advertise, if different (local TL is plain HTTP)
	FinderURL    string   `yaml:"finder_url"`     // ANS Finder base (.../v1); empty = no tag search
	DNSServer    string   `yaml:"dns_server"`     // host:port for badge/TLSA lookups; empty = system
	RootKeys     []string `yaml:"root_keys"`      // C2SP strings from the log's /root-keys
	InsecureHTTP bool     `yaml:"insecure_http"`  // allow http:// URLs (local stack only)
}

// Site is a ground station location for pass prediction.
type Site struct {
	Name   string  `yaml:"name"`
	Host   string  `yaml:"host"`
	LatDeg float64 `yaml:"lat"`
	LonDeg float64 `yaml:"lon"`
	AltM   float64 `yaml:"alt_m"`
}

// Satellite is the spacecraft Overpass plans for, with its cached TLE.
type Satellite struct {
	NoradID int64  `yaml:"norad_id"`
	TLEFile string `yaml:"tle_file"`
}

// DefaultSites are the three stations of master plan 7.2.
var DefaultSites = []Site{
	{Name: "Blacksburg", Host: "gs-blacksburg", LatDeg: 37.23, LonDeg: -80.42, AltM: 634},
	{Name: "Svalbard", Host: "gs-svalbard", LatDeg: 78.23, LonDeg: 15.41, AltM: 450},
	{Name: "Awarua", Host: "gs-awarua", LatDeg: -46.53, LonDeg: 168.38, AltM: 10},
}

// Pricing is how a station prices a pass and where it is paid (x402 shape).
type Pricing struct {
	PerMinuteCents int64  `yaml:"per_minute_cents"`
	PayTo          string `yaml:"pay_to"`
	Network        string `yaml:"network"`
	Asset          string `yaml:"asset"`
	AssetDecimals  int64  `yaml:"asset_decimals"` // accepts.amount = cents x 10^(decimals-2)
	Note           string `yaml:"note"`           // plain-text note carried in the card's x-payment (e.g. "simulated; no settlement is performed")
}

// StaticFile is one extra file served at Path from File with ContentType.
type StaticFile struct {
	Path        string `yaml:"path"`
	File        string `yaml:"file"`
	ContentType string `yaml:"content_type"`
}

// SupplierCfg mounts the supplier-conformance MCP surface (/mcp/) on a
// station: get_quote (x402-challenged) and book_flight (AP2 mandate + DPoP),
// verified against the Spending Authority's keys pinned by host. It is a
// separate surface from the Overpass A2A flow and touches none of it.
type SupplierCfg struct {
	Enabled       bool   `yaml:"enabled"`
	AuthorityHost string `yaml:"authority_host"` // e.g. authority.webmesh.ai (keys fetched from its trust card / jwks)
	AuthorityANS  string `yaml:"authority_ans"`  // expected issuer ANS name, if the mandate names one
}

// SatReg is the station's signed satellite registry and its pinned signer.
type SatReg struct {
	File          string `yaml:"file"`
	SignerKeyFile string `yaml:"signer_key_file"` // PEM public key
}

// AuthorityKey pins an authority's mandate-signing public key (PEM).
type AuthorityKey struct {
	ANSName string `yaml:"ans_name"`
	KeyFile string `yaml:"key_file"`
}

// TrustIndexCfg points an authority or auditor at the trust index. AdminKeyEnv
// names the environment variable holding the bearer key for /v1/internal/*
// (never the key itself: hard rule 2). An empty URL means "no index configured":
// the authority then falls back to trust_tiers and the auditor to a log sink.
type TrustIndexCfg struct {
	URL         string            `yaml:"url"`
	AdminKeyEnv string            `yaml:"admin_key_env"`
	Agents      map[string]string `yaml:"agents"` // station host -> index agentId
}

// FlightRules is the authority's policy (master plan 8.3).
type FlightRules struct {
	Stations        []string            `yaml:"stations"` // allowed station hosts; empty = any meeting min_tier
	MinTier         string              `yaml:"min_tier"`
	CommandClasses  map[string][]string `yaml:"command_classes"` // mode -> allowed classes
	MaxCentsPerPass int64               `yaml:"max_cents_per_pass"`
	MaxPassesPerDay int                 `yaml:"max_passes_per_day"`

	// OperatorAllow is the flight-rules "stations named explicitly" branch
	// (master plan 8.3): mode -> station hosts the OPERATOR authorizes directly.
	// A named station is granted that mode by operator policy, bypassing the
	// trust-vector tier gate (all other rules — classes, amount, daily limit,
	// window — still apply). It is an operator allow-list, NEVER a trust score:
	// the authority labels it "operator allow-list", not FIDUCIARY. Empty = none.
	OperatorAllow map[string][]string `yaml:"operator_allow"`

	// Overpass access-tier thresholds, computed from the truthful trust vector
	// (master plan §11, revised). Zero values fall back to the documented
	// defaults below, so an operator sets only what they want to change.
	DownlinkMinIntegrity int    `yaml:"downlink_min_integrity"` // default 50
	UplinkMinIntegrity   int    `yaml:"uplink_min_integrity"`   // default 80
	UplinkMinBehavior    int    `yaml:"uplink_min_behavior"`    // default 80
	UplinkMinPasses      int    `yaml:"uplink_min_passes"`      // default 3
	MinCertType          string `yaml:"min_cert_type"`          // default "DV"; production would set "OV"/"EV"
}

// Threshold accessors apply the documented defaults for an unset (zero) field.
func (r FlightRules) DownlinkIntegrity() int {
	if r.DownlinkMinIntegrity > 0 {
		return r.DownlinkMinIntegrity
	}
	return 50
}

func (r FlightRules) UplinkIntegrity() int {
	if r.UplinkMinIntegrity > 0 {
		return r.UplinkMinIntegrity
	}
	return 80
}

func (r FlightRules) UplinkBehavior() int {
	if r.UplinkMinBehavior > 0 {
		return r.UplinkMinBehavior
	}
	return 80
}

func (r FlightRules) UplinkPasses() int {
	if r.UplinkMinPasses > 0 {
		return r.UplinkMinPasses
	}
	return 3
}

func (r FlightRules) CertTypeFloor() string {
	if r.MinCertType != "" {
		return r.MinCertType
	}
	return "DV"
}

// SpacecraftCfg configures the simulated spacecraft role.
type SpacecraftCfg struct {
	NoradID    int64  `yaml:"norad_id"`
	OpsKeyFile string `yaml:"ops_key_file"` // PEM public key of the Ops identity key
}

// SessionCfg configures a station's pass sessions.
type SessionCfg struct {
	SpacecraftURL string   `yaml:"spacecraft_url"` // A2A URL of the spacecraft (the RF link)
	SpacecraftCA  string   `yaml:"spacecraft_ca"`  // PEM cert to trust for it (local self-signed runs)
	OpsEnv        string   `yaml:"ops_env"`        // environment of the Ops agents' tokens (default prod)
	Auditors      []string `yaml:"auditors"`       // ANS names that may read any session's evidence
}

// AuditorCfg configures the auditor role.
type AuditorCfg struct {
	StationKeys []AuthorityKey `yaml:"station_keys"` // ans_name -> identity public key (to verify receipts)
	Env         string         `yaml:"env"`          // environment for VerifyPeer (default prod)
}

// MaxPerMinuteCents bounds a station's price ($10,000 a minute).
const MaxPerMinuteCents = 1_000_000

// RogueAck must accompany rogue: true, so a rogue station is never an accident.
const RogueAck = "i-am-the-rogue-station"

// ProdEnv is the environment name used when a peer names none.
const ProdEnv = "prod"

// Identity holds paths to the ANS identity key (EC P-256, PEM) and its
// certificate chain (PEM, leaf first). Empty means a throwaway local identity.
type Identity struct {
	KeyFile   string `yaml:"key_file"`
	ChainFile string `yaml:"chain_file"`
}

// Skill is one A2A skill as the card lists it.
type Skill struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Tags        []string `yaml:"tags"`
	Examples    []string `yaml:"examples"`
}

// Card is what the well-known files say about the agent. Anything empty is
// left out of the files rather than faked.
type Card struct {
	Version     string  `yaml:"version"`
	DisplayName string  `yaml:"display_name"`
	Description string  `yaml:"description"`
	OrgName     string  `yaml:"org_name"`
	OrgURL      string  `yaml:"org_url"`
	AgentID     string  `yaml:"agent_id"`
	ReceiptFile string  `yaml:"receipt_file"` // base64 SCITT receipt from the log
	TLAgentURL  string  `yaml:"tl_agent_url"` // transparency log URL for this agent
	DNSAID      bool    `yaml:"dns_aid"`      // set only once the SVCB record exists
	Tier2       *bool   `yaml:"tier2"`        // default true
	SignedFile  string  `yaml:"signed_file"`  // persist the signed card here so its bytes (and metaDataHash) survive restarts
	Skills      []Skill `yaml:"skills"`
}

// Tier2On reports whether tier-2 files are served (default true).
func (c Card) Tier2On() bool { return c.Tier2 == nil || *c.Tier2 }

// Config is one agent's configuration.
type Config struct {
	Role      string    `yaml:"role"`
	Host      string    `yaml:"host"`
	Port      int       `yaml:"port"`
	PublicURL string    `yaml:"public_url"`
	Cert      Cert      `yaml:"cert"`
	Identity  Identity  `yaml:"identity"`
	Card      Card      `yaml:"card"`
	Peers     []Peer    `yaml:"peers"`
	Sites     []Site    `yaml:"sites"`
	Satellite Satellite `yaml:"satellite"`

	DBPath        string            `yaml:"db_path"`
	Pricing       Pricing           `yaml:"pricing"`
	SatReg        SatReg            `yaml:"satreg"`
	AuthorityKeys []AuthorityKey    `yaml:"authority_keys"`
	OpsAgents     []string          `yaml:"ops_agents"`  // ANS names allowed to request mandates
	TrustTiers    map[string]string `yaml:"trust_tiers"` // host -> tier; fallback when no trust_index
	TrustIndex    TrustIndexCfg     `yaml:"trust_index"`
	FlightRules   FlightRules       `yaml:"flight_rules"`
	Auditor       AuditorCfg        `yaml:"auditor"`
	Spacecraft    SpacecraftCfg     `yaml:"spacecraft"`
	Session       SessionCfg        `yaml:"session"`
	Supplier      SupplierCfg       `yaml:"supplier"`
	// WellKnownFiles serves extra static files (e.g. /.well-known/jwks.json for
	// AP2 request signing) verbatim; they are not part of the signed card.
	WellKnownFiles []StaticFile           `yaml:"well_known_files"`
	Rogue          bool                   `yaml:"rogue"`
	RogueAck       string                 `yaml:"rogue_ack"`
	TestControls   bool                   `yaml:"test_controls"` // enables the demo-only /control/compromise route
	Environments   map[string]Environment `yaml:"environments"`
	TrustRoots     []string               `yaml:"trust_roots"` // C2SP key strings, as served at the log's /root-keys
	RegistryURL    string                 `yaml:"registry_url"`
	LogURL         string                 `yaml:"log_url"`
	UI             UICfg                  `yaml:"ui"`
}

// UICfg configures the Ops dashboard's routes (master plan §12).
type UICfg struct {
	WebmeshURL string `yaml:"webmesh_url"` // GoDaddy's agent MCP endpoint for verify-station
	VerifyHost string `yaml:"verify_host"` // default station FQDN the verify button checks

	// Agents are the peers the dashboard verifies live, in display order.
	Agents []UIAgent `yaml:"agents"`
	// RefreshEvery is how often the live verification poller re-runs.
	// Zero means DefaultAgentRefresh.
	RefreshEvery time.Duration `yaml:"refresh_every"`
	// AccessBasis states, in plain language, what actually grants uplink. It is
	// the authority's flight rules, not a trust score.
	AccessBasis string `yaml:"access_basis"`
	// TrustIndexNote is shown wherever scores would otherwise be. Production
	// runs no trust index, so the dashboard says so rather than showing a tier.
	TrustIndexNote string `yaml:"trust_index_note"`
	// BatteryLiveFile is the JSON record of the last live battery run
	// (bin/battery -record). Absent means no live run has been recorded.
	BatteryLiveFile string `yaml:"battery_live_file"`
	// FraudRedteamFile is docs/status/fraud-redteam.md. GoDaddy fraud-agent
	// results are shown from this file only.
	FraudRedteamFile string `yaml:"fraud_redteam_file"`
}

// DefaultAgentRefresh matches the verifier's status-token max age, so tokens
// are refreshed exactly as they go stale.
const DefaultAgentRefresh = 10 * time.Minute

// UIAgent is one agent shown on the dashboard. Role is display text; the
// dashboard never infers a role from a hostname. Deployed false means the agent
// is registered but not running in production: it is labelled, never verified.
type UIAgent struct {
	Host     string `yaml:"host"`
	Role     string `yaml:"role"`
	Deployed *bool  `yaml:"deployed"`
}

// IsDeployed reports whether the agent should be verified live (default true).
func (a UIAgent) IsDeployed() bool { return a.Deployed == nil || *a.Deployed }

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
	c.ApplyDefaults()
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

// ApplyDefaults fills unset fields with their defaults.
func (c *Config) ApplyDefaults() {
	if c.RegistryURL == "" {
		c.RegistryURL = DefaultRegistryURL
	}
	if c.LogURL == "" {
		c.LogURL = DefaultLogURL
	}
	if c.Host == "" {
		c.Host = "localhost"
	}
	c.PublicURL = strings.TrimSuffix(c.PublicURL, "/")
	if c.PublicURL == "" {
		c.PublicURL = "https://" + c.Host
		if c.Port != 443 {
			c.PublicURL += ":" + strconv.Itoa(c.Port)
		}
	}
	if c.Card.Version == "" {
		c.Card.Version = "0.1.0"
	}
	if c.Card.DisplayName == "" {
		c.Card.DisplayName = c.Host
	}
	if len(c.Sites) == 0 {
		c.Sites = append([]Site(nil), DefaultSites...)
	}
	if c.Satellite.NoradID == 0 {
		c.Satellite = Satellite{NoradID: 27844, TLEFile: "data/27844.tle"}
	}
	if c.DBPath == "" {
		c.DBPath = "data/" + c.Host + "-" + strconv.Itoa(c.Port) + ".db"
	}
	if c.Pricing.AssetDecimals == 0 {
		c.Pricing.AssetDecimals = 6
	}
	if c.Environments == nil {
		c.Environments = map[string]Environment{}
	}
	if _, ok := c.Environments[ProdEnv]; !ok {
		c.Environments[ProdEnv] = Environment{RegistryURL: c.RegistryURL, LogURL: c.LogURL, RootKeys: c.TrustRoots}
	}
	if c.UI.RefreshEvery <= 0 {
		c.UI.RefreshEvery = DefaultAgentRefresh
	}
	if c.UI.AccessBasis == "" {
		c.UI.AccessBasis = "Uplink: operator allow-list (flight rules)"
	}
	if c.UI.TrustIndexNote == "" {
		c.UI.TrustIndexNote = "not deployed"
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
	if (c.Identity.KeyFile == "") != (c.Identity.ChainFile == "") {
		return errs.New(errs.BadRequest, "identity key_file and chain_file must both be set or both be empty")
	}
	if !semver(c.Card.Version) {
		return errs.New(errs.BadRequest, fmt.Sprintf("card version %q must be MAJOR.MINOR.PATCH", c.Card.Version))
	}
	seen := map[string]bool{}
	for _, s := range c.Card.Skills {
		if s.ID == "" || s.Name == "" || seen[s.ID] {
			return errs.New(errs.BadRequest, fmt.Sprintf("skill %q needs a unique id and a name", s.ID))
		}
		seen[s.ID] = true
	}
	if err := c.checkPublicURL(); err != nil {
		return err
	}
	for _, u := range []string{c.RegistryURL, c.LogURL} {
		if err := checkHTTPSURL(u); err != nil {
			return err
		}
	}
	if c.Rogue && (c.Role != "station" || c.RogueAck != RogueAck) {
		return errs.New(errs.BadRequest, "rogue: true needs role station and rogue_ack: "+RogueAck)
	}
	if c.Pricing.PerMinuteCents < 0 || c.Pricing.PerMinuteCents > MaxPerMinuteCents || c.Pricing.AssetDecimals < 2 || c.Pricing.AssetDecimals > 12 {
		return errs.New(errs.BadRequest, fmt.Sprintf("pricing: per_minute_cents must be 0-%d and asset_decimals 2-12", MaxPerMinuteCents))
	}
	if c.Spacecraft.NoradID < 0 || c.Spacecraft.NoradID > 999_999_999 {
		return errs.New(errs.BadRequest, "spacecraft.norad_id out of range")
	}
	if c.Role == "spacecraft" && c.Spacecraft.NoradID > 0 && c.Spacecraft.OpsKeyFile == "" {
		return errs.New(errs.BadRequest, "spacecraft.ops_key_file is required with spacecraft.norad_id")
	}
	if c.Session.SpacecraftURL != "" {
		if err := checkHTTPSURL(c.Session.SpacecraftURL); err != nil {
			return err
		}
	}
	if o := c.Session.OpsEnv; o != "" {
		if _, ok := c.Environments[o]; !ok {
			return errs.New(errs.BadRequest, "session.ops_env names an unknown environment "+o)
		}
	}
	hosts := map[string]bool{}
	for _, s := range c.Sites {
		if s.Name == "" || s.Host == "" || s.LatDeg < -90 || s.LatDeg > 90 || s.LonDeg < -180 || s.LonDeg > 180 {
			return errs.New(errs.BadRequest, fmt.Sprintf("site %q needs a name, a host and valid lat/lon", s.Name))
		}
		if hosts[s.Host] {
			return errs.New(errs.BadRequest, "duplicate site host "+s.Host)
		}
		hosts[s.Host] = true
	}
	for name, e := range c.Environments {
		if err := e.validate(name); err != nil {
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
		if _, ok := c.Environments[p.EnvName()]; !ok && len(c.Environments) > 0 {
			return errs.New(errs.BadRequest, fmt.Sprintf("peer %s names unknown environment %q", p.Name, p.Env))
		}
	}
	return nil
}

// EnvName returns the peer's environment, defaulting to prod.
func (p Peer) EnvName() string {
	if p.Env == "" {
		return ProdEnv
	}
	return p.Env
}

func (e Environment) validate(name string) error {
	check := func(field, u string, required bool) error {
		if u == "" && !required {
			return nil
		}
		parsed, err := url.Parse(u)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(e.InsecureHTTP && parsed.Scheme == "http")) {
			return errs.New(errs.BadRequest, fmt.Sprintf("environment %s: %s %q must be an https URL (http only with insecure_http)", name, field, u))
		}
		return nil
	}
	return errors.Join(check("registry_url", e.RegistryURL, true), check("log_url", e.LogURL, true),
		check("log_public_url", e.LogPublicURL, false), check("finder_url", e.FinderURL, false))
}

// semver accepts N.N.N with decimal numbers (the ANS name embeds it).
func semver(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if _, err := strconv.ParseUint(p, 10, 32); err != nil || p == "" {
			return false
		}
	}
	return true
}

func validRole(r string) bool {
	for _, v := range Roles {
		if r == v {
			return true
		}
	}
	return false
}

// checkPublicURL: https://<host>[:port] with no path, query, fragment or
// userinfo, on this agent's own host, so the card, jku and trust card agree.
func (c Config) checkPublicURL() error {
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.User != nil {
		return errs.New(errs.BadRequest, fmt.Sprintf("public_url %q must be https://<host>[:port]", c.PublicURL))
	}
	if !strings.EqualFold(u.Hostname(), c.Host) {
		return errs.New(errs.BadRequest, fmt.Sprintf("public_url host %q must equal host %q", u.Hostname(), c.Host))
	}
	return nil
}

func checkHTTPSURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errs.New(errs.BadRequest, fmt.Sprintf("url %q must be an absolute https URL", s))
	}
	return nil
}
