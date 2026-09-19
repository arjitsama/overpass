package a2a

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
)

// Scheme is one security scheme as the agent card declares it, in the
// {type, scheme, description} shape of docs/webmesh-spec.md section 2.1.
type Scheme struct {
	Name        string `json:"-"`
	Type        string `json:"type"`
	Scheme      string `json:"scheme,omitempty"`
	Description string `json:"description"`
}

// HTTPGuard is a scheme enforced on every A2A call (POST /) before the
// JSON-RPC body is read.
type HTTPGuard struct {
	Scheme Scheme
	Wrap   func(http.Handler) http.Handler
}

// SkillGuard is a scheme enforced on one skill's arguments before its handler
// runs. It returns an *errs.Error to reject.
type SkillGuard struct {
	Scheme Scheme
	Check  func(ctx context.Context, args json.RawMessage) error
}

// Security is what the server actually mounts. The agent card is generated
// from the same value, so the card cannot claim a scheme that is not enforced.
type Security struct {
	HTTP  []HTTPGuard
	Skill map[string][]SkillGuard // skill id -> guards
}

// NoAuth is declared when no HTTP guard is mounted.
var NoAuth = Scheme{Name: "noAuth", Type: "noAuth",
	Description: "Publicly accessible; no authentication is required to message this agent."}

// Schemes returns every declared scheme by name.
func (s Security) Schemes() map[string]Scheme {
	out := map[string]Scheme{}
	if len(s.HTTP) == 0 {
		out[NoAuth.Name] = NoAuth
	}
	for _, g := range s.HTTP {
		out[g.Scheme.Name] = g.Scheme
	}
	for _, gs := range s.Skill {
		for _, g := range gs {
			out[g.Scheme.Name] = g.Scheme
		}
	}
	return out
}

// Requirements returns the card-level securityRequirements: all HTTP guards
// together, or noAuth.
func (s Security) Requirements() []map[string][]string {
	req := map[string][]string{}
	for _, g := range s.HTTP {
		req[g.Scheme.Name] = []string{}
	}
	if len(req) == 0 {
		req[NoAuth.Name] = []string{}
	}
	return []map[string][]string{req}
}

// SkillRequirements returns the securityRequirements for one skill: the
// card-level ones plus that skill's own guards.
func (s Security) SkillRequirements(skill string) []map[string][]string {
	req := s.Requirements()[0]
	names := make([]string, 0, len(s.Skill[skill]))
	for _, g := range s.Skill[skill] {
		names = append(names, g.Scheme.Name)
	}
	sort.Strings(names)
	for _, n := range names {
		delete(req, NoAuth.Name)
		req[n] = []string{}
	}
	return []map[string][]string{req}
}
