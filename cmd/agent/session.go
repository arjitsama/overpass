package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/arjitsama/overpass/internal/a2a"
	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/jose"
	"github.com/arjitsama/overpass/internal/schema"
	"github.com/arjitsama/overpass/internal/session"
	"github.com/arjitsama/overpass/internal/spacecraft"
	"github.com/arjitsama/overpass/internal/station"
	"github.com/arjitsama/overpass/internal/store"
	"github.com/arjitsama/overpass/internal/verify"
	"github.com/arjitsama/overpass/internal/wellknown"
)

// spacecraftRole mounts the simulated spacecraft's `uplink` skill. It is
// noAuth at the transport: its security is the Ops signature on each command.
func spacecraftRole(ctx context.Context, cfg config.Config, r role, log *slog.Logger) (role, error) {
	if cfg.Spacecraft.NoradID == 0 || cfg.Spacecraft.OpsKeyFile == "" {
		log.Warn("spacecraft.norad_id / ops_key_file not configured: uplink disabled")
		r.disabled = append(r.disabled, "uplink")
		return r, nil
	}
	pub, err := station.LoadPublicKey(cfg.Spacecraft.OpsKeyFile)
	if err != nil {
		return r, fmt.Errorf("spacecraft ops key: %w", err)
	}
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return r, err
	}
	r.close = func() { db.Close() }
	sc, err := spacecraft.New(ctx, cfg.Spacecraft.NoradID, []*ecdsa.PublicKey{pub}, db)
	if err != nil {
		r.close()
		return r, err
	}
	r.handlers["uplink"] = sc.Handler
	return r, nil
}

// remoteSpacecraft relays over A2A to the spacecraft agent: the RF link.
type remoteSpacecraft struct{ c *a2a.Client }

func (s remoteSpacecraft) Uplink(ctx context.Context, token string) (schema.Ack, error) {
	var ack schema.Ack
	err := s.c.Call(ctx, "uplink", map[string]any{"command": token}, &ack)
	return ack, err
}

// sessionSkills mounts relay_command and session_evidence on a station.
func sessionSkills(cfg config.Config, db *store.Store, emit func(bus.Event), log *slog.Logger, r *role) error {
	if cfg.Session.SpacecraftURL == "" {
		log.Warn("session.spacecraft_url not configured: relay_command and session_evidence disabled")
		r.disabled = append(r.disabled, "relay_command", "session_evidence")
		return nil
	}
	hc, err := spacecraftHTTP(cfg.Session.SpacecraftCA)
	if err != nil {
		return err
	}
	v, err := verify.New(cfg, verify.Options{Self: cfg.Host, Emit: emit, Log: log})
	if err != nil {
		return err
	}
	env := cfg.Session.OpsEnv
	if env == "" {
		env = config.ProdEnv
	}
	keeper := verify.NewKeeper(verify.Policy{MaxAge: verify.DefaultMaxAge}, func(ctx context.Context, agentID string) (*verify.Token, error) {
		return v.FreshToken(ctx, env, agentID)
	})
	m := &session.Manager{ANSName: wellknown.ANSName(cfg), Store: db,
		Uplink: remoteSpacecraft{&a2a.Client{URL: cfg.Session.SpacecraftURL, HTTP: hc}},
		Caller: station.PopCaller, Keeper: keeper, Emit: emit, Now: time.Now, Every: session.TokenEvery,
		Auditors: cfg.Session.Auditors}
	prevClose := r.close
	r.close = func() { m.Close(); prevClose() }
	r.handlers["relay_command"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var a struct {
			Skill     string `json:"skill"`
			BookingID string `json:"booking_id"`
			Command   string `json:"command"`
		}
		if err := decodeStrict(raw, &a, errs.CommandParseError); err != nil {
			return nil, err
		}
		return m.Relay(ctx, a.BookingID, a.Command)
	}
	r.handlers["session_evidence"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var a struct {
			Skill     string `json:"skill"`
			BookingID string `json:"booking_id"`
		}
		if err := decodeStrict(raw, &a, errs.BadRequest); err != nil {
			return nil, err
		}
		c, ok := station.PopCaller(ctx)
		if !ok {
			return nil, errs.New(errs.SessionRejectedCaller, "no proven caller")
		}
		return m.Evidence(a.BookingID, c.ANSName)
	}
	return nil
}

func decodeStrict(raw json.RawMessage, v any, code errs.Code) error {
	if err := jose.StrictJSON(raw, schema.MaxObjectBytes); err != nil {
		return errs.New(code, err.Error())
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errs.New(code, "arguments: "+err.Error())
	}
	return nil
}

// spacecraftHTTP trusts the system roots plus an optional PEM (a local,
// self-signed spacecraft agent).
func spacecraftHTTP(caFile string) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if caFile != "" {
		raw, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("session.spacecraft_ca: %w", err)
		}
		pool, _ := x509.SystemCertPool()
		if pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(raw) {
			return nil, fmt.Errorf("session.spacecraft_ca: no certificates in %s", caFile)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Timeout: 10 * time.Second, Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
