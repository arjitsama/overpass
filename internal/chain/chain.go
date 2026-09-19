// Package chain is the command hash chain Ops and the station both keep.
//
//	hash = SHA-256(prev_hash || JCS(record without hash))
//
// prev_hash enters the hash as its 32 raw bytes; the genesis prev_hash is 32
// zero bytes. Records carry no timestamps, so two sides that saw the same
// commands in the same order hold the same head, and any drop, insertion or
// reorder changes it.
package chain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/jose"
	"github.com/arjitsama/overpass/internal/schema"
)

// Genesis is the prev_hash of the first record.
var Genesis = hex.EncodeToString(make([]byte, sha256.Size))

// unhashed is a record without its hash: the exact bytes that get hashed.
type unhashed struct {
	Counter   int64  `json:"counter"`
	MandateID string `json:"mandate_id"`
	Class     string `json:"class"`
	CmdSHA256 string `json:"cmd_sha256"`
	PrevHash  string `json:"prev_hash"`
}

// Chain appends records. The zero value is not usable; call New. A Chain is
// not safe for concurrent use: callers sharing one across handlers (Phase 5)
// must serialize access.
type Chain struct {
	head    string
	counter int64
	records []schema.CommandRecord
}

// New returns an empty chain at Genesis.
func New() *Chain { return &Chain{head: Genesis} }

// Head is the hash of the newest record, or Genesis.
func (c *Chain) Head() string { return c.head }

// Records returns a copy of the records so far.
func (c *Chain) Records() []schema.CommandRecord {
	return append([]schema.CommandRecord(nil), c.records...)
}

// CmdSHA256 is the cmd_sha256 of a command: SHA-256 of its compact JWS, as
// relayed. Both sides hash the same token bytes.
func CmdSHA256(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Append adds the record for one command. Counters must strictly increase.
func (c *Chain) Append(counter int64, mandateID, class, cmdSHA256 string) (schema.CommandRecord, error) {
	if counter <= c.counter {
		return schema.CommandRecord{}, errs.New(errs.ChainMismatch,
			fmt.Sprintf("counter %d not above %d", counter, c.counter))
	}
	r, err := link(unhashed{Counter: counter, MandateID: mandateID, Class: class, CmdSHA256: cmdSHA256, PrevHash: c.head})
	if err != nil {
		return schema.CommandRecord{}, err
	}
	c.head, c.counter = r.Hash, counter
	c.records = append(c.records, r)
	return r, nil
}

// link computes the hash of u and returns the full, validated record.
func link(u unhashed) (schema.CommandRecord, error) {
	prev, err := hex.DecodeString(u.PrevHash)
	if err != nil || len(prev) != sha256.Size {
		return schema.CommandRecord{}, errs.New(errs.RecordParseError, "prev_hash is not 32 bytes of hex")
	}
	body, err := jose.Canonicalize(u)
	if err != nil {
		return schema.CommandRecord{}, errs.New(errs.RecordParseError, err.Error())
	}
	h := sha256.New()
	h.Write(prev)
	h.Write(body)
	r := schema.CommandRecord{Counter: u.Counter, MandateID: u.MandateID, Class: u.Class,
		CmdSHA256: u.CmdSHA256, PrevHash: u.PrevHash, Hash: hex.EncodeToString(h.Sum(nil))}
	if err := r.Validate(); err != nil {
		return schema.CommandRecord{}, errs.New(errs.RecordParseError, err.Error())
	}
	return r, nil
}

// Walk re-verifies records from Genesis and returns the head. A broken link,
// wrong hash or non-increasing counter is CHAIN_MISMATCH; a record whose
// fields are malformed is RECORD_PARSE_ERROR.
func Walk(records []schema.CommandRecord) (string, error) {
	head, last := Genesis, int64(0)
	for i, r := range records {
		if r.PrevHash != head {
			return "", errs.New(errs.ChainMismatch, fmt.Sprintf("record %d does not link to the previous one", i))
		}
		if r.Counter <= last {
			return "", errs.New(errs.ChainMismatch, fmt.Sprintf("record %d counter %d not above %d", i, r.Counter, last))
		}
		want, err := link(unhashed{Counter: r.Counter, MandateID: r.MandateID, Class: r.Class, CmdSHA256: r.CmdSHA256, PrevHash: r.PrevHash})
		if err != nil {
			return "", err
		}
		if want.Hash != r.Hash {
			return "", errs.New(errs.ChainMismatch, fmt.Sprintf("record %d hash does not match its contents", i))
		}
		head, last = r.Hash, r.Counter
	}
	return head, nil
}
