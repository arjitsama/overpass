#!/usr/bin/env bash
# Pre-ship gate (master plan phase 11 "make preflight"): the full test suite, the
# attack battery against an honest station, and a local smoke. Exits non-zero on
# any failure so it can gate a deploy.
set -uo pipefail
cd "$(dirname "$0")/.."

fail() { echo "PREFLIGHT FAIL: $*" >&2; exit 1; }

echo "== preflight 1/4: go test -race ./..."
rm -f data/*.db* 2>/dev/null || true
go test -race ./... || fail "unit/integration tests"

echo "== preflight 2/4: attack battery against an honest station"
go test -count=1 -run 'TestBatteryHonest' ./cmd/agent || fail "battery vs honest station"

echo "== preflight 3/4: build"
make build >/dev/null || fail "build"

echo "== preflight 4/4: run-local + smoke"
mkdir -p .run
OPS_PORT=8443 STATION_PORT=8444
scripts/run-local.sh >.run/preflight.log 2>&1 &
runner=$!
cleanup() { kill -TERM "$runner" 2>/dev/null || true; wait 2>/dev/null || true; }
trap cleanup EXIT INT TERM

up=""
for _ in $(seq 1 20); do
  if curl -ks --max-time 1 "https://localhost:$OPS_PORT/health" >/dev/null &&
     curl -ks --max-time 1 "https://localhost:$STATION_PORT/health" >/dev/null; then
    up=1; break
  fi
  kill -0 "$runner" 2>/dev/null || { cat .run/preflight.log >&2; fail "agents exited during startup"; }
  sleep 0.5
done
[[ -n $up ]] || { cat .run/preflight.log >&2; fail "agents not answering"; }

SMOKE_OPS_PORT=$OPS_PORT SMOKE_STATION_PORT=$STATION_PORT scripts/smoke.sh || fail "smoke"

echo "preflight: pass"
