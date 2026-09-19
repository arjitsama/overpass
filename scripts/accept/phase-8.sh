#!/usr/bin/env bash
# Phase 8 acceptance: trust index and tiers. Exits non-zero on failure.
#   1. PassDelivery unit tests: no data, perfect, partial, audit-failure cap, invalid payloads.
#   2. Seeded honest station is uplink-eligible; the lookalike is downlink-probation only,
#      uplink refused; the evaluation shows five dimensions with solvency+safety 0 and real identity.
#   3. After CANARY_ACCEPTED, the rogue's behavior is capped at 40 and it loses uplink.
#   4. An observation for an unknown agent surfaces the index's 422 body.
#   5. With the index down, the authority fails closed on uplink (POLICY_REFUSED:tier).
set -euo pipefail
cd "$(dirname "$0")/../.."

fail() { echo "FAIL: $*" >&2; exit 1; }
run() {
  local pkg=$1; shift
  local pattern out
  pattern="^($(IFS='|'; echo "$*"))\$"
  out=$(go test -race -count=1 -v "$pkg" -run "$pattern" 2>&1) || { tail -50 <<<"$out" >&2; fail "go test $pkg $*"; }
  for name in "$@"; do
    grep -q -- "--- PASS: $name " <<<"$out" || fail "$name did not run in $pkg"
  done
  echo "ok"
}

echo "== 1: PassDelivery signal (fork unit tests)"
( cd third_party/agent-trust-discovery
  out=$(go test -count=1 -v ./internal/scoring/signals/ -run 'PassDelivery' 2>&1) \
    || { tail -50 <<<"$out" >&2; fail "fork PassDelivery tests"; }
  for name in TestPassDeliveryEvaluate TestPassDeliveryValidate; do
    grep -q -- "--- PASS: $name " <<<"$out" || fail "$name did not run"
  done
  echo "ok" )

echo "== 2: seeded honest uplink-eligible, lookalike downlink-only, 5 dims";  run ./internal/trust TestTierEligibility
echo "== 3: CANARY_ACCEPTED caps behavior and drops uplink";                  run ./internal/trust TestCanaryDropsUplink
echo "== 4: unknown agent surfaces the 422 body";                            run ./internal/trust TestUnknownAgentSurfaces422
echo "== 5: index down -> authority fails closed on uplink";                 run ./internal/authority TestFailsClosedWhenIndexDown TestOverpassTier
echo "phase-8 acceptance: pass"
