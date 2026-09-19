package spacecraft

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/schema"
	"github.com/arjitsama/overpass/internal/store"
)

func cmd(t *testing.T, k *ecdsa.PrivateKey, counter int64) string {
	tok, err := schema.SignCommand(schema.Command{NoradID: 27844, Counter: counter, MandateID: "m-1", Class: "telemetry",
		Body: json.RawMessage(`{"op":"set_beacon","beacon_s":60}`), IssuedAt: 1790000000}, k)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// The last accepted counter survives a restart, and of many concurrent
// uplinks of one counter exactly one is accepted.
func TestCounterPersistsAndIsAtomic(t *testing.T) {
	ctx := context.Background()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	path := filepath.Join(t.TempDir(), "sc.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sc, _ := New(ctx, 27844, []*ecdsa.PublicKey{&k.PublicKey}, st)
	if a, _ := sc.Uplink(ctx, cmd(t, k, 5)); a.Result != schema.AckAccepted || sc.State().BeaconS != 60 {
		t.Fatalf("%+v", a)
	}
	st.Close()
	st2, _ := store.Open(path)
	defer st2.Close()
	sc2, _ := New(ctx, 27844, []*ecdsa.PublicKey{&k.PublicKey}, st2)
	if a, _ := sc2.Uplink(ctx, cmd(t, k, 5)); a.Reason != string(errs.CommandRejectedCounter) {
		t.Fatalf("replay after restart: %+v", a)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	tok := cmd(t, k, 6)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a, _ := sc2.Uplink(ctx, tok); a.Result == schema.AckAccepted {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("%d accepted", accepted.Load())
	}
	if _, err := sc2.Uplink(ctx, "garbage"); !errs.Is(err, errs.CommandParseError) {
		t.Fatalf("garbage: %v", err)
	}
}
