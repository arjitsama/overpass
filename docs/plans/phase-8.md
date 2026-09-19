# Phase 8 plan: Trust index and tiers

Prompt: master plan §11 (+ revision 3). Upstream read at pinned commit
fde0b448 (MIT). Ship as "phase 8: trust index and tiers".

## Files to touch
- third_party/agent-trust-discovery/: upstream fork at fde0b448 + LICENSE.
  Patch: internal/scoring/signals/passdelivery.go (+ _test), add to BuiltIns,
  config/default-profile.yaml (behavior active, pass_delivery weight 1),
  config/runtime.yaml (our thresholds). third_party/PATCHES.md lists it.
- go.mod: require + replace => ./third_party/agent-trust-discovery.
- internal/trust/client.go (+ _test): HTTP client. TrustSource (authority) via
  GET /v1/ans/registered-agents/{id} -> recommendedProfile; TrustSink (auditor)
  via POST /v1/internal/observations/import; ImportAgents seed helper. Non-200
  surfaces the body.
- internal/config: trust index url, admin key (env), agent-id map.
- cmd/agent: authority role uses trust.Client not StaticTrust; auditor role
  uses trust.Client not LogSink. Fail closed when the index is unreachable.
- scripts/seed-trust.sh + cmd/trustseed: import 7 agents, seed honest stations
  (provenance.source=seeded), none for the lookalike.
- Makefile: trust-up (build+run fork on :8080), trust-down.
- scripts/accept/phase-8.sh.

## Signal (master plan §11.4)
PassDelivery: ID pass_delivery, DimensionBehavior, Derived false. Validate
{booked,delivered,auditFailures} non-negative ints, delivered<=booked. Evaluate:
nil obs -> 0 "no audited passes yet"; else round(100*delivered/booked), capped 40
with risk BEHAVIOR_AUDIT_FAILURE when auditFailures>0. Weight 1.

## Scoring reality (read from source, hard rule 9)
identity dimension = certtype ALONE (DV 40 / OV 70 / EV 100). FIDUCIARY needs
every active dim >=80 AND identity>=identityFiduciary. Unseeded integrity=0 ->
UNTRUSTED for all. So, for the LOCAL stack: seed integrity+identity+behavior for
honest stations (all provenance.source=seeded, transparent baseline, exactly the
cold-start mechanism rev-3 describes); lookalike seeded with NOTHING.
Thresholds (our fork config, documented): untrusted 0 (so behavior=0 lands the
lookalike at READ_ONLY not UNTRUSTED), transactional 50, fiduciary 80,
identityFiduciary 80 (lowered from 90: our demo CA cannot publish the DNSSEC/DANE
records that lift a live identity score past the fiduciary floor; production
returns it to 90). This is the one human-review decision — recorded in status.

## Tests (one per acceptance criterion)
1. PassDelivery unit: no data, perfect, partial, audit-failure cap, invalid payloads.
2. Index in-process (fork server.Build over httptest): seeded honest -> FIDUCIARY,
   lookalike -> READ_ONLY, all 5 dims present, solvency+safety 0.
3. auditor posts CANARY_ACCEPTED (auditFailures>0) for gs-rogue -> tier < FIDUCIARY,
   authority refuses its next uplink mandate POLICY_REFUSED:tier.
4. observation for an unknown agent -> 422 body surfaced (not swallowed).
5. index down -> authority refuses uplink (fail closed), reason names the index.

## Risks / assumptions
- Fork go directive 1.26.5 > ours 1.25; bump our go.mod if the toolchain requires.
- auditor.Observation has Verdict+AuditFailures, no counts: map one pass to
  {booked:1, delivered: pass?1:0, auditFailures}. Index keeps latest, so a bad
  audit overwrites the seed low. Sufficient for "drops a tier after one pass".
- Do NOT run make demo-live (production); a human runs it (out of scope).

## Decision (from the human, mid-phase)
Do NOT fabricate certtype/identity; do NOT patch the classifier or loader. The
fork adds only PassDelivery (+ its weight) and keeps upstream thresholds and
recommendedProfile, which we display, not gate on. Overpass access tiers live in
the authority flight rules, computed from the truthful vector: availability = ANS
verified; downlink = integrity>=50 && !auditFailures (behavior 0 = probation,
allowed); uplink = integrity>=80 && behavior>=80 && >=3 audited passes &&
!auditFailures && cert>=min_cert_type (default DV, production OV/EV). Prefer
earned history (warm-up passes) over seeding; if infeasible, seed pass_delivery
only, marked seed. Acceptance 2/3 rewritten accordingly.

## Review (hostile pass, findings fixed)
1. Cross-module internal import: tests could not call the fork's internal/server.
   Added a tiny exported shim `overpassapi.Start` inside the fork (PATCHES.md).
2. Fork config edits broke upstream tests (behavior-active default profile,
   untrusted 0 rejected by the loader). Reverted the shipped configs; the fork
   patch is now only the signal + its weight + a local demo runtime file.
3. Fixed-clock observations tied on observedAt, so the index kept the first
   pass_delivery and Post never accumulated. Tests use an increasing clock.
4. currentTally recovered `delivered` lossily from a (possibly capped) rawScore;
   now parses it exactly from the explanation (deliveredRE).
5. The auditor swallowed Sink.Post errors; added an optional Emit so a down index
   surfaces a `trust_post_failed` warning instead of silence.
6. Admin key read from env only (hard rule 2); non-200 bodies surfaced, not
   swallowed; GET/POST bodies size-limited (1 MiB).

Known limitation (documented in status): the per-agent Post tally has a benign
read-modify-write race under concurrent posts for one agent; the auditor posts
sequentially per pass, so it does not arise in practice.
