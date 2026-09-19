package chain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/schema"
)

type cmd struct {
	counter int64
	class   string
	token   string
}

func cmds(n int) []cmd {
	out := make([]cmd, n)
	for i := range out {
		out[i] = cmd{counter: int64(i + 1), class: "telemetry", token: "tok-" + strconv.Itoa(i)}
	}
	return out
}

func build(t *testing.T, cs []cmd) *Chain {
	t.Helper()
	c := New()
	for _, x := range cs {
		if _, err := c.Append(x.counter, "m-1", x.class, CmdSHA256(x.token)); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

// Acceptance 5.
func TestChainDeterministic(t *testing.T) {
	cs := cmds(5)
	ops, station := build(t, cs), build(t, cs)
	if ops.Head() != station.Head() || ops.Head() == Genesis {
		t.Fatalf("heads differ: %s vs %s", ops.Head(), station.Head())
	}

	dropped := append(append([]cmd{}, cs[:2]...), cs[3:]...)
	if build(t, dropped).Head() == ops.Head() {
		t.Fatal("dropping a command kept the head")
	}

	// Reorder: swap the payloads of commands 2 and 3 (counters stay increasing).
	swapped := append([]cmd{}, cs...)
	swapped[1].token, swapped[2].token = cs[2].token, cs[1].token
	if build(t, swapped).Head() == ops.Head() {
		t.Fatal("reordering commands kept the head")
	}
}

// Pins the hash definition so it cannot drift silently:
// SHA-256(32 zero bytes || JCS(record without hash)).
func TestHashDefinition(t *testing.T) {
	c := New()
	r, err := c.Append(1, "m-1", "telemetry", CmdSHA256("x"))
	if err != nil {
		t.Fatal(err)
	}
	jcs := `{"class":"telemetry","cmd_sha256":"` + CmdSHA256("x") + `","counter":1,"mandate_id":"m-1","prev_hash":"` + Genesis + `"}`
	sum := sha256.Sum256(append(make([]byte, 32), jcs...))
	if r.Hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash %s, want %x", r.Hash, sum)
	}
}

func TestAppendRejectsNonIncreasingCounter(t *testing.T) {
	c := build(t, cmds(2))
	_, err := c.Append(2, "m-1", "telemetry", CmdSHA256("again"))
	if !errs.Is(err, errs.ChainMismatch) {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.Append(3, "m-1", "BAD CLASS", CmdSHA256("x")); !errs.Is(err, errs.RecordParseError) {
		t.Fatalf("bad class err = %v", err)
	}
}

func TestWalk(t *testing.T) {
	c := build(t, cmds(4))
	head, err := Walk(c.Records())
	if err != nil || head != c.Head() {
		t.Fatalf("walk head %s err %v", head, err)
	}
	if h, err := Walk(nil); err != nil || h != Genesis {
		t.Fatalf("empty walk: %s %v", h, err)
	}

	mutations := map[string]func([]schema.CommandRecord){
		"drop":       func(r []schema.CommandRecord) { copy(r[1:], r[2:]) },
		"edit class": func(r []schema.CommandRecord) { r[1].Class = "reboot" },
		"edit hash":  func(r []schema.CommandRecord) { r[3].Hash = Genesis },
		"swap":       func(r []schema.CommandRecord) { r[1], r[2] = r[2], r[1] },
	}
	for name, mut := range mutations {
		t.Run(name, func(t *testing.T) {
			recs := c.Records()
			mut(recs)
			if _, err := Walk(recs); !errs.Is(err, errs.ChainMismatch) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestRecordsRoundTripStrictly(t *testing.T) {
	c := build(t, cmds(1))
	raw, _ := json.Marshal(c.Records()[0])
	r, err := schema.DecodeCommandRecord(raw)
	if err != nil || r != c.Records()[0] {
		t.Fatalf("decode: %+v %v", r, err)
	}
}
