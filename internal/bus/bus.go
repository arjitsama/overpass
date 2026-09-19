// Package bus is an in-process event bus. Every agent publishes what it does;
// the dashboard reads it over server-sent events at /events.
package bus

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/arjitsama/overpass/internal/errs"
)

// Defaults for New.
const (
	DefaultBacklog     = 256
	DefaultMaxSubs     = 64
	subscriberChanSize = 64
)

// Event is one thing that happened. TS is epoch milliseconds.
type Event struct {
	ID      uint64         `json:"-"`
	TS      int64          `json:"ts"`
	Agent   string         `json:"agent"`
	Kind    string         `json:"kind"`
	Subject string         `json:"subject"`
	Result  string         `json:"result"`
	Reason  string         `json:"reason"`
	Data    map[string]any `json:"data,omitempty"`

	raw []byte // JSON encoding, fixed at Publish so later Data edits cannot race
}

// Bus fans events out to subscribers and keeps a short backlog so a
// subscriber that connects late, or reconnects, can catch up.
type Bus struct {
	mu      sync.Mutex
	nextID  uint64
	backlog []Event
	size    int
	maxSubs int
	subs    map[chan Event]struct{}
	closed  bool
	now     func() time.Time
}

// New returns a bus that keeps the last backlog events and allows at most
// maxSubs concurrent subscribers.
func New(backlog, maxSubs int) *Bus {
	return &Bus{
		size:    backlog,
		maxSubs: maxSubs,
		subs:    make(map[chan Event]struct{}),
		now:     time.Now,
	}
}

// Publish assigns an ID (and TS if unset) and delivers e. It never blocks:
// a subscriber whose buffer is full is disconnected and can resume with
// Last-Event-ID from the backlog. Events whose Data cannot be encoded as
// JSON are rejected with bad_request; after Close, Publish is a no-op.
func (b *Bus) Publish(e Event) (Event, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return e, nil
	}
	if e.TS == 0 {
		e.TS = b.now().UnixMilli()
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return e, errs.New(errs.BadRequest, fmt.Sprintf("event %q not encodable: %v", e.Kind, err))
	}
	b.nextID++
	e.ID, e.raw = b.nextID, raw
	b.backlog = append(b.backlog, e)
	if len(b.backlog) > b.size {
		b.backlog = b.backlog[len(b.backlog)-b.size:]
	}
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
			delete(b.subs, ch)
			close(ch)
		}
	}
	return e, nil
}

// Subscribe returns the backlog after afterID and a channel of later events.
// An afterID beyond the newest event came from a previous boot of this agent
// (IDs restart at 1), so the whole backlog is replayed.
// The channel closes when the bus closes or the subscriber falls behind.
// cancel releases the subscription and is safe to call more than once.
func (b *Bus) Subscribe(afterID uint64) (backlog []Event, ch <-chan Event, cancel func(), err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, nil, nil, errs.New(errs.Unavailable, "event bus is shutting down")
	}
	if len(b.subs) >= b.maxSubs {
		return nil, nil, nil, errs.New(errs.Unavailable, "too many event subscribers")
	}
	if afterID > b.nextID {
		afterID = 0
	}
	for _, e := range b.backlog {
		if e.ID > afterID {
			backlog = append(backlog, e)
		}
	}
	c := make(chan Event, subscriberChanSize)
	b.subs[c] = struct{}{}
	return backlog, c, func() { b.unsubscribe(c) }, nil
}

func (b *Bus) unsubscribe(c chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subs[c]; ok {
		delete(b.subs, c)
		close(c)
	}
}

// Close disconnects every subscriber and refuses new ones. Call it before
// http.Server.Shutdown so open SSE streams end.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for c := range b.subs {
		delete(b.subs, c)
		close(c)
	}
}
