# Phase 5 status: Quote, mandate, booking

## What works
- **Station `get_pass_quote`**
  - Price = `per_minute_cents` × whole pass-minutes, in integer cents. Bounds prevent overflow.
  - It carries an x402-shaped `accepts` block. Its `payTo` equals the signed card's new
    `x-payment.payTo`, and its amount is in asset units (`asset_decimals`).
  - Quotes are valid for 10 minutes. The NORAD ID must be in the station's signed satellite
    registry. Refusals are `QUOTE_REJECTED:{window,norad_id}`.
- **Station `book_pass`:** master plan 8.5's checks in their exact order and codes, in one
  function that recovers panics:
  1. parse, typ → `MANDATE_PARSE_ERROR` / `TYP_REJECTED`
  2. signature under the issuer's pinned key
  3. `not_owner` via the registry
  4. audience
  5. quote
  6. scope
  7. amount
  8. window
  9. DPoP key = `jkt`
  10. — (DPoP jti unseen; see Deviations)
  11. overlap
  12. `consumed`

  Checks 11–12 and the booking insert run in one SQLite `BEGIN IMMEDIATE` transaction. Nonces and
  the overlap exception are scoped to `(iss, nonce)`. The response is a station-signed
  `overpass-receipt+jws`.
- **Authority `issue_mandate`**
  - The caller must be in `ops_agents`, and VerifyPeer on the station must pass with its ANS name
    equal to `quote.station`.
  - The tier comes from `TrustSource` (a static config stub until Phase 8). Uplink needs
    FIDUCIARY.
  - Flight rules cover the station list or `min_tier`, command classes per mode, max cents per
    pass, and max mandates per day.
  - The mandate is signed with `jkt` = the caller's DPoP key. Refusals are
    `POLICY_REFUSED:<rule>`.
- **Satellite registry:** `bin/satreg sign|pubkey`. Stations load it at start and pin the signer.
- **SQLite store** (modernc.org/sqlite, WAL): quotes, bookings, `consumed_nonces`, `dpop_jti` and
  `mandates_issued`. Expired jti and quote rows are pruned. Database failures surface as the
  named `unavailable`.
- **Rogue mode:** `rogue: true` needs `role: station` plus `rogue_ack: i-am-the-rogue-station`,
  and logs a loud banner. It skips check 2, check 9, and the jti replay check (check 10).

## How it was tested
- `gofmt` is clean, `go vet` passes, and `go test -race ./...` passes.
- `scripts/accept/phase-5.sh` passes:
  1. **Happy path:** quote → mandate → book succeeds once, then `MANDATE_REJECTED:consumed`.
     Tested three ways: at the station, across authority + station (a real `issue_mandate`), and
     over HTTPS with real DPoP proofs (SDK demokit).
  2. **Every check with its exact code:**
     - superseded format; typ confusion; flipped, tampered-underpay and unknown-key signatures
     - `not_owner` with a validly signed mandate from a second registered authority, and with an
       unregistered `sub`
     - audience, quote swap and unknown quote, scope, amount, widened and expired window
     - wrong DPoP key and missing caller, overlap, and a replayed spent mandate
     - review regressions: `mandate_id` reuse across issuers, nonce squatting, and replay after
       the quote TTL
  3. **Concurrent overlap:** two overlapping bookings, 50 iterations under `-race`. Exactly one
     wins each time.
  4. **Hostile inputs:** garbage, truncated, oversized, extra-field and float inputs all get named
     codes, plus a 20 s fuzz of `book_pass`.
  5. **Authority:** uplink on a READ_ONLY station (and a TRANSACTIONAL one) gets
     `POLICY_REFUSED:tier`. Every other rule has its own refusal test.
  6. **payTo:** equals the served card's `x-payment.payTo` (over HTTP) and the pricing config.
- **Over HTTPS:** a replayed DPoP proof gets `DPOP_REJECTED:replay`, and no proof gets
  `CALLER_REJECTED`.
- The Phase 0–4 acceptance scripts still pass.
- A separate review subagent reviewed the diff. Its 10 findings are listed in
  `docs/plans/phase-5.md`, and all are fixed. The two high-severity ones were cross-issuer
  `mandate_id` and nonce scoping, which enabled an overlap bypass and nonce squatting.

## Deviations
- **Check 10 (DPoP jti unseen)** runs in the transport, in ans-sdk-go `pop.Middleware` backed by
  our SQLite `dpop_jti` table, so it happens before the mandate is read. The SDK does not give
  handlers the jti. The code is still `DPOP_REJECTED:replay`.
- **Overlap exception:** a booking made with the same mandate (same issuer and nonce) doesn't
  count as an overlap, so attack 12 (`replay_settled`) gets `consumed`, as the battery table
  expects.
- **Quote `max_elevation_deg`** is supplied by Ops, from the pass table. The station bounds it to
  0–90 but doesn't recompute it.

## Stubbed or deferred
- **Trust tiers** come from config (`TrustSource` stub) until Phase 8.
- **Authority keys on stations** are pinned PEM files. A KeySource backed by the verified trust
  card is not wired yet.
- **For the Phase 6 battery:** build tampers as the tests do, with a re-canonicalized payload and
  decoded-byte flips, so attacks 2, 3 and 9 hit `signature` rather than parse.

## Needs a human
See `docs/human-actions.md`, updated this phase:
- pricing and `payTo`
- satellite registry signing
- flight rules
- approval to add the new codes to the frozen `docs/schemas.md`
- the check 10 deviation
