package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/agentnameservice/ans-sdk-go/pop"
	"github.com/agentnameservice/ans-sdk-go/verify/scitt"

	"github.com/arjitsama/overpass/internal/a2a"
	"github.com/arjitsama/overpass/internal/authority"
	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/schema"
	"github.com/arjitsama/overpass/internal/station"
	"github.com/arjitsama/overpass/internal/store"
	"github.com/arjitsama/overpass/internal/verify"
	"github.com/arjitsama/overpass/internal/wellknown"
)

// role is what one role mounts: the security the card is generated from,
// the skill handlers, and what to close on shutdown.
type role struct {
	sec      a2a.Security
	handlers map[string]a2a.Handler
	close    func()
}

// buildRole wires the role's skills. The agent card is generated from the
// same Security value (rule 6):
//   - station: DPoP on every A2A call, plus a mandate on book_pass
//   - authority: DPoP on every A2A call (it issues mandates, it does not take them)
//   - ops, auditor, spacecraft: noAuth
func buildRole(ctx context.Context, cfg config.Config, id wellknown.Identity, b *bus.Bus, log *slog.Logger) (role, error) {
	r := role{sec: a2a.Security{Skill: map[string][]a2a.SkillGuard{}}, handlers: map[string]a2a.Handler{}, close: func() {}}
	if cfg.Role != "station" && cfg.Role != "authority" {
		return r, nil
	}
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return r, fmt.Errorf("store %s: %w", cfg.DBPath, err)
	}
	r.close = func() { db.Close() }
	emit := func(e bus.Event) { _, _ = b.Publish(e) }
	guard, err := dpopGuard(cfg, db, log)
	if err != nil {
		r.close()
		return r, err
	}
	r.sec.HTTP = []a2a.HTTPGuard{guard}
	if cfg.Role == "station" {
		st, err := newStation(cfg, id, db, emit, log)
		if err != nil {
			r.close()
			return r, err
		}
		r.sec.Skill["book_pass"] = []a2a.SkillGuard{a2a.MandateDeclared()}
		r.handlers["get_pass_quote"] = st.GetPassQuote
		r.handlers["book_pass"] = st.BookPass
		return r, nil
	}
	v, err := verify.New(cfg, verify.Options{Self: cfg.Host, Emit: emit, Log: log})
	if err != nil {
		r.close()
		return r, err
	}
	au := &authority.Authority{ANSName: wellknown.ANSName(cfg), Key: id.Key, Rules: cfg.FlightRules, Ops: cfg.OpsAgents,
		Peers: v, Trust: authority.StaticTrust(cfg.TrustTiers), Store: db, Caller: station.PopCaller,
		Emit: emit, Now: time.Now, Log: log}
	r.handlers["issue_mandate"] = au.IssueMandate
	return r, nil
}

// dpopGuard mounts ans-sdk-go's pop.Middleware with the store's jti table as
// its replay cache (book_pass check 10). A rogue station gets a cache that
// never sees a replay: it skips the DPoP checks, as master plan 10 requires.
func dpopGuard(cfg config.Config, db *store.Store, log *slog.Logger) (a2a.HTTPGuard, error) {
	keys, err := scitt.NewKeyStore(rootKeys(cfg))
	if err != nil {
		return a2a.HTTPGuard{}, fmt.Errorf("trust_roots: %w", err)
	}
	if keys.IsEmpty() {
		log.Warn("no trust_roots configured: every inbound A2A call will be rejected (fail closed)")
	}
	u, err := url.Parse(cfg.PublicURL)
	if err != nil || u.Host == "" {
		return a2a.HTTPGuard{}, fmt.Errorf("public_url %q has no host", cfg.PublicURL)
	}
	var replay pop.ReplayCache = db.ReplayCache()
	if cfg.Rogue {
		replay = neverSeen{}
	}
	return a2a.DPoPGuard(keys, replay, u.Host, log), nil
}

// neverSeen is the rogue station's replay cache.
type neverSeen struct{}

func (neverSeen) CheckAndStore(string, time.Time) (bool, error) { return false, nil }

func newStation(cfg config.Config, id wellknown.Identity, db *store.Store, emit func(bus.Event), log *slog.Logger) (*station.Station, error) {
	var reg schema.SatRegistry
	if cfg.SatReg.File != "" {
		var err error
		if reg, err = station.LoadRegistry(cfg.SatReg); err != nil {
			return nil, err
		}
	} else {
		log.Warn("no satellite registry configured: every quote is refused (QUOTE_REJECTED:norad_id)")
	}
	keys, err := station.LoadStaticKeys(cfg.AuthorityKeys)
	if err != nil {
		return nil, err
	}
	if cfg.Rogue {
		banner := strings.Repeat("!", 72)
		log.Error(banner)
		log.Error("ROGUE STATION: book_pass SKIPS the mandate signature and DPoP checks (master plan 10). Demo only.")
		log.Error(banner)
	}
	return &station.Station{ANSName: wellknown.ANSName(cfg), Pricing: cfg.Pricing, Key: id.Key, Store: db,
		Registry: reg, Keys: keys, Caller: station.PopCaller, Rogue: cfg.Rogue, Emit: emit, Now: time.Now, Log: log}, nil
}

// rootKeys is every transparency log root key the agent trusts: trust_roots
// plus each environment's root_keys, deduplicated. Callers may be registered
// in any configured environment.
func rootKeys(cfg config.Config) []string {
	seen := map[string]bool{}
	var out []string
	add := func(ks []string) {
		for _, k := range ks {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	add(cfg.TrustRoots)
	for _, e := range cfg.Environments {
		add(e.RootKeys)
	}
	return out
}

// a2aServer builds the JSON-RPC server for the configured skills. A skill
// the role has no handler for answers UnsupportedOperation.
func a2aServer(cfg config.Config, r role, log *slog.Logger) *a2a.Server {
	skills := make([]a2a.SkillInfo, 0, len(cfg.Card.Skills))
	for _, s := range cfg.Card.Skills {
		skills = append(skills, a2a.SkillInfo{ID: s.ID, Name: s.Name, Description: s.Description})
	}
	name := cfg.Card.DisplayName + " (" + wellknown.ANSName(cfg) + ")"
	return a2a.NewServer(name, skills, r.handlers, r.sec, log)
}
