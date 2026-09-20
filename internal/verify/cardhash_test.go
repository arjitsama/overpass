package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/agentnameservice/ans-sdk-go/verify/scitt"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/config"
)

// cardHashRun builds a peerRun at the card_hash stage: the served card bytes,
// the status token payload, and whether the receipt verified.
func cardHashRun(t *testing.T, card []byte, hashes map[string]string, receiptOK bool) *peerRun {
	t.Helper()
	v, err := New(config.Config{}, Options{Self: "ops", Emit: func(bus.Event) {}})
	if err != nil {
		t.Fatal(err)
	}
	return &peerRun{v: v, res: &Result{Host: "x.example"}, card: card, receiptOK: receiptOK,
		token: &Token{Payload: scitt.StatusTokenPayload{MetadataHashes: hashes}}}
}

func lastCheck(r *peerRun) Check { return r.res.Checks[len(r.res.Checks)-1] }

// No registered hash is tolerated only when the log entry's receipt verified
// AND the card signature bound the card to a log-attested key.
func TestCardHashNotRegisteredVerifiedEntryIsWarn(t *testing.T) {
	r := cardHashRun(t, []byte(`{"name":"x"}`), nil, true)
	r.cardHash(true)
	c := lastCheck(r)
	if c.Name != CheckCardHash || c.Verdict != Warn || c.Reason != ReasonCardHashNotRegistered {
		t.Fatalf("check %+v", c)
	}
}

// An unverified log entry never softens the missing hash to a Warn, and
// neither does a failed card signature.
func TestCardHashNotRegisteredUnverifiedEntryIsFail(t *testing.T) {
	r := cardHashRun(t, []byte(`{"name":"x"}`), nil, false)
	r.cardHash(true)
	if c := lastCheck(r); c.Verdict != Fail {
		t.Fatalf("receipt not verified: %+v", c)
	}
	r = cardHashRun(t, []byte(`{"name":"x"}`), nil, true)
	r.cardHash(false)
	if c := lastCheck(r); c.Verdict != Fail {
		t.Fatalf("signature failed: %+v", c)
	}
}

// A registered hash that does not match the served card is always a Fail,
// even with a verified receipt and a good signature; a matching one passes.
func TestCardHashMismatchIsFail(t *testing.T) {
	card := []byte(`{"name":"x"}`)
	r := cardHashRun(t, card, map[string]string{"0": "SHA256:" + hex.EncodeToString(make([]byte, 32))}, true)
	r.cardHash(true)
	if c := lastCheck(r); c.Verdict != Fail {
		t.Fatalf("mismatch: %+v", c)
	}
	sum := sha256.Sum256(card)
	r = cardHashRun(t, card, map[string]string{"0": "SHA256:" + hex.EncodeToString(sum[:])}, false)
	r.cardHash(false)
	if c := lastCheck(r); c.Verdict != Pass {
		t.Fatalf("match: %+v", c)
	}
}
