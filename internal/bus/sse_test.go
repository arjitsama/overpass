package bus

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// An idle stream must survive its own heartbeats. The write deadline used to be
// armed before the select that waits for the tick, so the budget was already
// spent when the ping was written and every idle client dropped at ~writeWait.
func TestIdleStreamSurvivesHeartbeats(t *testing.T) {
	oldHeartbeat, oldWriteWait := heartbeat, writeWait
	heartbeat, writeWait = 30*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { heartbeat, writeWait = oldHeartbeat, oldWriteWait })

	b := New(8, 4)
	t.Cleanup(b.Close)
	srv := httptest.NewServer(b.Handler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	pings := make(chan int, 1)
	go func() {
		n := 0
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), ": ping") {
				n++
				if n == 3 {
					pings <- n
					return
				}
			}
		}
		pings <- n
	}()

	select {
	case n := <-pings:
		if n < 3 {
			t.Fatalf("stream closed after %d heartbeats; want at least 3", n)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for three heartbeats")
	}
}
