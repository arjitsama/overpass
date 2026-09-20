package session

import (
	"context"
	"testing"
	"time"

	"github.com/agentnameservice/ans-sdk-go/verify/scitt"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/verify"
)

const peer = "gs-blacksburg-agent"

// activeFetch always returns a fresh ACTIVE token.
func activeFetch(now func() time.Time) Fetch {
	return func(context.Context, string) (*verify.Token, error) {
		return &verify.Token{AgentID: peer, Status: scitt.StatusActive, Iat: now()}, nil
	}
}

func monitorOn(k *verify.Keeper) (*Monitor, *[]bus.Event) {
	var events []bus.Event
	m := &Monitor{Keeper: k, AgentID: peer, Self: "ops", Subject: "b-1",
		Emit: func(e bus.Event) { events = append(events, e) }}
	return m, &events
}

// TestCompromiseCutsAndResets: while unarmed the session stays up; arming the
// peer cuts it (SESSION_CUT:revoked); resetting lets a fresh session run again.
func TestCompromiseCutsAndResets(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	c := NewCompromise()
	k := verify.NewKeeper(verify.Policy{MaxAge: verify.DefaultMaxAge}, c.Wrap(activeFetch(clock)))
	k.Now = clock

	// Unarmed: allowed, no cut.
	m, _ := monitorOn(k)
	if code := m.Tick(context.Background()); code != "" {
		t.Fatalf("unarmed tick cut the session: %s", code)
	}

	// Arm the peer: the next tick cuts with a revocation.
	c.Arm(peer)
	m2, ev := monitorOn(k)
	if code := m2.Tick(context.Background()); code != errs.SessionCutRevoked {
		t.Fatalf("armed tick = %q, want %s", code, errs.SessionCutRevoked)
	}
	if !c.Armed(peer) {
		t.Fatal("peer should read as armed")
	}
	var sawCut bool
	for _, e := range *ev {
		if e.Kind == "session_cut" {
			sawCut = true
		}
	}
	if !sawCut {
		t.Fatal("no session_cut event emitted")
	}

	// Reset: a fresh session (and a fresh keeper, as a new pass would build) runs.
	c.Reset(peer)
	now = now.Add(time.Minute)
	k2 := verify.NewKeeper(verify.Policy{MaxAge: verify.DefaultMaxAge}, c.Wrap(activeFetch(clock)))
	k2.Now = clock
	m3, _ := monitorOn(k2)
	if code := m3.Tick(context.Background()); code != "" {
		t.Fatalf("after reset, tick cut the session: %s", code)
	}
}
