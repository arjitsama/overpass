# Phase 6 status: Pass session

## What works
- **Spacecraft role** (`internal/spacecraft`, A2A skill `uplink`, noAuth: its security is the
  Ops signature on each command).
  - It accepts a command only if typ and the Ops signature verify, `norad_id` is its own, and the
    counter is strictly greater than the last accepted one.
  - The last counter is persisted atomically in SQLite and survives restarts. Of 20 concurrent
    uplinks with the same counter, exactly one is accepted.
  - State is simple: mode, battery, beacon interval.
  - It returns an unsigned Ack: `{counter, result, telemetry_sha256}` or a rejection with a
    `COMMAND_REJECTED:*` reason.
- **Ops `Commander`:** signs `overpass-cmd+jws` with a per-satellite counter that survives
  restarts (`store.NextCounter`).
- **Station `relay_command`** (`session.Manager`) runs these checks in order:
  1. proven caller
  2. booking
  3. window by the mandate's clock (`nbf − 30 s` to `exp + 30 s`, else `WINDOW_CLOSED`)
  4. DPoP key = `jkt`
  5. Ops token
  6. typ (`PeekCommand`)
  7. class in `command_classes`, else `CLASS_REJECTED`
  8. `mandate_id`
  9. counter

  It relays to the spacecraft agent without reading or changing the body, bounded by the window's
  end. Then it appends the CommandRecord and validates the Ack. A missing or invalid Ack is
  `ACK_MISSING` and a suspected drop.
- **Chains:** Ops and the station each keep a CommandRecord chain. Commands the station refuses
  are chained by neither side. Station `session_evidence` returns the head, records, Acks and
  suspected drops, to the booking's Ops agent or a configured auditor.
- **Token loop, both sides** (`session.Monitor`, Phase 3 policy):
  - fetch every 30 s, and on a command at most once per 30 s
  - a token that isn't ACTIVE → `SESSION_CUT:revoked`
  - a newest token older than 10 min → `SESSION_CUT:token_stale`
  - a fetch error alone → a `token_warning` event
  - a failed fetch at open refuses the command (`SESSION_REJECTED:token_unavailable`) without
    cutting
- **On a cut:** the session closes, Ops discards queued commands, `session_cut` is emitted, and
  the planner's Replan hook runs. At `exp + 30 s` a timer ends the session and emits
  `window_closed`.
- **Card honesty:** skills a role cannot serve for lack of config (`relay_command` without
  `session.spacecraft_url`, `uplink` without spacecraft config) are pruned from the card (rule 6).
- **Store:** a schema version (`PRAGMA user_version`). An older database is refused with a named
  error rather than failing mid-query.

## How it was tested
- `gofmt` is clean, `go vet` passes, and `go test -race ./...` passes.
- `scripts/accept/phase-6.sh` passes. The pass tests run real Ops, station and spacecraft code
  with three separate stores and a fake clock, and fake token sources that live only in
  `_test.go`.
  1. Commands before `nbf − 30 s` and after `exp + 30 s` get `WINDOW_CLOSED`. Commands inside are
     acked and applied.
  2. A station-invented command is rejected by the spacecraft (`COMMAND_REJECTED:signature`). A
     replay is rejected on counter, by the spacecraft and by the station.
  3. After a clean pass the chain heads and records are byte-identical. A silently dropped relay
     gives a suspected drop and unequal heads. Commands the station refused keep the heads equal.
  4. A token flipped to revoked is cut at the next fetch. Replan and `session_cut` follow, and
     queued commands are discarded. Tested on both sides, including a real background loop at
     20 ms.
  5. A 20 s outage gives warnings, the session stays up and commands still flow. A failed fetch at
     open is not a cut.
  6. An outage past 10 min cuts with `token_stale`, on both sides.
  7. A class outside the mandate gets `CLASS_REJECTED`.
- **The whole pass over HTTPS:**
  - a station agent and a spacecraft agent, with real DPoP proofs (SDK demokit)
  - the Ops status token served by a stub log and verified with the log's root key
  - quote → book → `relay_command` → spacecraft Ack, then `CLASS_REJECTED`, then
    `session_evidence`
- The Phase 0–5 acceptance scripts still pass.
- A separate review subagent reviewed the diff. Its 10 findings are listed in
  `docs/plans/phase-6.md`, and all are fixed. The two high-severity ones:
  - refused commands were chained on the Ops side, making an honest station look like it dropped them
  - one failed fetch at open cut the pass permanently

## Known limitations
- **Acks are unsigned** (schemas.md). A station could drop a command and forge its Ack; the heads
  and `missing_acks` would not show it. This is listed in `docs/human-actions.md`.
- **No agent drives a pass yet.** The Ops pass driver (`ops.Pass`) is a library; the dashboard
  drives it in Phase 9.
- **Revocation on production:** the revocation beat (master plan 9.12) uses the real `ans-cli
  revoke` (gate H6). It's rehearsed only with fake token sources here.

## Needs a human
See `docs/human-actions.md`:
- spacecraft and session config
- the revocation demo plan
- whether spacecraft-signed Acks are needed
