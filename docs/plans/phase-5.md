# Phase 5 plan: Quote, mandate, booking

Goal: a pass can be quoted, authorized and booked exactly once; every bad request is refused with the right code.

## Files
- internal/store (SQLite, modernc.org/sqlite, WAL, `_txlock=immediate`, busy_timeout): tables
  quotes, bookings, consumed_nonces, dpop_jti, mandates_issued. `Book` runs overlap check,
  nonce consume and booking insert in ONE immediate transaction. `ReplayCache()` implements
  pop.ReplayCache over dpop_jti (atomic upsert-if-expired).
- internal/station: `get_pass_quote` (price = per_minute_cents x whole minutes; x402 accepts with
  the card's payTo; 10 min expiry; NORAD must be in the registry) and `book_pass`: checks 1-9, 11,
  12 in order in one recovering function, then a station-signed BookingReceipt. Rogue mode skips
  check 2 (signature) and check 9 (DPoP key) and uses a replay cache that never says "seen"; it
  logs a loud warning at start and needs `rogue: true` plus `rogue_ack: "i-am-the-rogue-station"`.
- internal/authority: `issue_mandate` (caller in ops_agents, PeerVerifier.VerifyPeer on the quoted
  station, TrustSource.Tier stub from config, flight rules) -> signed mandate. Refusals:
  POLICY_REFUSED:{caller,unverified,station,tier,classes,amount,daily_limit,window}.
- Satellite registry: `cmd/satreg sign` writes the JWS; stations load it at start and pin the
  signer public key from config (schema.VerifySatRegistry).
- Authority keys for check 2: `KeySource` interface (static PEM keys from config; a verifier-backed
  source uses VerifyPeer + the attested trust-card keys, cached).
- a2a: the mandate SkillGuard becomes a declaration (enforced by book_pass itself, so the 12
  checks keep one order); DPoPGuard takes the replay cache and maps a replay to
  DPOP_REJECTED:replay.
- config: db_path, rogue, pricing{per_minute_cents,pay_to,network,asset,asset_decimals},
  satreg{file,signer_key_file}, authority_keys[{ans_name,key_file}], ops_agents, trust_tiers,
  flight_rules{stations,min_tier,command_classes{uplink,downlink},max_cents_per_pass,
  max_passes_per_day}. Card gains `x-payment{payTo,network,asset}` when pricing is set.

## Deviation (documented)
Check 10 (jti unseen) is enforced by ans-sdk-go pop.Middleware at the transport, before the
mandate is read, using our SQLite-backed ReplayCache; the SDK does not expose the jti to handlers
(pop.CallerIdentity has AnsName, AgentID, Fingerprint, JKT). The code is still DPOP_REJECTED:replay.

## Dependencies (rule 7)
- modernc.org/sqlite: named in CLAUDE.md's stack; pure Go, no cgo.

## Tests, one per acceptance criterion
1. TestHappyPathThenConsumed. 2. TestBookPassChecks: table, one row per check, exact code
   (not_owner: validly signed by a second registered authority; overlap). 3. TestConcurrentOverlap
   (-race, 50 iterations, exactly one winner). 4. TestHostileInputs + fuzz book_pass args.
5. TestReadOnlyUplinkRefused. 6. TestPayToMatchesCard.

## Review
Separate review subagent. (H) overlap exclusion keyed on mandate_id let another registered authority
reuse an id; (H) global nonces let one authority burn another's: bookings/nonces now keyed by
(iss, nonce), overlap excludes only this exact mandate. (M) replay after quote TTL gave `quote`:
booked quotes live to LOS. (M) x402 amount overflow: bounded pricing + named refusal. (M) raw DB
errors: named `unavailable`. (L) pruning of dpop_jti/quotes, PeekMandate decodes before high-S,
daily limit documented as mandates/day, satreg uses errs constant. Battery note: build tampers as
the tests do (re-canonicalized payload, decoded-byte flips) so attacks 2/3/9 hit `signature`.
