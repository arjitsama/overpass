# Phase 8 status: trust index and tiers

## What works
- **Forked trust index** (`third_party/agent-trust-discovery`, upstream fde0b448,
  MIT): one added behavior signal, `PassDelivery` (`pass_delivery`), scoring
  `round(100*delivered/booked)`, capped at 40 with `BEHAVIOR_AUDIT_FAILURE` on any
  audit failure, and 0 ("no audited passes yet") with no observation. Registered
  beside the eight built-ins; weighted so the behavior dimension displays. Patch
  listed in `third_party/PATCHES.md`. `make trust-up` runs it on :8080.
- **`internal/trust.Client`**: implements `authority.TrustSource` (reads a
  station's truthful five-dimension vector, its audited-pass count, and its
  audit-failure flag) and `auditor.TrustSink` (records each audited pass as a
  cumulative `pass_delivery` observation via read-modify-write). Imports agents;
  surfaces any non-200 body (e.g. the 422 `AGENT_NOT_FOUND`) instead of swallowing
  it. Also carries the index's own `recommendedProfile` for display.
- **Overpass access tiers in the authority's flight rules** (not the index's
  `recommendedProfile`), computed from the truthful vector: `availability` = ANS
  verified; `downlink` (TRANSACTIONAL) = integrity ≥ 50 and no audit failures
  (behavior 0 / no history is probation, allowed so a new station can earn
  history); `uplink` (FIDUCIARY) = integrity ≥ 80, behavior ≥ 80, ≥ 3 audited
  passes, no audit failures, and cert tier ≥ `min_cert_type` (default DV).
  Thresholds live in `flight_rules` (config), each with a documented default.
  The authority **fails closed**: an unreachable index refuses uplink with
  `POLICY_REFUSED:tier` naming the index.
- **Auditor → index**: the auditor role posts `pass_delivery` to the index when
  one is configured (else a log sink); a failed post surfaces a warning event
  rather than being dropped.
- **Seed tool** (`cmd/trustseed`): imports the seven agents and gives the honest
  stations a baseline `pass_delivery`, marked as seed data (`provenance.aimId =
  overpass-seed`). The lookalike is imported but never seeded. It never writes
  identity or integrity.

## How it was tested
- `gofmt -l`, `go vet ./...`, `go test -race ./...` — all clean/green.
- Fork suite green; `scripts/accept/phase-8.sh` passes all five criteria:
  1. `PassDelivery` unit tests (no data / perfect / partial / audit cap / invalid).
  2. A station with earned history is uplink-eligible; the lookalike (same
     identity/integrity, no audited passes) is downlink-probation and uplink is
     refused; the evaluation shows five dimensions with solvency+safety 0 and a
     real DV identity of 40.
  3. After a `CANARY_ACCEPTED`, the rogue's behavior is capped at 40 and it loses
     uplink eligibility.
  4. An observation for an unknown agent surfaces the 422 body.
  5. Index down → the authority fails closed on uplink.
- Regression: phases 0–7 acceptance all still pass.

## What is stubbed / a truthful limitation of the LOCAL stack
The upstream engine computes the **identity** dimension from the `certtype`
signal alone — DV=40 / OV=70 / EV=100
(`third_party/agent-trust-discovery/internal/scoring/signals/certtype.go:17`
maps it to `DimensionIdentity`; DV→40 is asserted at
`.../internal/server/build_test.go` "identity 40 for the DV cert"). The
`recommendedProfile` FIDUCIARY rule requires **every active dimension ≥ 80** and
identity ≥ `identityFiduciaryThreshold`
(`.../internal/scoring/engine/classify.go:40-41`, defaults 80/90 at
`classify.go:16`). So a DV-certified agent — which is all our demo-CA stations —
**cannot reach the index's FIDUCIARY**, and lowering only `identityFiduciaryThreshold`
(as master-plan rev-3 suggested) is insufficient because the 80 floor also gates
identity.

Per the project decision (never fabricate identity or integrity; do not patch the
classifier or loader), Overpass does **not** gate on `recommendedProfile`. It
gates on the truthful vector in its own flight rules, and identity is displayed as
measured, not gated (production would raise `min_cert_type` to OV/EV). Integrity
and identity have **no local source** (no prober against real DNS/certs, and we
never seed them), so on the local stack a behavior-only seed leaves an honest
station at integrity ≈ 14, identity 0 → Overpass READ_ONLY. The tier **logic** is
verified in `internal/trust` and `internal/authority` tests with representative
vectors (legitimate test fixtures standing in for the production prober); reaching
downlink/uplink on the local stack requires real integrity evidence from the
prober/hydrator, which a human runs.

## What needs a human
See `docs/human-actions.md` (Phase 8): run the trust index (`make trust-up` or a
deploy with a real admin bearer), register the agents and seed via `cmd/trustseed`,
supply the admin key through the `admin_key_env` environment variable, run the
prober/hydrator (or `make demo-live`) to populate real integrity/identity, and set
`flight_rules.min_cert_type` to OV/EV for production uplink. `make demo-live`
against production is out of scope for this phase.
