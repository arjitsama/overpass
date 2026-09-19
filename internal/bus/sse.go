package bus

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/arjitsama/overpass/internal/errs"
)

const (
	// heartbeat keeps idle connections open through proxies.
	heartbeat = 15 * time.Second
	// writeWait bounds each write so a stalled client cannot pin a handler
	// (and its subscriber slot) or hold up shutdown.
	writeWait = 10 * time.Second
)

// Handler serves the bus as text/event-stream. It honors Last-Event-ID.
func (b *Bus) Handler() http.Handler {
	return http.HandlerFunc(b.serveSSE)
}

func (b *Bus) serveSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		errs.Write(w, http.StatusMethodNotAllowed, errs.MethodNotAllowed, "use GET")
		return
	}
	after, err := lastEventID(r)
	if err != nil {
		errs.Write(w, http.StatusBadRequest, errs.BadRequest, "Last-Event-ID must be a non-negative integer")
		return
	}
	rc := http.NewResponseController(w)
	backlog, ch, cancel, err := b.Subscribe(after)
	if err != nil {
		detail := "event bus unavailable"
		var e *errs.Error
		if errors.As(err, &e) {
			detail = e.Detail
		}
		errs.Write(w, http.StatusServiceUnavailable, errs.Unavailable, detail)
		return
	}
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	_ = rc.SetWriteDeadline(time.Now().Add(writeWait))
	w.WriteHeader(http.StatusOK)
	for _, e := range backlog {
		if writeEvent(w, e) != nil {
			return
		}
	}
	if rc.Flush() != nil {
		return
	}
	b.stream(w, r, rc, ch)
}

func (b *Bus) stream(w http.ResponseWriter, r *http.Request, rc *http.ResponseController, ch <-chan Event) {
	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for {
		var err error
		_ = rc.SetWriteDeadline(time.Now().Add(writeWait))
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			err = writeEvent(w, e)
		case <-tick.C:
			_, err = fmt.Fprint(w, ": ping\n\n")
		}
		if err != nil || rc.Flush() != nil {
			return
		}
	}
}

func lastEventID(r *http.Request) (uint64, error) {
	v := r.Header.Get("Last-Event-ID")
	if v == "" {
		return 0, nil
	}
	return strconv.ParseUint(v, 10, 64)
}

func writeEvent(w http.ResponseWriter, e Event) error {
	_, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.ID, e.raw)
	return err
}
