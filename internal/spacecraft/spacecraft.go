// Package spacecraft is the simulated satellite (master plan 9.1). It holds
// the Ops public key, its NORAD ID and the last accepted counter, persisted.
// It accepts a command only if the typ and Ops signature check out, the
// norad_id is its own and the counter is strictly greater than the last one.
// It does not know or care which station relayed the command, so a station
// can relay, delay or drop, but never forge or replay.
package spacecraft

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/jose"
	"github.com/arjitsama/overpass/internal/schema"
	"github.com/arjitsama/overpass/internal/store"
)

// State is the spacecraft's simple state. Integers only.
type State struct {
	Mode       string `json:"mode"` // "nominal" or "safe"
	BatteryPct int64  `json:"battery_pct"`
	BeaconS    int64  `json:"beacon_s"`
	LastCmd    int64  `json:"last_counter"`
}

// Spacecraft is one simulated satellite.
type Spacecraft struct {
	NoradID int64
	OpsKeys []*ecdsa.PublicKey
	Store   *store.Store

	mu    sync.Mutex
	state State
}

// New returns a spacecraft whose last accepted counter is read from store.
func New(ctx context.Context, norad int64, opsKeys []*ecdsa.PublicKey, st *store.Store) (*Spacecraft, error) {
	last, err := st.Counter(ctx, counterName(norad))
	if err != nil {
		return nil, err
	}
	return &Spacecraft{NoradID: norad, OpsKeys: opsKeys, Store: st,
		state: State{Mode: "nominal", BatteryPct: 87, BeaconS: 30, LastCmd: last}}, nil
}

func counterName(norad int64) string { return "spacecraft:" + strconv.FormatInt(norad, 10) }

// body is what the spacecraft understands in a command's body.
type body struct {
	Op      string `json:"op"`
	Mode    string `json:"mode,omitempty"`
	BeaconS int64  `json:"beacon_s,omitempty"`
}

// Uplink handles one command token and returns the Ack. A token too
// malformed to carry a counter is an error with a named code.
func (s *Spacecraft) Uplink(ctx context.Context, token string) (schema.Ack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cmd, err := schema.VerifyCommand(token, s.OpsKeys)
	if err != nil {
		peek, perr := schema.PeekCommand(token)
		if perr != nil {
			return schema.Ack{}, err // typ or structure: no counter to ack
		}
		return rejected(peek.Counter, err), nil
	}
	if cmd.NoradID != s.NoradID {
		return rejected(cmd.Counter, errs.New(errs.CommandRejectedNorad, fmt.Sprintf("for %d, this is %d", cmd.NoradID, s.NoradID))), nil
	}
	advanced, err := s.Store.Advance(ctx, counterName(s.NoradID), cmd.Counter)
	if err != nil {
		return schema.Ack{}, err
	}
	if !advanced {
		return rejected(cmd.Counter, errs.New(errs.CommandRejectedCounter, fmt.Sprintf("counter %d is not above %d", cmd.Counter, s.state.LastCmd))), nil
	}
	s.state.LastCmd = cmd.Counter
	s.apply(cmd)
	return schema.Ack{Counter: cmd.Counter, Result: schema.AckAccepted, TelemetrySHA256: s.telemetry()}, nil
}

// apply runs a known op; unknown ops are accepted as no-ops (the station
// cannot read the body, and the spacecraft logs what it got).
func (s *Spacecraft) apply(c schema.Command) {
	var b body
	if json.Unmarshal(c.Body, &b) != nil {
		return
	}
	switch b.Op {
	case "set_mode":
		if b.Mode == "nominal" || b.Mode == "safe" {
			s.state.Mode = b.Mode
		}
	case "set_beacon":
		if b.BeaconS >= 5 && b.BeaconS <= 600 {
			s.state.BeaconS = b.BeaconS
		}
	}
	if s.state.BatteryPct > 20 {
		s.state.BatteryPct--
	}
}

// telemetry is SHA-256 of the JCS of the current state.
func (s *Spacecraft) telemetry() string {
	raw, _ := jose.Canonicalize(s.state)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// State returns a copy of the current state.
func (s *Spacecraft) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func rejected(counter int64, err error) schema.Ack {
	var e *errs.Error
	code := string(errs.CommandRejectedSignature)
	if errors.As(err, &e) {
		code = string(e.Code)
	}
	if counter < 1 {
		counter = 1
	}
	return schema.Ack{Counter: counter, Result: schema.AckRejected, Reason: code}
}

// Handler is the A2A `uplink` skill: {"skill":"uplink","command":"<jws>"}.
func (s *Spacecraft) Handler(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Skill   string `json:"skill"`
		Command string `json:"command"`
	}
	if err := jose.StrictJSON(raw, schema.MaxObjectBytes); err != nil {
		return nil, errs.New(errs.CommandParseError, err.Error())
	}
	if err := json.Unmarshal(raw, &a); err != nil || a.Command == "" {
		return nil, errs.New(errs.CommandParseError, "command is missing")
	}
	return s.Uplink(ctx, a.Command)
}
