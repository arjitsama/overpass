#!/usr/bin/env bash
# Phase 4 acceptance: passes and greedy planner. Exits non-zero on failure.
#   1. Cached TLE + fixed start -> the pass table equals the golden file.
#   2. Every pass lasts 1-15 minutes and peaks at 10-90 degrees (LEO TLE).
#   3. The planner never overlaps, never picks an unverified or under-tier station, is deterministic.
#   4. Replan removes a station and emits a diff.
#   5. --demo-pass yields a window starting within 5 s of now, lasting 90 s.
set -euo pipefail
cd "$(dirname "$0")/../.."

fail() { echo "FAIL: $*" >&2; exit 1; }
run() {
  local pkg=$1; shift
  local pattern out
  pattern="^($(IFS='|'; echo "$*"))\$"
  out=$(go test -count=1 -v "$pkg" -run "$pattern" 2>&1) || { tail -40 <<<"$out" >&2; fail "go test $pkg $*"; }
  for name in "$@"; do
    grep -q -- "--- PASS: $name " <<<"$out" || fail "$name did not run in $pkg"
  done
  echo "ok"
}

echo "== 1: golden table";        run ./internal/passes TestGoldenTable
                                  run ./cmd/passes TestTableMatchesGolden
echo "== 2: sanity";              run ./internal/passes TestSanity TestWindowEdges TestMalformedFieldsRejected TestStaleOrDecayedTLE
echo "== 3: planner rules";       run ./internal/planner TestPlannerRules TestTierByMode TestScoreFormula TestScheduleRejectsBadInput
echo "== 4: replan diff";         run ./internal/planner TestReplanDiff
echo "== 5: demo pass";           run ./internal/passes TestDemoPass
                                  run ./cmd/passes TestDemoPassFlag

echo "== binary: golden output and a live demo window"
make build >/dev/null
bin/passes -start 2026-09-20T00:00:00Z | diff - internal/passes/testdata/golden-passes.txt >/dev/null || fail "bin/passes output differs from golden"
now=$(date +%s)
json=$(bin/passes -demo-pass -json)
aos=$(jq '.[0].aos' <<<"$json"); los=$(jq '.[0].los' <<<"$json")
(( aos >= now - 5 && aos <= now + 5 && los - aos == 90 )) || fail "demo window $aos-$los at $now"
echo "ok: bin/passes matches golden; demo window starts at now and lasts 90 s"
echo "phase-4 acceptance: pass"
