package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/chain"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/schema"
	"github.com/arjitsama/overpass/internal/station"
	"github.com/arjitsama/overpass/internal/store"
	"github.com/arjitsama/overpass/internal/verify"
)

// Slack widens the window on both sides for TLE drift and clock skew (9.2).
const Slack = 30 * time.Second

// Uplinker sends a command to the spacecraft and returns its Ack.
type Uplinker interface {
	Uplink(ctx context.Context, token string) (schema.Ack, error)
}

// Evidence is what one side can show about a pass (for the auditor).
type Evidence struct {
	BookingID   string                 `json:"booking_id"`
	Head        string                 `json:"chain_head"`
	Records     []schema.CommandRecord `json:"records"`
	Acks        []schema.Ack           `json:"acks"`
	MissingAcks []int64                `json:"missing_acks"` // suspected drops
	Cut         string                 `json:"cut,omitempty"`
}

// Manager is the station's side of every pass session.
type Manager struct {
	ANSName string
	Store   *store.Store
	Uplink  Uplinker
	Caller  station.CallerFunc
	// Keeper checks the Ops agent's status token (master plan 9.7).
	Keeper *verify.Keeper
	Emit   func(bus.Event)
	Now    func() time.Time
	// Every is the token loop's interval; 0 means no background loop and no
	// window timer (tests tick by hand). Commands check the token at most
	// once per TokenEvery either way.
	Every time.Duration
	// Auditors may read any session's evidence (besides its own Ops agent).
	Auditors []string

	mu       sync.Mutex
	sessions map[string]*stationSession
}

type stationSession struct {
	mu      sync.Mutex
	booking store.Booking
	mandate schema.Mandate
	chain   *chain.Chain
	acks    map[int64]schema.Ack
	missing []int64
	monitor *Monitor
	stop    context.CancelFunc
	timer   *time.Timer
	closed  bool
}

// Relay runs the station's per-command checks (9.2, 9.3), relays the
// command to the spacecraft without reading or changing its body, appends
// the CommandRecord and returns the Ack. A relayed command with no valid Ack
// is a suspected drop (ACK_MISSING / ACK_PARSE_ERROR).
func (m *Manager) Relay(ctx context.Context, bookingID, token string) (schema.Ack, error) {
	caller, ok := m.Caller(ctx)
	if !ok {
		return schema.Ack{}, errs.New(errs.DPoPRejectedKey, "no proven caller")
	}
	s, err := m.session(ctx, bookingID)
	if err != nil {
		return schema.Ack{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := m.Now()
	end := time.Unix(s.mandate.Exp, 0).Add(Slack)
	if s.closed || now.After(end) {
		m.endLocked(s)
		return schema.Ack{}, errs.New(errs.WindowClosed, "the pass window has closed")
	}
	if now.Before(time.Unix(s.mandate.Nbf, 0).Add(-Slack)) {
		return schema.Ack{}, errs.New(errs.WindowClosed, "the pass window has not opened")
	}
	if caller.JKT != s.mandate.JKT {
		return schema.Ack{}, errs.New(errs.DPoPRejectedKey, "DPoP key is not the mandate's jkt")
	}
	if err := m.watch(ctx, s, caller.AgentID); err != nil {
		return schema.Ack{}, err
	}
	cmd, err := schema.PeekCommand(token)
	if err != nil {
		return schema.Ack{}, err
	}
	if !contains(s.mandate.CommandClasses, cmd.Class) {
		return schema.Ack{}, errs.New(errs.ClassRejected, fmt.Sprintf("class %q is not in the mandate's %v", cmd.Class, s.mandate.CommandClasses))
	}
	if cmd.MandateID != s.booking.MandateID {
		return schema.Ack{}, errs.New(errs.CommandRejectedMandID, "command names another mandate")
	}
	if len(s.chain.Records()) > 0 && cmd.Counter <= lastCounter(s.chain) {
		return schema.Ack{}, errs.New(errs.CommandRejectedCounter, "counter not above the last relayed one")
	}
	// The session ends at exp+30 s even mid-command (9.8).
	upCtx, cancel := context.WithTimeout(ctx, end.Sub(now))
	defer cancel()
	ack, uerr := m.Uplink.Uplink(upCtx, token)
	if _, err := s.chain.Append(cmd.Counter, cmd.MandateID, cmd.Class, chain.CmdSHA256(token)); err != nil {
		return schema.Ack{}, err
	}
	if uerr == nil {
		if verr := ack.Validate(); verr != nil {
			uerr = errs.New(errs.AckParseError, verr.Error())
		}
	}
	if uerr != nil || ack.Counter != cmd.Counter {
		s.missing = append(s.missing, cmd.Counter)
		m.emit("suspected_drop", bookingID, "warn", string(errs.AckMissing), map[string]any{"counter": cmd.Counter})
		return schema.Ack{}, errs.New(errs.AckMissing, "no valid ack from the spacecraft")
	}
	s.acks[cmd.Counter] = ack
	m.emit("command_relayed", bookingID, ack.Result, ack.Reason, map[string]any{"counter": cmd.Counter, "class": cmd.Class})
	return ack, nil
}

// watch opens the Ops token check on the first command and re-checks it at
// most once per TokenEvery after that (9.7). A failed first fetch refuses
// the command without cutting, so the next command retries.
func (m *Manager) watch(ctx context.Context, s *stationSession, opsAgentID string) error {
	if s.monitor == nil {
		mon := &Monitor{Keeper: m.Keeper, AgentID: opsAgentID, Self: m.ANSName, Subject: s.booking.BookingID, Emit: m.Emit}
		if code := mon.Tick(ctx); code != "" {
			return errs.New(code, "Ops agent's status token")
		}
		s.monitor = mon
		m.startLoop(s)
		return nil
	}
	if code := s.monitor.TickIfDue(ctx, TokenEvery); code != "" {
		return errs.New(code, "Ops agent's status token")
	}
	return nil
}

// startLoop runs the background token loop and the window-end timer.
func (m *Manager) startLoop(s *stationSession) {
	if m.Every <= 0 {
		return
	}
	loop, stop := context.WithCancel(context.Background())
	s.stop = stop
	go s.monitor.Run(loop, m.Every)
	end := time.Unix(s.mandate.Exp, 0).Add(Slack)
	s.timer = time.AfterFunc(end.Sub(m.Now()), func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		m.endLocked(s)
	})
}

// Tick runs one token check for a booking's session (tests; production
// sessions tick on their own every Every).
func (m *Manager) Tick(ctx context.Context, bookingID string) errs.Code {
	m.mu.Lock()
	s := m.sessions[bookingID]
	m.mu.Unlock()
	if s == nil {
		return ""
	}
	s.mu.Lock()
	mon := s.monitor
	s.mu.Unlock()
	if mon == nil {
		return ""
	}
	return mon.Tick(ctx)
}

// Evidence returns the station's record of a pass to the booking's Ops agent
// or a configured auditor.
func (m *Manager) Evidence(bookingID, callerANS string) (Evidence, error) {
	m.mu.Lock()
	s := m.sessions[bookingID]
	m.mu.Unlock()
	if s == nil {
		return Evidence{}, errs.New(errs.SessionRejectedBooked, "no session for that booking")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if callerANS != s.mandate.Sub && !contains(m.Auditors, callerANS) {
		return Evidence{}, errs.New(errs.SessionRejectedCaller, "only the booking's Ops agent or an auditor may read its evidence")
	}
	e := Evidence{BookingID: bookingID, Head: s.chain.Head(), Records: s.chain.Records(),
		MissingAcks: append([]int64(nil), s.missing...)}
	for _, r := range e.Records {
		if a, ok := s.acks[r.Counter]; ok {
			e.Acks = append(e.Acks, a)
		}
	}
	if s.monitor != nil {
		code, _ := s.monitor.Cut()
		e.Cut = string(code)
	}
	return e, nil
}

// session returns the booking's session, loading the booking outside the
// manager lock.
func (m *Manager) session(ctx context.Context, bookingID string) (*stationSession, error) {
	m.mu.Lock()
	if s, ok := m.sessions[bookingID]; ok {
		m.mu.Unlock()
		return s, nil
	}
	m.mu.Unlock()
	b, found, err := m.Store.GetBooking(ctx, bookingID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errs.New(errs.SessionRejectedBooked, "no such booking")
	}
	// The mandate was fully verified at booking; re-read its fields.
	md, err := schema.PeekMandate(b.Mandate)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions == nil {
		m.sessions = map[string]*stationSession{}
	}
	if s, ok := m.sessions[bookingID]; ok {
		return s, nil
	}
	s := &stationSession{booking: b, mandate: md, chain: chain.New(), acks: map[int64]schema.Ack{}}
	m.sessions[bookingID] = s
	return s, nil
}

// endLocked closes a session at exp+30 s (9.8): no more commands, the token
// loop stops, window_closed is emitted once. The evidence stays readable.
// s.mu is held.
func (m *Manager) endLocked(s *stationSession) {
	if s.closed {
		return
	}
	s.closed = true
	if s.stop != nil {
		s.stop()
	}
	if s.timer != nil {
		s.timer.Stop()
	}
	m.emit("window_closed", s.booking.BookingID, "closed", string(errs.WindowClosed), nil)
}

// Close stops every token loop and timer.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		if s.stop != nil {
			s.stop()
		}
		if s.timer != nil {
			s.timer.Stop()
		}
	}
}

func (m *Manager) emit(kind, subject, result, reason string, data map[string]any) {
	if m.Emit != nil {
		m.Emit(bus.Event{Agent: m.ANSName, Kind: kind, Subject: subject, Result: result, Reason: reason, Data: data})
	}
}

func lastCounter(c *chain.Chain) int64 {
	rs := c.Records()
	return rs[len(rs)-1].Counter
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
