// Package ops is Mission Ops' side of a pass: it signs commands with a
// per-satellite counter that survives restarts, keeps its own command chain,
// watches the station's status token, and on a cut discards queued commands
// and asks the planner to replan (master plan 9.4-9.7, 7.9).
package ops

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/chain"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/schema"
	"github.com/arjitsama/overpass/internal/session"
	"github.com/arjitsama/overpass/internal/store"
)

// Commander builds signed commands for one satellite.
type Commander struct {
	NoradID int64
	Key     *ecdsa.PrivateKey // the Ops identity key; the spacecraft holds its public half
	Store   *store.Store
	Now     func() time.Time
}

// Build signs a command with the next counter.
func (c *Commander) Build(ctx context.Context, mandateID, class string, body json.RawMessage) (string, schema.Command, error) {
	n, err := c.Store.NextCounter(ctx, "cmd:"+strconv.FormatInt(c.NoradID, 10))
	if err != nil {
		return "", schema.Command{}, err
	}
	cmd := schema.Command{NoradID: c.NoradID, Counter: n, MandateID: mandateID, Class: class, Body: body, IssuedAt: c.Now().Unix()}
	tok, err := schema.SignCommand(cmd, c.Key)
	return tok, cmd, err
}

// StationClient relays one command through a station and returns the Ack.
type StationClient interface {
	Relay(ctx context.Context, bookingID, token string) (schema.Ack, error)
}

type queued struct {
	class string
	body  json.RawMessage
}

// Pass drives one booked pass from the Ops side.
type Pass struct {
	BookingID   string
	MandateID   string
	StationHost string
	Commander   *Commander
	Station     StationClient
	// Monitor watches the station's status token; its OnCut is wired by Start.
	Monitor *session.Monitor
	// Replan is called with the station's host after a cut.
	Replan func(host string)
	Emit   func(bus.Event)
	Self   string

	mu      sync.Mutex
	chain   *chain.Chain
	acks    map[int64]schema.Ack
	missing []int64
	queue   []queued
	cut     errs.Code
}

// Start checks the station's token once and wires the cut handler.
func (p *Pass) Start(ctx context.Context) error {
	p.mu.Lock()
	p.chain, p.acks = chain.New(), map[int64]schema.Ack{}
	p.mu.Unlock()
	p.Monitor.OnCut = p.onCut
	if code := p.Monitor.Tick(ctx); code != "" {
		return errs.New(code, "station's status token")
	}
	return nil
}

func (p *Pass) onCut(code errs.Code, reason string) {
	p.mu.Lock()
	dropped := len(p.queue)
	p.queue, p.cut = nil, code
	p.mu.Unlock()
	// The Monitor already emitted session_cut; record what the cut discarded.
	p.emit("commands_discarded", "cut", string(code), map[string]any{"reason": reason, "discarded": dropped})
	if p.Replan != nil {
		p.Replan(p.StationHost)
	}
}

// Enqueue queues a command for Flush.
func (p *Pass) Enqueue(class string, body json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cut == "" {
		p.queue = append(p.queue, queued{class, body})
	}
}

// Flush sends queued commands in order until the queue is empty or the
// session is cut.
func (p *Pass) Flush(ctx context.Context) ([]schema.Ack, error) {
	var acks []schema.Ack
	for {
		p.mu.Lock()
		if len(p.queue) == 0 {
			p.mu.Unlock()
			return acks, nil
		}
		q := p.queue[0]
		p.queue = p.queue[1:]
		p.mu.Unlock()
		a, err := p.Send(ctx, q.class, q.body)
		if err != nil {
			return acks, err
		}
		acks = append(acks, a)
	}
}

// Send signs, chains and relays one command. A command with no Ack is a
// suspected drop.
func (p *Pass) Send(ctx context.Context, class string, body json.RawMessage) (schema.Ack, error) {
	p.mu.Lock()
	if p.cut != "" {
		p.mu.Unlock()
		return schema.Ack{}, errs.New(p.cut, "session cut")
	}
	p.mu.Unlock()
	tok, cmd, err := p.Commander.Build(ctx, p.MandateID, class, body)
	if err != nil {
		return schema.Ack{}, err
	}
	ack, err := p.Station.Relay(ctx, p.BookingID, tok)
	p.mu.Lock()
	defer p.mu.Unlock()
	// A named refusal by the station (window, class, cut, DPoP...) means the
	// command was never relayed, so neither side chains it. Anything else
	// (timeout, transport, no ack) may have been relayed: chain it.
	if err != nil && refusedBeforeRelay(err) {
		return schema.Ack{}, err
	}
	if _, cerr := p.chain.Append(cmd.Counter, cmd.MandateID, cmd.Class, chain.CmdSHA256(tok)); cerr != nil {
		return schema.Ack{}, cerr
	}
	if err == nil {
		if verr := ack.Validate(); verr != nil {
			err = errs.New(errs.AckParseError, verr.Error())
		}
	}
	if err != nil || ack.Counter != cmd.Counter {
		p.missing = append(p.missing, cmd.Counter)
		p.emit("suspected_drop", "warn", string(errs.AckMissing), map[string]any{"counter": cmd.Counter})
		if err == nil {
			err = errs.New(errs.AckMissing, fmt.Sprintf("ack for %d, sent %d", ack.Counter, cmd.Counter))
		}
		return schema.Ack{}, err
	}
	p.acks[cmd.Counter] = ack
	return ack, nil
}

// refusedBeforeRelay reports whether err is a station-side rejection issued
// before the command reached the spacecraft.
func refusedBeforeRelay(err error) bool {
	var e *errs.Error
	if !errs.As(err, &e) {
		return false
	}
	switch e.Code {
	case errs.AckMissing, errs.AckParseError, errs.Unavailable, errs.Internal:
		return false
	}
	return true
}

// Evidence returns Ops' record of the pass.
func (p *Pass) Evidence() session.Evidence {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := session.Evidence{BookingID: p.BookingID, Head: p.chain.Head(), Records: p.chain.Records(),
		MissingAcks: append([]int64(nil), p.missing...), Cut: string(p.cut)}
	for _, r := range e.Records {
		if a, ok := p.acks[r.Counter]; ok {
			e.Acks = append(e.Acks, a)
		}
	}
	return e
}

func (p *Pass) emit(kind, result, reason string, data map[string]any) {
	if p.Emit != nil {
		p.Emit(bus.Event{Agent: p.Self, Kind: kind, Subject: p.BookingID, Result: result, Reason: reason, Data: data})
	}
}
