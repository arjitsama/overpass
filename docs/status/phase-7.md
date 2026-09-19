# Phase 7 status: adversaries, battery, auditor

## What works
- **Attack battery** (`internal/battery`, `cmd/battery`): 23 attacks from master-plan
  section 10 run against a target station over real A2A. Each returns a verdict —
  BLOCKED (refused with the expected named code), VULNERABLE (accepted, or refused
  with the wrong code), or INCONCLUSIVE (could not run). Booking attacks
  (replay, underpay, tamper, quote-swap, wrong-audience/scope, wrong-DPoP-key,
  corrupt-JWS, superseded-format, unknown-key, replay-settled, canonicalization,
  payto-binding, card-drift, not-owner, overlap, typ-confusion, jku-injection) run
  always; command/relay attacks (forged/replayed/late command, class escalation)
  run only when a spacecraft URL is configured.
- **Deploy gate**: `battery run` exits 0 only if every check is BLOCKED; `-expect-vulnerable`
  inverts it for the rogue demo. `AllBlocked` treats an empty result set as *not* passing.
- **Auditor** (`internal/auditor`, `cmd/agent --role auditor`): verifies a completed pass —
  identity, SCITT receipt, mandate, DPoP binding, chain-head match, and acks — and signs an
  `overpass-audit` JWS verdict. Two active canary probes (corrupt-JWS mandate, wrong-DPoP-key
  mandate) run against a live station; a station that accepts either is reported CANARY_ACCEPTED.
- **Deploy configs** (`deploy/local/`): honest, rogue, impostor (unregistered, self-signed),
  and registered-lookalike station configs, plus honest/rogue battery configs. Placeholders
  only — no keys or secrets committed.

## How it was tested
- `go test -race ./...` — all packages pass.
- `gofmt -l` clean; `go vet ./...` clean.
- `scripts/accept/phase-7.sh` — all 7 acceptance criteria pass:
  1. honest station BLOCKs every attack with the master-plan code;
  2. rogue station is VULNERABLE to tamper/wrong-DPoP;
  3. impostor fails VerifyPeer and gets no quote request;
  4. registered lookalike verifies but the authority refuses uplink (POLICY_REFUSED:tier);
  5. canary — honest rejects both probes, rogue accepts and the report says CANARY_ACCEPTED;
  6. clean pass -> verdict pass with equal chain heads on both sides;
  7. battery exit codes gate honest (0) vs rogue (non-zero).
- Regression: phases 0–6 acceptance scripts all still pass.
- `TestBatteryHonest`, `TestCanaryLive` run `-count=5` with no flakes (finding 1 fix).

## What is stubbed
- `auditor.LogSink` is a stub `TrustSink` (real trust-card publishing lands in Phase 8).

## What needs a human
See `docs/human-actions.md` (Phase 7 section): register the honest/rogue/lookalike
stations on the reference stack (impostor stays unregistered), fill the battery config
placeholders (Ops identities + authority signing keys the target trusts — deliberate for a
red-team tool), configure the auditor's station/authority keys and add its ANS name to each
station's `session.auditors`, and wire `battery run` into the deploy as a gate.
