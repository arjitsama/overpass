#!/usr/bin/env bash
# Phase 5 acceptance: quote, mandate, booking. Exits non-zero on failure.
#   1. Happy path: quote, mandate, book succeeds once; the same mandate again is MANDATE_REJECTED:consumed.
#   2. One table-driven test per book_pass check, each asserting the exact code (not_owner, overlap included).
#   3. Two concurrent bookings for overlapping windows: exactly one wins (-race, 50 iterations).
#   4. Garbage, oversized and truncated inputs return named codes, never a 500 (plus a 20 s fuzz).
#   5. The authority refuses an uplink mandate for a READ_ONLY station with POLICY_REFUSED:tier.
#   6. payTo in a quote always equals payTo in the card.
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

echo "== 1: happy path, then consumed";  run ./internal/station TestHappyPathThenConsumed
                                         run ./internal/authority TestQuoteMandateBook
                                         run ./cmd/agent TestBookingOverHTTP
echo "== 2: every check, exact codes";   run ./internal/station TestBookPassChecks TestMandateIDReuseStillOverlaps TestNonceScopedToIssuer TestConsumedAfterQuoteTTL
echo "== 3: concurrent overlap x50";     run ./internal/station TestConcurrentOverlap
                                         run ./internal/store TestReplayCache
echo "== 4: hostile inputs";             run ./internal/station TestHostileInputs
                                         run ./internal/authority TestHostileArgs
go test -count=1 ./internal/station -run '^$' -fuzz '^FuzzBookPass$' -fuzztime "${FUZZTIME:-20s}" >/dev/null || fail "fuzz book_pass"
echo "ok (fuzzed book_pass ${FUZZTIME:-20s})"
echo "== 5: READ_ONLY uplink refused";   run ./internal/authority TestPolicyRefusals TestDailyLimitAndStationList
echo "== 6: payTo equals the card";      run ./internal/station TestPayToMatchesPricing
                                         run ./cmd/agent TestBookingOverHTTP
echo "== rogue and registry";            run ./internal/station TestRogueSkipsChecks TestRogueAcceptsHighS TestPriceOverflowRefused
                                         run ./cmd/satreg TestSignThenStationLoads
echo "phase-5 acceptance: pass"
