package main

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/agentnameservice/ans-sdk-go/pop"
	"github.com/agentnameservice/ans-sdk-go/verify/scitt"

	"github.com/arjitsama/overpass/internal/a2a"
	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/wellknown"
)

// replayEntries bounds the DPoP jti replay cache.
const replayEntries = 10_000

// securityFor returns what this role enforces. The agent card is generated
// from the same value (rule 6):
//   - station: DPoP on every A2A call, plus a mandate on book_pass
//   - authority: DPoP on every A2A call (it issues mandates, it does not take them)
//   - ops, auditor, spacecraft: noAuth
func securityFor(ctx context.Context, cfg config.Config, log *slog.Logger) (a2a.Security, error) {
	sec := a2a.Security{Skill: map[string][]a2a.SkillGuard{}}
	if cfg.Role != "station" && cfg.Role != "authority" {
		return sec, nil
	}
	keys, err := scitt.NewKeyStore(cfg.TrustRoots)
	if err != nil {
		return sec, fmt.Errorf("trust_roots: %w", err)
	}
	if keys.IsEmpty() {
		log.Warn("no trust_roots configured: every inbound A2A call will be rejected (fail closed)")
	}
	u, err := url.Parse(cfg.PublicURL)
	if err != nil || u.Host == "" {
		return sec, fmt.Errorf("public_url %q has no host", cfg.PublicURL)
	}
	replay := pop.NewMemoryReplayCache(ctx, replayEntries)
	sec.HTTP = []a2a.HTTPGuard{a2a.DPoPGuard(keys, replay, u.Host, log)}
	if cfg.Role == "station" {
		// Authority keys come from the authority's trust card once Phase 3/4
		// wires verification; until then no mandate verifies (fail closed).
		sec.Skill["book_pass"] = []a2a.SkillGuard{a2a.MandateGuard(func() []*ecdsa.PublicKey { return nil })}
	}
	return sec, nil
}

// a2aServer builds the JSON-RPC server for the configured skills. Handlers
// arrive in later phases; until then a skill answers UnsupportedOperation.
func a2aServer(cfg config.Config, sec a2a.Security, log *slog.Logger) *a2a.Server {
	skills := make([]a2a.SkillInfo, 0, len(cfg.Card.Skills))
	for _, s := range cfg.Card.Skills {
		skills = append(skills, a2a.SkillInfo{ID: s.ID, Name: s.Name, Description: s.Description})
	}
	name := cfg.Card.DisplayName + " (" + wellknown.ANSName(cfg) + ")"
	return a2a.NewServer(name, skills, map[string]a2a.Handler{}, sec, log)
}
