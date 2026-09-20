package session

import (
	"context"
	"sync"
	"time"

	"github.com/agentnameservice/ans-sdk-go/verify/scitt"

	"github.com/arjitsama/overpass/internal/verify"
)

// Compromise is a TEST control for the demo. Real ANS revocation is terminal
// (REVOKED has no outgoing transition), so `ans-cli revoke` works exactly once;
// this lets a chosen peer be marked compromised so its NEXT status-token check
// returns a simulated non-ACTIVE token, cutting the session and triggering a
// re-plan — repeatable for every judge and resettable without restarting. It is
// never armed by default; an operator arms it and can reset it. Reads/writes are
// safe from many goroutines.
type Compromise struct {
	mu    sync.Mutex
	armed map[string]bool
}

// NewCompromise returns a switch with nothing armed.
func NewCompromise() *Compromise { return &Compromise{armed: map[string]bool{}} }

// Arm marks agentID compromised; Reset clears it.
func (c *Compromise) Arm(agentID string)   { c.set(agentID, true) }
func (c *Compromise) Reset(agentID string) { c.set(agentID, false) }

func (c *Compromise) set(agentID string, on bool) {
	c.mu.Lock()
	c.armed[agentID] = on
	c.mu.Unlock()
}

// Armed reports whether agentID is currently marked compromised. The wildcard
// "*" arms every peer at once, so the demo has a single button.
func (c *Compromise) Armed(agentID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.armed[agentID] || c.armed["*"]
}

// simulatedRevokedIat is deliberately far in the past: the Keeper never lets an
// ACTIVE token that predates a known revocation back in, so a real ACTIVE token
// (issued now) is newer and is accepted again after Reset — that is what makes
// the cut resettable without restarting.
var simulatedRevokedIat = time.Unix(1, 0)

// Fetch is the status-token fetch signature the Keeper uses.
type Fetch = func(ctx context.Context, agentID string) (*verify.Token, error)

// Wrap returns a fetch that, for an armed agent, returns a simulated non-ACTIVE
// token (so the policy denies with DenyRevoked and the session is cut) instead
// of calling inner. Unarmed agents pass straight through to inner.
func (c *Compromise) Wrap(inner Fetch) Fetch {
	return func(ctx context.Context, agentID string) (*verify.Token, error) {
		if c.Armed(agentID) {
			return &verify.Token{AgentID: agentID, Status: scitt.StatusRevoked, Iat: simulatedRevokedIat}, nil
		}
		return inner(ctx, agentID)
	}
}
