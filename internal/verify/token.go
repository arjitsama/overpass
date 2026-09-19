package verify

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/agentnameservice/ans-sdk-go/verify/scitt"
)

// DefaultMaxAge is Overpass's rule for uplink: a status token issued within
// the last 10 minutes (master plan 9.5). The log's own validity is an hour,
// too long to catch a revocation inside a pass.
const DefaultMaxAge = 10 * time.Minute

// Token is a verified status token.
type Token struct {
	AgentID string
	ANSName string
	Status  scitt.AgentStatus
	Iat     time.Time
	Payload scitt.StatusTokenPayload
}

// FreshToken fetches and verifies a new status token for agentID from its
// environment's log. It does not apply the age policy.
func (v *Verifier) FreshToken(ctx context.Context, envName, agentID string) (*Token, error) {
	e, ok := v.envs[envName]
	if !ok {
		return nil, fmt.Errorf("unknown environment %q", envName)
	}
	return e.freshToken(ctx, agentID)
}

func (e *env) freshToken(ctx context.Context, agentID string) (*Token, error) {
	raw, err := e.scitt.FetchStatusToken(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("fetch status token: %w", err)
	}
	vt, err := scitt.VerifyStatusToken(raw, e.keys, clockSkew)
	if err != nil {
		return nil, fmt.Errorf("verify status token: %w", err)
	}
	if vt.Payload.AgentID != agentID {
		return nil, fmt.Errorf("status token is for agent %q, not %q", vt.Payload.AgentID, agentID)
	}
	return &Token{AgentID: vt.Payload.AgentID, ANSName: vt.Payload.AnsName, Status: vt.Payload.Status,
		Iat: time.Unix(vt.Payload.Iat, 0), Payload: vt.Payload}, nil
}

// Policy decides whether a peer's liveness is good enough right now.
type Policy struct {
	MaxAge time.Duration
}

// Decision is the policy's answer.
type Decision struct {
	Allow   bool
	Warning string // set when allowed on an older held token
	Reason  string // set when denied
	Kind    string // why denied: DenyRevoked, DenyStale or DenyUnavailable
	Token   *Token // the token the decision rests on
}

// Denial kinds.
const (
	DenyRevoked     = "revoked"     // a fetched token is not ACTIVE
	DenyStale       = "token_stale" // the newest held token is older than MaxAge
	DenyUnavailable = "unavailable" // no token was ever fetched
)

// Decide applies master plan 9.6:
//   - a fetched token that is not ACTIVE denies at once
//   - a fetched ACTIVE token allows
//   - a failed fetch alone denies nothing: it warns while the newest held
//     ACTIVE token is younger than MaxAge, and denies after that
func (p Policy) Decide(held, fresh *Token, fetchErr error, now time.Time) Decision {
	if fetchErr == nil && fresh != nil {
		if fresh.Status != scitt.StatusActive {
			return Decision{Reason: fmt.Sprintf("status token says %s", fresh.Status), Kind: DenyRevoked, Token: fresh}
		}
		return Decision{Allow: true, Token: fresh}
	}
	why := "no status token"
	if fetchErr != nil {
		why = fetchErr.Error()
	}
	if held == nil || held.Status != scitt.StatusActive {
		return Decision{Reason: "status token unavailable (" + why + ") and none held", Kind: DenyUnavailable}
	}
	age := now.Sub(held.Iat)
	if age >= p.MaxAge {
		return Decision{Reason: fmt.Sprintf("status token unavailable (%s); newest held is %s old, over %s",
			why, age.Round(time.Second), p.MaxAge), Kind: DenyStale, Token: held}
	}
	return Decision{Allow: true, Token: held,
		Warning: fmt.Sprintf("status token fetch failed (%s); using one %s old", why, age.Round(time.Second))}
}

// Keeper holds the newest good token per agent and applies the policy on
// each check, so a short log outage does not cut a pass (master plan 9.6).
type Keeper struct {
	Policy  Policy
	fetch   func(ctx context.Context, agentID string) (*Token, error)
	Now     func() time.Time // clock; tests may replace it
	mu      sync.Mutex
	held    map[string]*Token
	revoked map[string]time.Time // newest non-ACTIVE token's iat per agent
}

// NewKeeper returns a keeper that fetches with fetch.
func NewKeeper(p Policy, fetch func(ctx context.Context, agentID string) (*Token, error)) *Keeper {
	return &Keeper{Policy: p, fetch: fetch, Now: time.Now, held: map[string]*Token{}, revoked: map[string]time.Time{}}
}

// Check fetches a fresh token and decides.
func (k *Keeper) Check(ctx context.Context, agentID string) Decision {
	fresh, err := k.fetch(ctx, agentID)
	k.mu.Lock()
	defer k.mu.Unlock()
	if err == nil && fresh != nil {
		if r, ok := k.revoked[agentID]; ok && fresh.Status == scitt.StatusActive && !fresh.Iat.After(r) {
			return Decision{Reason: "fetched ACTIVE token predates a known revocation", Kind: DenyRevoked, Token: fresh}
		}
		k.record(agentID, fresh)
	}
	return k.Policy.Decide(k.held[agentID], fresh, err, k.Now())
}

// record keeps the newest ACTIVE token, and never lets an ACTIVE token
// issued at or before a known non-ACTIVE one back in (fetches can finish out
// of order), so a revocation is not outlived by an older token.
func (k *Keeper) record(agentID string, t *Token) {
	if t.Status != scitt.StatusActive {
		if t.Iat.After(k.revoked[agentID]) {
			k.revoked[agentID] = t.Iat
		}
		delete(k.held, agentID)
		return
	}
	if r, ok := k.revoked[agentID]; ok && !t.Iat.After(r) {
		return
	}
	if h := k.held[agentID]; h == nil || t.Iat.After(h.Iat) {
		k.held[agentID] = t
	}
}
