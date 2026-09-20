#!/usr/bin/env bash
# Phase 10 acceptance: LLM planner, bounded by policy. Exits non-zero on failure.
#   1. A model that proposes the lookalike for uplink is refused by the authority
#      with POLICY_REFUSED:tier and nothing is booked.
#   2. The injected station note never reaches the model prompt (captured request).
#   3. Timeout and error both fall back to greedy and still book a pass.
#   4. With the real client and ANS_LIVE_LLM=1, an anomaly context changes the
#      chosen pass versus greedy; skipped otherwise (reported).
set -euo pipefail
cd "$(dirname "$0")/../.."

fail() { echo "FAIL: $*" >&2; exit 1; }
run() {
  local pkg=$1; shift
  local pattern out
  pattern="^($(IFS='|'; echo "$*"))\$"
  out=$(go test -race -count=1 -v "$pkg" -run "$pattern" 2>&1) || { tail -60 <<<"$out" >&2; fail "go test $pkg $*"; }
  for name in "$@"; do
    grep -q -- "--- PASS: $name " <<<"$out" || fail "$name did not run in $pkg"
  done
  echo "ok"
}

echo "== 1: lookalike uplink proposal refused on tier, nothing booked";  run ./internal/llmplan TestProposeLookalikeRefusedOnTier
echo "== 2: injected note never reaches the prompt";                      run ./internal/llmplan TestInjectionNeverReachesPrompt
echo "== 3: timeout and error fall back to greedy and still book";       run ./internal/llmplan TestFallsBackToGreedy
echo "== (happy path) a proposed good station is booked";                 run ./internal/llmplan TestProposeGoodStationBooks

echo "== 4: real client anomaly changes the plan (ANS_LIVE_LLM=1)"
if [ "${ANS_LIVE_LLM:-}" = "1" ] && [ -n "${ANTHROPIC_API_KEY:-}" ]; then
  run ./internal/llmplan TestLiveAnomalyChangesPlan
else
  echo "skipped: set ANS_LIVE_LLM=1 and ANTHROPIC_API_KEY to run the live model test"
fi

echo "phase-10 acceptance: pass"
