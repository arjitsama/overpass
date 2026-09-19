#!/usr/bin/env bash
# Phase 7 acceptance: adversaries, battery, auditor. Exits non-zero on failure.
#   1. run_battery against an honest station: every check BLOCKED (23/23).
#   2. run_battery against gs-rogue: tamper_mandate and wrong_dpop_key_attack VULNERABLE.
#   3. The impostor is refused at verification before any quote is requested.
#   4. The registered lookalike verifies; an uplink mandate for it is POLICY_REFUSED:tier.
#   5. Canary: honest stations reject both probes; the rogue accepts and the report says CANARY_ACCEPTED.
#   6. A clean pass produces a report with verdict pass and equal chain heads.
#   7. The battery gate exits non-zero unless every honest-target check is BLOCKED.
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

echo "== 1 and 7: honest station, all BLOCKED, gate passes";  run ./cmd/agent TestBatteryHonest
echo "== 2 and 7: rogue station VULNERABLE, gate fails";       run ./cmd/agent TestBatteryRogue
echo "== 3: impostor refused before any quote";               run ./cmd/agent TestImpostorRefusedNoQuote
echo "== 4: lookalike verifies, uplink refused on tier";      run ./internal/authority TestLookalikeVerifiesButNoUplink
echo "== 5: canary (honest rejects, rogue CANARY_ACCEPTED)";  run ./cmd/agent TestCanaryLive
                                                              run ./internal/auditor TestCanaryReport
echo "== 6: clean pass report, equal heads";                  run ./internal/auditor TestAuditCleanPass TestAuditCatchesTampering
echo "== 7: battery gate exit codes";                         run ./cmd/battery TestGate TestRender TestNamesCoverRunOrder
echo "phase-7 acceptance: pass"
