# third_party patches

## agent-trust-discovery

- **Upstream:** https://github.com/agentnameservice/agent-trust-discovery
- **Pinned commit:** `fde0b448d2dc4d2e25f6ef139c24a88734fcf027`
- **License:** MIT (see `agent-trust-discovery/LICENSE`), retained unchanged.

Vendored (not Go-imported from a proxy) because the plug-in model is "compile
your own binary with the extra signal registered" (docs/extending-signals.md);
signals live under `internal/`, so a fork is the only way to add one. Overpass
imports the fork via a `replace` directive in the root `go.mod`.

### Patch (kept small, master plan §11)

1. `internal/scoring/signals/passdelivery.go` — new `PassDelivery` signal
   (`pass_delivery`, dimension `behavior`, not derived). Value
   `{booked, delivered, auditFailures}` (non-negative ints, `delivered <= booked`).
   Score: no observation → 0 "no audited passes yet"; else `round(100*delivered/booked)`,
   capped at 40 with risk `BEHAVIOR_AUDIT_FAILURE` when `auditFailures > 0`.
   Absence scores 0 and counts (no `AbsenceAware`): cold start is a feature.
2. `internal/scoring/signals/signals.go` — append `PassDelivery{}` to `BuiltIns`,
   after the eight upstream built-ins (their order unchanged).
3. `internal/scoring/signals/passdelivery_test.go` — unit tests: no data, perfect,
   partial, audit-failure cap, invalid payloads.
4. `config/default-profile.yaml` — add `pass_delivery` signal weight `1` so the
   behavior DIMENSION score reflects real audited passes and is displayed.
   `dimensionWeights.behavior` stays `0`: behavior is deliberately kept OUT of the
   upstream `recommendedProfile` cascade, so the index's own verdict is unchanged.

Deliberately NOT changed: `config/runtime.yaml` thresholds, the classifier, and
the config loader are all upstream-identical. Overpass does not gate on the
index's `recommendedProfile`. It defines its own access tiers (availability /
downlink / uplink) in the authority's flight-rules YAML, computed from the
truthful five-dimension trust vector. Identity is displayed as measured (a DV cert
scores 40) and is not part of the gate; production would require OV/EV via the
flight rules' `min_certtype`. See docs/status/phase-8.md.

Nothing else in the upstream tree is modified.
