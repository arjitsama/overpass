package schema

import (
	"os"
	"regexp"
	"testing"

	"github.com/arjitsama/overpass/internal/errs"
)

// TestSchemasDocExamples decodes every tagged example in docs/schemas.md with
// the real strict decoder, so the frozen doc and the code cannot drift.
func TestSchemasDocExamples(t *testing.T) {
	doc, err := os.ReadFile("../../docs/schemas.md")
	if err != nil {
		t.Fatal(err)
	}
	decoders := map[string]func([]byte) error{
		"quote":   func(b []byte) error { _, err := DecodeQuote(b); return err },
		"mandate": func(b []byte) error { return Decode(b, &Mandate{}, errs.MandateParseError) },
		"satreg":  func(b []byte) error { return Decode(b, &SatRegistry{}, errs.SatRegParseError) },
		"command": func(b []byte) error { return Decode(b, &Command{}, errs.CommandParseError) },
		"record":  func(b []byte) error { _, err := DecodeCommandRecord(b); return err },
		"ack":     func(b []byte) error { _, err := DecodeAck(b); return err },
		"audit":   func(b []byte) error { return Decode(b, &AuditReport{}, errs.AuditParseError) },
		"receipt": func(b []byte) error { return Decode(b, &BookingReceipt{}, errs.ReceiptParseError) },
	}
	blocks := regexp.MustCompile("(?s)```json (\\w+)\n(.*?)```").FindAllSubmatch(doc, -1)
	seen := map[string]bool{}
	for _, b := range blocks {
		name := string(b[1])
		dec, ok := decoders[name]
		if !ok {
			t.Errorf("example %q has no decoder", name)
			continue
		}
		if err := dec(b[2]); err != nil {
			t.Errorf("example %q: %v", name, err)
		}
		seen[name] = true
	}
	for name := range decoders {
		if !seen[name] {
			t.Errorf("docs/schemas.md has no example for %q", name)
		}
	}
}
