// Package store is one agent's SQLite database (modernc.org/sqlite, pure
// Go): quotes, bookings, consumed mandate nonces, DPoP jti replay entries,
// and the authority's issued-mandate log. Booking is one immediate
// transaction: the window-overlap check, the nonce consumption and the
// booking insert commit together or not at all (master plan 8.5 steps 11-12,
// 8.8).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"

	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/schema"
)

// Store wraps the database.
type Store struct {
	db     *sql.DB
	now    func() time.Time
	writes atomic.Int64 // every pruneEvery-th write prunes expired rows
}

// pruneEvery bounds dpop_jti and quotes: expired rows go every N writes.
const pruneEvery = 256

// dbErr gives a raw database failure a named code (rule 4); an *errs.Error
// passes through.
func dbErr(err error) error {
	if err == nil {
		return nil
	}
	var e *errs.Error
	if errors.As(err, &e) {
		return e
	}
	return errs.New(errs.Unavailable, "store: "+err.Error())
}

func (s *Store) maybePrune() {
	if s.writes.Add(1)%pruneEvery != 0 {
		return
	}
	now := s.now().Unix()
	_, _ = s.db.Exec(`DELETE FROM dpop_jti WHERE exp <= ?`, now)
	_, _ = s.db.Exec(`DELETE FROM quotes WHERE exp <= ? AND quote_id NOT IN (SELECT quote_id FROM bookings)`, now)
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS quotes (
  quote_id TEXT PRIMARY KEY,
  body     TEXT NOT NULL,      -- JCS of schema.Quote
  exp      INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS bookings (
  booking_id TEXT PRIMARY KEY,
  quote_id   TEXT NOT NULL,
  mandate_id TEXT NOT NULL,
  iss        TEXT NOT NULL,     -- the mandate's issuer: nonces are scoped to it
  nonce      TEXT NOT NULL,
  norad_id   INTEGER NOT NULL,
  nbf        INTEGER NOT NULL,
  exp        INTEGER NOT NULL,
  receipt    TEXT NOT NULL,
  mandate    TEXT NOT NULL      -- the booked mandate JWS, for the pass session
);
CREATE TABLE IF NOT EXISTS counters (
  name  TEXT PRIMARY KEY,
  value INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS bookings_window ON bookings (nbf, exp);
CREATE TABLE IF NOT EXISTS consumed_nonces (
  iss        TEXT NOT NULL,
  nonce      TEXT NOT NULL,
  mandate_id TEXT NOT NULL,
  at         INTEGER NOT NULL,
  PRIMARY KEY (iss, nonce)      -- one issuer cannot burn another's nonce
);
CREATE TABLE IF NOT EXISTS dpop_jti (
  key TEXT PRIMARY KEY,
  exp INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS mandates_issued (
  mandate_id TEXT PRIMARY KEY,
  norad_id   INTEGER NOT NULL,
  day        TEXT NOT NULL,     -- UTC date of nbf
  station    TEXT NOT NULL
);
`

// Open opens (creating if needed) the database at path. ":memory:" is not
// supported (each pooled connection would see its own database); tests use
// a temp file.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if err := migrate(db, path); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, now: time.Now}, nil
}

// schemaVersion is bumped whenever a table changes shape.
const schemaVersion = 3

// migrate creates a fresh database at schemaVersion, and refuses one written
// by an older build rather than failing later mid-query: this is demo state,
// so the fix is to delete the file.
func migrate(db *sql.DB, path string) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'`).Scan(&tables); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if tables > 0 && v != schemaVersion {
		return errs.New(errs.Unavailable, fmt.Sprintf("store %s has schema v%d, this build needs v%d: delete the file", path, v, schemaVersion))
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("store: schema: %w", err)
	}
	_, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion))
	return err
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// PutQuote stores a quote until its expiry.
func (s *Store) PutQuote(ctx context.Context, q schema.Quote, body []byte) error {
	s.maybePrune()
	_, err := s.db.ExecContext(ctx, `INSERT INTO quotes (quote_id, body, exp) VALUES (?, ?, ?)`, q.QuoteID, string(body), q.Exp)
	return dbErr(err)
}

// GetQuote returns a stored, unexpired quote.
func (s *Store) GetQuote(ctx context.Context, id string) (schema.Quote, bool, error) {
	var body string
	var exp int64
	err := s.db.QueryRowContext(ctx, `SELECT body, exp FROM quotes WHERE quote_id = ?`, id).Scan(&body, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return schema.Quote{}, false, nil
	}
	if err != nil {
		return schema.Quote{}, false, dbErr(err)
	}
	if exp <= s.now().Unix() {
		return schema.Quote{}, false, nil
	}
	q, err := schema.DecodeQuote([]byte(body))
	return q, err == nil, err
}

// Booking is what Book records.
type Booking struct {
	BookingID string
	QuoteID   string
	MandateID string
	Iss       string // the mandate's issuer
	Nonce     string
	NoradID   int64
	Nbf, Exp  int64
	Receipt   string // station-signed BookingReceipt JWS
	Mandate   string // the booked mandate JWS
}

// Book runs book_pass steps 11 and 12 and the insert in one immediate
// transaction: no booking may overlap [Nbf, Exp) (one antenna), and the
// mandate nonce must be unused; it is consumed only if the booking commits.
// The one booking that does not count as an overlap is the one made with
// this very mandate (same issuer and nonce), so a replayed mandate reaches
// step 12 and is MANDATE_REJECTED:consumed. Nonces are scoped to the issuer.
// The booked quote is kept until the pass ends, so a replay long after
// booking still reaches step 12 while the window is open.
func (s *Store) Book(ctx context.Context, b Booking) error {
	return dbErr(s.book(ctx, b))
}

func (s *Store) book(ctx context.Context, b Booking) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var clash string
	err = tx.QueryRowContext(ctx, `SELECT booking_id FROM bookings WHERE nbf < ? AND ? < exp AND NOT (iss = ? AND nonce = ?) LIMIT 1`,
		b.Exp, b.Nbf, b.Iss, b.Nonce).Scan(&clash)
	switch {
	case err == nil:
		return errs.New(errs.BookingRejectedOverlap, "window overlaps booking "+clash)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO consumed_nonces (iss, nonce, mandate_id, at) VALUES (?, ?, ?, ?)`,
		b.Iss, b.Nonce, b.MandateID, s.now().Unix())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errs.New(errs.MandateRejectedConsumed, "mandate nonce already used")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO bookings (booking_id, quote_id, mandate_id, iss, nonce, norad_id, nbf, exp, receipt, mandate) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.BookingID, b.QuoteID, b.MandateID, b.Iss, b.Nonce, b.NoradID, b.Nbf, b.Exp, b.Receipt, b.Mandate); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE quotes SET exp = MAX(exp, ?) WHERE quote_id = ?`, b.Exp, b.QuoteID); err != nil {
		return err
	}
	return tx.Commit()
}

// GetBooking returns one booking, or found=false.
func (s *Store) GetBooking(ctx context.Context, id string) (b Booking, found bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT booking_id, quote_id, mandate_id, iss, nonce, norad_id, nbf, exp, receipt, mandate
		FROM bookings WHERE booking_id = ?`, id).Scan(&b.BookingID, &b.QuoteID, &b.MandateID, &b.Iss, &b.Nonce,
		&b.NoradID, &b.Nbf, &b.Exp, &b.Receipt, &b.Mandate)
	if errors.Is(err, sql.ErrNoRows) {
		return Booking{}, false, nil
	}
	return b, err == nil, dbErr(err)
}

// NextCounter atomically increments and returns the named counter (Ops'
// per-satellite command counter: it survives restarts and never repeats).
func (s *Store) NextCounter(ctx context.Context, name string) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO counters (name, value) VALUES (?, 1)
		ON CONFLICT(name) DO UPDATE SET value = value + 1 RETURNING value`, name).Scan(&v)
	return v, dbErr(err)
}

// Advance sets the named counter to v only if v is greater than its current
// value (the spacecraft's last accepted counter), atomically. It reports
// whether it advanced.
func (s *Store) Advance(ctx context.Context, name string, v int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO counters (name, value) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET value = excluded.value WHERE counters.value < excluded.value`, name, v)
	if err != nil {
		return false, dbErr(err)
	}
	n, err := res.RowsAffected()
	return n == 1, dbErr(err)
}

// Counter returns the named counter's value (0 if unset).
func (s *Store) Counter(ctx context.Context, name string) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `SELECT value FROM counters WHERE name = ?`, name).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return v, dbErr(err)
}

// Bookings returns the number of bookings (tests, dashboard).
func (s *Store) Bookings(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bookings`).Scan(&n)
	return n, dbErr(err)
}

// RecordMandate logs an issued mandate and returns how many the authority
// has issued for noradID on that UTC day, including this one. The flight
// rule it enforces is mandates per day: an unused mandate still counts.
func (s *Store) RecordMandate(ctx context.Context, mandateID string, noradID, nbf int64, station string, limit int) (int, error) {
	n, err := s.recordMandate(ctx, mandateID, noradID, nbf, station, limit)
	return n, dbErr(err)
}

func (s *Store) recordMandate(ctx context.Context, mandateID string, noradID, nbf int64, station string, limit int) (int, error) {
	day := time.Unix(nbf, 0).UTC().Format("2006-01-02")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mandates_issued WHERE norad_id = ? AND day = ?`, noradID, day).Scan(&n); err != nil {
		return 0, err
	}
	if n >= limit {
		return n, errs.New(errs.PolicyRefusedDailyLimit, fmt.Sprintf("%d passes already authorized for %d on %s", n, noradID, day))
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO mandates_issued (mandate_id, norad_id, day, station) VALUES (?, ?, ?, ?)`,
		mandateID, noradID, day, station); err != nil {
		return 0, err
	}
	return n + 1, tx.Commit()
}

// ReplayCache is the DPoP jti cache for pop.Middleware (check 10), backed by
// dpop_jti so it survives restarts. CheckAndStore is one atomic statement:
// insert, or replace an expired entry; a live entry means a replay.
func (s *Store) ReplayCache() *Replay { return &Replay{s: s} }

// Replay implements pop.ReplayCache.
type Replay struct{ s *Store }

// CheckAndStore reports seen=true for a live duplicate key.
func (r *Replay) CheckAndStore(key string, exp time.Time) (bool, error) {
	r.s.maybePrune()
	res, err := r.s.db.Exec(`INSERT INTO dpop_jti (key, exp) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET exp = excluded.exp WHERE dpop_jti.exp <= ?`,
		key, exp.Unix(), r.s.now().Unix())
	if err != nil {
		return false, err // pop fails closed on an error
	}
	n, err := res.RowsAffected()
	return n == 0, err
}
