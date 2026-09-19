package authority

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/schema"
	"github.com/arjitsama/overpass/internal/station"
	"github.com/arjitsama/overpass/internal/store"
)

// Acceptance 1 across both agents: the station quotes, the authority issues
// a mandate for that quote, the station books it once, and the same mandate
// is then MANDATE_REJECTED:consumed.
func TestQuoteMandateBook(t *testing.T) {
	f := newFixture(t)
	db, err := store.Open(filepath.Join(t.TempDir(), "gs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gsName := "ans://v0.1.0.gs-blacksburg.example"
	gs := &station.Station{ANSName: gsName, Key: f.key, Store: db,
		Pricing: config.Pricing{PerMinuteCents: 100, PayTo: "0xabc", Network: "base-sepolia", Asset: "USDC", AssetDecimals: 6},
		Registry: schema.SatRegistry{Iss: authName, Iat: now.Unix(), Satellites: []schema.Satellite{
			{NoradID: 27844, AuthorityANSName: authName, OpsANSNames: []string{opsName}}}},
		Keys: station.StaticKeys{authName: {&f.key.PublicKey}}, Caller: f.a.Caller,
		Now: func() time.Time { return now }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	qArgs := fmt.Sprintf(`{"skill":"get_pass_quote","norad_id":27844,"aos":%d,"los":%d,"mode":"uplink","max_elevation_deg":52}`,
		now.Unix()+1800, now.Unix()+2340)
	out, err := gs.GetPassQuote(f.ctx, json.RawMessage(qArgs))
	if err != nil {
		t.Fatal(err)
	}
	q := out.(schema.Quote)
	tok, err := f.issue(f.ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	book := func() error {
		args, _ := json.Marshal(map[string]string{"skill": "book_pass", "quote_id": q.QuoteID, "mandate": tok})
		_, err := gs.BookPass(f.ctx, args)
		return err
	}
	if err := book(); err != nil {
		t.Fatalf("first booking: %v", err)
	}
	if err := book(); !errs.Is(err, errs.MandateRejectedConsumed) {
		t.Fatalf("second booking: %v", err)
	}
}
