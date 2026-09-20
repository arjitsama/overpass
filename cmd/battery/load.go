package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/arjitsama/overpass/internal/battery"
	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/verify"
)

// identity is one Ops credential: an ANS identity key and its cert chain.
type identity struct {
	KeyFile   string `yaml:"key_file"`
	ChainFile string `yaml:"chain_file"`
	ANSName   string `yaml:"ans_name"`
	AgentID   string `yaml:"agent_id"`
	Env       string `yaml:"env"` // which environment's log holds this identity's SCITT headers
}

// batteryConfig is the red-team tool's config. It legitimately holds the
// authority signing keys the target trusts, to craft validly-signed mandates.
type batteryConfig struct {
	StationURL    string                        `yaml:"station_url"`
	StationANS    string                        `yaml:"station_ans"`
	StationHost   string                        `yaml:"station_host"`
	StationCA     string                        `yaml:"station_ca"`
	SpacecraftURL string                        `yaml:"spacecraft_url"`
	NoradID       int64                         `yaml:"norad_id"`
	OtherStation  string                        `yaml:"other_station"`
	Dials         map[string]string             `yaml:"dials"` // public host:port -> real addr (local runs)
	Environments  map[string]config.Environment `yaml:"environments"`

	Ops          identity `yaml:"ops"`
	OpsWrong     identity `yaml:"ops_wrong"`
	AuthKeyFile  string   `yaml:"authority_key_file"`
	AuthName     string   `yaml:"authority_ans"`
	OtherKeyFile string   `yaml:"other_authority_key_file"`
	OtherName    string   `yaml:"other_authority_ans"`
}

// load builds a Battery from the config file, returning what it targets and a
// cleanup func.
func load(ctx context.Context, path string) (*battery.Battery, Target, func(), error) {
	var target Target
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, target, nil, err
	}
	var c batteryConfig
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, target, nil, fmt.Errorf("battery config: %w", err)
	}
	target = Target{StationHost: c.StationHost, StationANS: c.StationANS,
		StationURL: c.StationURL, SpacecraftURL: c.SpacecraftURL}
	v, err := verify.New(config.Config{Environments: c.Environments}, verify.Options{Self: "battery"})
	if err != nil {
		return nil, target, nil, err
	}
	hc, err := c.httpClient()
	if err != nil {
		return nil, target, nil, err
	}
	out1, stop1, err := outbound(ctx, v, c.Ops)
	if err != nil {
		return nil, target, nil, err
	}
	stops := []func(){stop1}
	stopAll := func() {
		for _, s := range stops {
			s()
		}
	}
	// A second registered identity is a deployment fact, not a given: only
	// three agents are registered in production. Without one, the attacks that
	// need it report INCONCLUSIVE with the reason rather than being skipped
	// silently or counted as blocked.
	var out2 *verify.Outbound
	if c.OpsWrong.KeyFile != "" {
		o, stop2, err := outbound(ctx, v, c.OpsWrong)
		if err != nil {
			stopAll()
			return nil, target, nil, err
		}
		out2, stops = o, append(stops, stop2)
	}
	opsKey, err := readPrivateKey(c.Ops.KeyFile)
	if err != nil {
		stopAll()
		return nil, target, nil, err
	}
	authKey, err := readPrivateKey(c.AuthKeyFile)
	if err != nil {
		stopAll()
		return nil, target, nil, err
	}
	// An unconfigured second authority is an untrusted key, which is exactly
	// what the not_owner attack needs; the target must refuse it either way.
	otherKey := mustEphemeralKey()
	if c.OtherKeyFile != "" {
		otherKey, err = readPrivateKey(c.OtherKeyFile)
		if err != nil {
			stopAll()
			return nil, target, nil, err
		}
	}
	b := &battery.Battery{
		HTTP: hc, StationURL: c.StationURL, StationANS: c.StationANS, StationHost: c.StationHost,
		SpaceURL: c.SpacecraftURL, Ops: out1, OpsANS: c.Ops.ANSName, OpsJKT: out1.JKT(), OpsKey: opsKey,
		OpsWrong: out2, OpsWrong2: mustEphemeralKey(), AuthKey: authKey, AuthName: c.AuthName,
		OtherAuthKey: otherKey, OtherAuthName: c.OtherName, UnknownKey: mustEphemeralKey(),
		NoradID: c.NoradID, OtherStation: c.OtherStation, Now: time.Now,
	}
	return b, target, stopAll, nil
}

// mustEphemeralKey returns a throwaway P-256 key. These stand in for keys the
// target does not trust; generation cannot fail with a working RNG.
func mustEphemeralKey() *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic("battery: no entropy for an ephemeral key: " + err.Error())
	}
	return k
}

func outbound(ctx context.Context, v *verify.Verifier, id identity) (*verify.Outbound, func(), error) {
	key, err := readPrivateKey(id.KeyFile)
	if err != nil {
		return nil, nil, err
	}
	chain, err := os.ReadFile(id.ChainFile)
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(chain)
	if block == nil {
		return nil, nil, fmt.Errorf("%s: no certificate", id.ChainFile)
	}
	env := id.Env
	if env == "" {
		env = config.ProdEnv
	}
	out, sup, err := v.NewOutbound(key, block.Bytes, env, id.AgentID)
	if err != nil {
		return nil, nil, err
	}
	if err := sup.RefreshNow(ctx); err != nil {
		return nil, nil, fmt.Errorf("fetch SCITT headers for %s: %w", id.ANSName, err)
	}
	stop := sup.StartAutoRefresh(ctx)
	return out, stop, nil
}

func (c batteryConfig) httpClient() (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if c.StationCA != "" {
		raw, err := os.ReadFile(c.StationCA)
		if err != nil {
			return nil, err
		}
		pool, _ := x509.SystemCertPool()
		if pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(raw) {
			return nil, fmt.Errorf("station_ca: no certificates in %s", c.StationCA)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	if len(c.Dials) > 0 {
		dials := c.Dials
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if real, ok := dials[addr]; ok {
				addr = real
			}
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
		}
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func readPrivateKey(path string) (*ecdsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("%s: no PEM block", path)
	}
	if block.Type == "EC PRIVATE KEY" {
		return x509.ParseECPrivateKey(block.Bytes)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an EC key", path)
	}
	return ek, nil
}
