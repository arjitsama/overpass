#!/usr/bin/env bash
# Phase 0 acceptance.
#   1. make build and make test pass on a clean copy of the repo.
#   2. make run-local starts ops and one station; /health is ok on both.
#   3. /events on each carries agent_started.
#   4. The checks fail loudly: smoke against a dead port must exit non-zero.
# Exits non-zero if any check fails.
set -euo pipefail
cd "$(dirname "$0")/../.."
OPS_PORT=8443       # must match configs/local/*.yaml
STATION_PORT=8444

fail() { echo "FAIL: $*" >&2; exit 1; }

echo "== 1: clean copy builds and tests"
tmp=$(mktemp -d)
runner=
cleanup() {
  [[ -n $runner ]] && { kill -TERM "$runner" 2>/dev/null || true; wait "$runner" 2>/dev/null || true; }
  rm -rf "$tmp"
}
trap cleanup EXIT
# Everything git would commit (tracked plus untracked, minus ignored).
git ls-files -co --exclude-standard | while IFS= read -r f; do
  [[ -e $f ]] && { mkdir -p "$tmp/$(dirname "$f")"; cp -p "$f" "$tmp/$f"; }
done
make -C "$tmp" build test >"$tmp.log" 2>&1 || { cat "$tmp.log" >&2; rm -f "$tmp.log"; fail "clean build/test"; }
rm -f "$tmp.log"
echo "ok: clean build and test"

echo "== 2, 3: run-local and smoke"
make build >/dev/null
for port in $OPS_PORT $STATION_PORT; do
  nc -z localhost "$port" 2>/dev/null && fail "port $port already in use; stop whatever is listening there"
done
mkdir -p .run
scripts/run-local.sh >.run/phase-0.log 2>&1 &
runner=$!

up=
for _ in $(seq 1 50); do
  if curl -ks --max-time 1 "https://localhost:$OPS_PORT/health" >/dev/null &&
     curl -ks --max-time 1 "https://localhost:$STATION_PORT/health" >/dev/null; then
    up=1; break
  fi
  kill -0 "$runner" 2>/dev/null || { cat .run/phase-0.log >&2; fail "agents exited during startup"; }
  sleep 0.2
done
[[ -n $up ]] || { cat .run/phase-0.log >&2; fail "agents not answering after 10 s"; }

SMOKE_OPS_PORT=$OPS_PORT SMOKE_STATION_PORT=$STATION_PORT scripts/smoke.sh ||
  { echo "--- agent log ---" >&2; cat .run/phase-0.log >&2; fail "smoke"; }

echo "== 4: smoke fails against a dead port"
if SMOKE_OPS_PORT=$OPS_PORT SMOKE_STATION_PORT=9 scripts/smoke.sh >/dev/null 2>&1; then
  fail "smoke passed against a port with no agent"
fi
echo "ok: smoke exits non-zero on failure"

echo "phase-0 acceptance: pass"
