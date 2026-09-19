package main

import (
	"os"

	"github.com/arjitsama/overpass/internal/authority"
	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/trust"
)

// newTrustClient builds a trust-index client from config, reading the admin
// bearer key from the environment (hard rule 2: never from the config file).
// It returns nil when no trust index is configured.
func newTrustClient(cfg config.Config) *trust.Client {
	if cfg.TrustIndex.URL == "" {
		return nil
	}
	key := ""
	if cfg.TrustIndex.AdminKeyEnv != "" {
		key = os.Getenv(cfg.TrustIndex.AdminKeyEnv)
	}
	return &trust.Client{BaseURL: cfg.TrustIndex.URL, AdminKey: key, Agents: cfg.TrustIndex.Agents}
}

// trustSource returns the authority's TrustSource: the real index when
// configured, else the static trust_tiers map (local runs and tests).
func trustSource(cfg config.Config) authority.TrustSource {
	if c := newTrustClient(cfg); c != nil {
		return c
	}
	return authority.StaticTrust(cfg.TrustTiers)
}
