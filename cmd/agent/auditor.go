package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"log/slog"
	"time"

	ansverify "github.com/agentnameservice/ans-sdk-go/verify"

	"github.com/arjitsama/overpass/internal/auditor"
	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/jose"
	"github.com/arjitsama/overpass/internal/station"
	"github.com/arjitsama/overpass/internal/verify"
	"github.com/arjitsama/overpass/internal/wellknown"
)

// auditorRole mounts the passive audit_pass skill: given a finished pass's
// receipt, mandate and both sides' evidence, it re-verifies and returns a
// signed AuditReport (master plan 9.9). Canary probing is driven by
// cmd/battery, which holds the credentials to book.
// ansHost extracts the host from an ANS name.
func ansHost(ansName string) (string, error) {
	n, err := ansverify.ParseAnsName(ansName)
	if err != nil {
		return "", err
	}
	return n.Host, nil
}

func auditorRole(cfg config.Config, id wellknown.Identity, b *bus.Bus, log *slog.Logger, r role) (role, error) {
	env := cfg.Auditor.Env
	if env == "" {
		env = config.ProdEnv
	}
	v, err := verify.New(cfg, verify.Options{Self: cfg.Host, Emit: func(e bus.Event) { _, _ = b.Publish(e) }, Log: log})
	if err != nil {
		return r, err
	}
	// Key station identity keys by host (from each entry's ANS name), so a
	// version difference between auditor and station cannot break the lookup.
	stationKeys := map[string][]*ecdsa.PublicKey{}
	for _, k := range cfg.Auditor.StationKeys {
		pub, err := station.LoadPublicKey(k.KeyFile)
		if err != nil {
			return r, err
		}
		h, herr := ansHost(k.ANSName)
		if herr != nil {
			return r, herr
		}
		stationKeys[h] = append(stationKeys[h], pub)
	}
	var authKeys []*ecdsa.PublicKey
	for _, k := range cfg.AuthorityKeys {
		pub, err := station.LoadPublicKey(k.KeyFile)
		if err != nil {
			return r, err
		}
		authKeys = append(authKeys, pub)
	}
	// Post pass_delivery to the trust index when one is configured; otherwise
	// fall back to emitting the observation as a bus event.
	var sink auditor.TrustSink = auditor.LogSink{Emit: func(e bus.Event) { _, _ = b.Publish(e) }}
	if tc := newTrustClient(cfg); tc != nil {
		sink = tc
	}
	au := &auditor.Auditor{ANSName: wellknown.ANSName(cfg), Key: id.Key, Peers: v, AuthorityKeys: authKeys,
		StationKeys: func(host string) []*ecdsa.PublicKey { return stationKeys[host] },
		Sink:        sink, Emit: func(e bus.Event) { _, _ = b.Publish(e) }, Now: time.Now}
	r.handlers["audit_pass"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Skill       string `json:"skill"`
			StationHost string `json:"station_host"`
			StationANS  string `json:"station_ans"`
			PassID      string `json:"pass_id"`
			auditor.PassEvidence
		}
		if err := jose.StrictJSON(raw, 256<<10); err != nil {
			return nil, errs.New(errs.AuditParseError, err.Error())
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, errs.New(errs.AuditParseError, err.Error())
		}
		rep, tok, err := au.Audit(ctx, in.StationHost, in.StationANS, in.PassID, in.PassEvidence)
		if err != nil {
			return nil, err
		}
		return map[string]any{"report": rep, "signed": tok}, nil
	}
	return r, nil
}
