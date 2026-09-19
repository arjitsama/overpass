package store

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// The replay cache reports a live duplicate, lets an expired one back in,
// and admits exactly one of many concurrent uses of a jti.
func TestReplayCache(t *testing.T) {
	s := open(t)
	now := time.Unix(1_790_000_000, 0)
	s.now = func() time.Time { return now }
	rc := s.ReplayCache()
	if seen, err := rc.CheckAndStore("k1", now.Add(time.Minute)); seen || err != nil {
		t.Fatalf("first use: %v %v", seen, err)
	}
	if seen, _ := rc.CheckAndStore("k1", now.Add(time.Minute)); !seen {
		t.Fatal("replay not seen")
	}
	s.now = func() time.Time { return now.Add(2 * time.Minute) }
	if seen, _ := rc.CheckAndStore("k1", now.Add(3*time.Minute)); seen {
		t.Fatal("expired entry still blocks")
	}
	var fresh atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if seen, err := rc.CheckAndStore("k2", now.Add(time.Hour)); err == nil && !seen {
				fresh.Add(1)
			}
		}()
	}
	wg.Wait()
	if fresh.Load() != 1 {
		t.Fatalf("%d concurrent first uses", fresh.Load())
	}
}

func TestDailyLimit(t *testing.T) {
	s := open(t)
	nbf := int64(1_790_000_000)
	for i := 0; i < 2; i++ {
		if _, err := s.RecordMandate(t.Context(), "m-"+string(rune('a'+i)), 27844, nbf, "gs", 2); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.RecordMandate(t.Context(), "m-c", 27844, nbf, "gs", 2); err == nil {
		t.Fatal("third mandate on the same day allowed")
	}
	if _, err := s.RecordMandate(t.Context(), "m-d", 27844, nbf+86400, "gs", 2); err != nil {
		t.Fatalf("next day refused: %v", err)
	}
}
