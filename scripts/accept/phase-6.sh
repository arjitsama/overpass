#!/usr/bin/env bash
# Phase 6 acceptance: pass session. Exits non-zero on failure.
#   1. A command before and after the window is WINDOW_CLOSED; inside it is acked.
#   2. A station-invented command is rejected by the spacecraft; a replayed command on counter.
#   3. After a clean pass the chain heads are byte-identical; a dropped relay gives suspected_drop and unequal evidence.
#   4. Token flips to revoked: cut within one fetch interval, replan follows.
#   5. Token source fails for 20 s: warnings, session stays up, commands flow.
#   6. Token source fails past the max age: cut with token_stale.
#   7. A class outside the mandate is CLASS_REJECTED.
# Plus the whole pass over HTTPS: station and spacecraft agents, real DPoP, a TL-signed status token.
set -euo pipefail
cd "$(dirname "$0")/../.."

fail() { echo "FAIL: $*" >&2; exit 1; }
run() {
  local pkg=$1; shift
  local pattern out
  pattern="^($(IFS='|'; echo "$*"))\$"
  out=$(go test -race -count=1 -v "$pkg" -run "$pattern" 2>&1) || { tail -40 <<<"$out" >&2; fail "go test $pkg $*"; }
  for name in "$@"; do
    grep -q -- "--- PASS: $name " <<<"$out" || fail "$name did not run in $pkg"
  done
  echo "ok"
}

echo "== 1: window";                    run ./internal/ops TestWindow
echo "== 2: no forging, no replay";     run ./internal/ops TestStationCannotForgeOrReplay TestStationRefusesReplay
echo "== 3: chains and drops";          run ./internal/ops TestChainsMatchAndDrop TestRefusedCommandsNotChained
echo "== 4: revocation cuts";           run ./internal/ops TestRevokedCuts TestLoopCutsWithinInterval
echo "== 5: outage warns";              run ./internal/ops TestOutageWarns TestOpenFetchFailureDoesNotCut
echo "== 6: past max age";              run ./internal/ops TestOutagePastMaxAge TestStationStaleOpsToken
echo "== 7: class";                     run ./internal/ops TestClassRejected
echo "== counters, access, HTTP pass";  run ./internal/ops TestCounterSurvivesRestart TestEvidenceAccess
                                        run ./internal/spacecraft TestCounterPersistsAndIsAtomic
                                        run ./cmd/agent TestPassOverHTTP
echo "phase-6 acceptance: pass"
