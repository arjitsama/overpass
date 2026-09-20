#!/usr/bin/env bash
# Phase 12 acceptance: live cross-process Ops flow. Hermetic (no ../ans). Exits
# non-zero on failure.
#   1. Full flow across separate HTTP servers: verify -> get_pass_quote ->
#      issue_mandate (to the authority, over A2A) -> book_pass -> relay_command.
#   2. An unverified peer is refused BEFORE any quote is read.
#   3. bin/opsflow builds and is runnable (the operator points it at real,
#      registered hosts from a prod config after H2 — see docs/deploy-runbook.md).
#
# Note: the happy path uses real HTTP servers, the real a2a.Client, real DPoP
# envelopes and a real authority-signed mandate verified with schema.VerifyMandate.
# A four-OS-process happy path with ENFORCED inbound DPoP additionally needs ANS
# (each station/authority guard fails closed without it), so that is exercised
# against the local ANS stack / real registration, not here. The refusal path is
# real verify.VerifyPeer.
set -uo pipefail
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

echo "== 1: full cross-process flow (verify -> quote -> mandate -> book -> relay)"
run ./internal/opsflow TestRunHappyPath
echo "== 2: unverified peer refused before any quote"
run ./internal/opsflow TestUnverifiedPeerRefusedBeforeQuote

echo "== 3: bin/opsflow builds and shows usage"
go build -o bin/opsflow ./cmd/opsflow || fail "build opsflow"
# Missing required flags must exit non-zero with a usage error (no network).
if bin/opsflow >/dev/null 2>&1; then fail "opsflow with no args should error"; fi
usage=$(bin/opsflow 2>&1 || true)
grep -qi 'required' <<<"$usage" || fail "opsflow did not report a required flag: $usage"
echo "ok"

echo "phase-12 acceptance: pass"
