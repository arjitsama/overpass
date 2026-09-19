# Phase 6 plan: Pass session

Goal: commands flow only inside a booked window; the station can relay but not forge; revocation
cuts the session while an outage does not.

## Files
- internal/store: `counters` table: `NextCounter(name)` (Ops' per-satellite counter, survives
  restarts) and `Advance(name, v)` (the spacecraft's last accepted counter: only if v is greater,
  atomically). `bookings.mandate` keeps the booked mandate; `GetBooking(id)`.
- internal/spacecraft: `Spacecraft{NoradID, OpsKeys, Store}`; `Uplink(token)` verifies typ and Ops
  signature (schema.VerifyCommand), norad_id, counter > last (persisted) and applies a tiny state
  machine (mode, battery_pct, beacon_s). Ack{counter, result, telemetry_sha256 = SHA-256(JCS(state))}
  or a rejected Ack with reason COMMAND_REJECTED:{signature,norad_id,counter}.
- internal/session: `Monitor` (shared token loop): `Tick(ctx)` uses verify.Keeper; non-ACTIVE ->
  cut "revoked", newest token older than 10 min -> cut "token_stale", fetch error alone -> warning
  event. `Run(ctx, every)` ticks every 30 s. Station `Manager`: `Relay(ctx, bookingID, token)`:
  booking exists; mandate clock window nbf-30..exp+30 (else WINDOW_CLOSED); not cut; DPoP key =
  mandate jkt; command typ (PeekCommand); class in command_classes (else CLASS_REJECTED);
  mandate_id matches; relay to the spacecraft (Uplink interface) without touching body; append the
  CommandRecord to the station chain; missing Ack -> suspected_drop. `Evidence(bookingID)` returns
  head, records, missing acks. Station skill `relay_command`.
- internal/ops: `Commander` (counter from store, signs overpass-cmd+jws); `Pass` driver: own chain,
  queue, Monitor on the station's token; on cut: discard queue, emit session_cut, call Replan hook.
- verify.Decision gains `Kind` (revoked/stale/unavailable) so cuts carry a named reason.
- cmd/agent: role spacecraft (skill `uplink`, config spacecraft{norad_id, ops_key_file}); station
  relays to `spacecraft_url` over A2A.
- Test hooks: fake token sources in _test.go files only.

## Codes
SESSION_CUT:revoked, SESSION_CUT:token_stale, SESSION_REJECTED:booking, COMMAND_REJECTED:mandate_id,
WINDOW_CLOSED, CLASS_REJECTED, ACK_MISSING (suspected drop), COMMAND_REJECTED:{signature,norad_id,counter}.

## Tests, one per acceptance criterion
1. TestWindow: before nbf-30 and after exp+30 -> WINDOW_CLOSED; inside -> acked.
2. TestStationCannotForgeOrReplay: station-signed command -> COMMAND_REJECTED:signature; replay -> counter.
3. TestChainsMatchAndDrop: clean pass heads byte-identical; dropped relay -> suspected_drop, heads differ.
4. TestRevokedCuts: flip to revoked, next Tick cuts, replan event follows.
5. TestOutageWarns: 20 s of fetch errors (ticks at 30 s cadence on a fake clock) -> warnings, still open, commands flow.
6. TestOutagePastMaxAge: -> cut token_stale.
7. TestClassRejected.
## Dependencies: none new.

## Review
Separate review subagent. (H) Ops chained station-refused commands: now only relayed ones.
(H) a failed first token fetch cut the pass for good: now SESSION_REJECTED:token_unavailable, no cut.
(M) window end: timer at exp+30 closes the session, stops the loop; uplink deadline = window end.
(M) per-command token check at most once per 30 s (TickIfDue). (M) skills disabled for want of
config are pruned from the card; new config validated. (M) store schema version (refuses old DBs).
(M) Acks validated (ACK_PARSE_ERROR). (L) A2A client passes on only known errs codes; no DB lookup
under the manager lock; session only after a proven caller; evidence only for the booking's Ops
agent or configured auditors. (L) tests: station replay, station stale, real loop, spacecraft
restart and concurrency; cmd/agent tests use temp DBs.
