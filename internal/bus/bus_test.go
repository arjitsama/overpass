package bus

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arjitsama/overpass/internal/errs"
)

func mustPublish(t *testing.T, b *Bus, kind string) Event {
	t.Helper()
	e, err := b.Publish(Event{Agent: "ops", Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func wantCode(t *testing.T, err error, code errs.Code) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
}

func TestPublishAssignsIDAndTS(t *testing.T) {
	b := New(8, 4)
	b.now = func() time.Time { return time.UnixMilli(1700000000123) }
	e1 := mustPublish(t, b, "a")
	e2 := mustPublish(t, b, "b")
	if e1.ID != 1 || e2.ID != 2 || e1.TS != 1700000000123 {
		t.Fatalf("got %+v %+v", e1, e2)
	}
}

func TestPublishRejectsUnencodable(t *testing.T) {
	b := New(8, 4)
	_, err := b.Publish(Event{Kind: "x", Data: map[string]any{"f": math.Inf(1)}})
	wantCode(t, err, errs.BadRequest)
	if bl, _, cancel, _ := b.Subscribe(0); len(bl) != 0 {
		t.Fatalf("rejected event reached backlog: %v", bl)
	} else {
		cancel()
	}
}

func TestSubscribeBacklogAfterID(t *testing.T) {
	b := New(3, 4)
	for _, k := range []string{"a", "b", "c", "d"} {
		mustPublish(t, b, k)
	}
	bl, _, cancel, err := b.Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if len(bl) != 3 || bl[0].Kind != "b" {
		t.Fatalf("backlog = %+v, want last 3 starting at b", bl)
	}
	bl2, _, cancel2, _ := b.Subscribe(3)
	defer cancel2()
	if len(bl2) != 1 || bl2[0].Kind != "d" {
		t.Fatalf("after 3 = %+v", bl2)
	}
}

// A Last-Event-ID from before a restart is beyond the newest ID; the client
// must still get the new agent_started instead of silence.
func TestSubscribeStaleIDReplaysAll(t *testing.T) {
	b := New(8, 4)
	mustPublish(t, b, "agent_started")
	bl, _, cancel, err := b.Subscribe(57)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if len(bl) != 1 || bl[0].Kind != "agent_started" {
		t.Fatalf("backlog = %+v", bl)
	}
}

func TestLiveDeliveryInOrder(t *testing.T) {
	b := New(8, 4)
	_, ch, cancel, _ := b.Subscribe(0)
	defer cancel()
	mustPublish(t, b, "a")
	mustPublish(t, b, "b")
	if (<-ch).Kind != "a" || (<-ch).Kind != "b" {
		t.Fatal("out of order")
	}
}

func TestSlowSubscriberDisconnected(t *testing.T) {
	b := New(8, 4)
	_, ch, cancel, _ := b.Subscribe(0)
	defer cancel()
	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberChanSize+1; i++ {
			mustPublish(t, b, "x")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}
	n := 0
	for range ch {
		n++
	}
	if n != subscriberChanSize {
		t.Fatalf("drained %d, want %d then close", n, subscriberChanSize)
	}
}

func TestSubscriberCapAndClose(t *testing.T) {
	b := New(8, 1)
	_, ch, cancel, err := b.Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = b.Subscribe(0)
	wantCode(t, err, errs.Unavailable)

	b.Close()
	if _, ok := <-ch; ok {
		t.Fatal("channel open after Close")
	}
	cancel() // after Close: must not panic on double close
	_, _, _, err = b.Subscribe(0)
	wantCode(t, err, errs.Unavailable)
	if _, err := b.Publish(Event{Kind: "late"}); err != nil {
		t.Fatalf("Publish after Close = %v", err)
	}
}

func TestConcurrentPublishSubscribe(t *testing.T) {
	b := New(16, 64)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _ = b.Publish(Event{Kind: "x"})
			}
		}()
		go func() {
			defer wg.Done()
			_, ch, cancel, err := b.Subscribe(0)
			if err != nil {
				return
			}
			for j := 0; j < 10; j++ {
				<-ch
			}
			cancel()
			cancel()
		}()
	}
	wg.Wait()
}

func readSSEEvent(t *testing.T, r *bufio.Reader) (id string, e Event) {
	t.Helper()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(line, "id: "):
			id = line[4:]
		case strings.HasPrefix(line, "data: "):
			if err := json.Unmarshal([]byte(line[6:]), &e); err != nil {
				t.Fatalf("data not JSON: %v", err)
			}
		case line == "" && id != "":
			return id, e
		}
	}
}

func TestHandlerReplaysBacklog(t *testing.T) {
	b := New(8, 4)
	mustPublish(t, b, "agent_started")
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()

	ctx, cancelReq := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelReq()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}
	r := bufio.NewReader(resp.Body)
	id, e := readSSEEvent(t, r)
	if id != "1" || e.Kind != "agent_started" || e.Agent != "ops" {
		t.Fatalf("got id=%s %+v", id, e)
	}
	mustPublish(t, b, "live")
	if _, e := readSSEEvent(t, r); e.Kind != "live" {
		t.Fatalf("live event = %+v", e)
	}
	b.Close()
	if _, err := r.ReadString('\n'); err == nil {
		t.Fatal("stream still open after Close")
	}
}

func TestHandlerLastEventID(t *testing.T) {
	b := New(8, 4)
	mustPublish(t, b, "a")
	mustPublish(t, b, "b")
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()
	defer b.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Last-Event-ID", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if id, e := readSSEEvent(t, bufio.NewReader(resp.Body)); id != "2" || e.Kind != "b" {
		t.Fatalf("got id=%s %+v", id, e)
	}
}

func TestHandlerRejections(t *testing.T) {
	b := New(8, 4)
	h := b.Handler()
	cases := []struct {
		method, lastID string
		status         int
		code           errs.Code
	}{
		{http.MethodPost, "", http.StatusMethodNotAllowed, errs.MethodNotAllowed},
		{http.MethodGet, "-1", http.StatusBadRequest, errs.BadRequest},
		{http.MethodGet, "abc", http.StatusBadRequest, errs.BadRequest},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "/events", nil)
		if c.lastID != "" {
			req.Header.Set("Last-Event-ID", c.lastID)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var body errs.Error
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != c.status || body.Code != c.code {
			t.Fatalf("%s %q: %d %+v", c.method, c.lastID, rec.Code, body)
		}
	}
	b.Close()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/events", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("after Close: %d", rec.Code)
	}
}
