#!/usr/bin/env bash
# Check running local agents: /health is ok and /events carries agent_started.
# Exits non-zero on the first failure. Ports: SMOKE_OPS_PORT, SMOKE_STATION_PORT.
set -uo pipefail

OPS_PORT=${SMOKE_OPS_PORT:-8443}
STATION_PORT=${SMOKE_STATION_PORT:-8444}
# -k: local agents use throwaway self-signed certs.
CURL=(curl -ks --max-time 5)

fail() { echo "FAIL: $*" >&2; exit 1; }

check_health() {
  local name=$1 port=$2 body
  body=$("${CURL[@]}" "https://localhost:$port/health") || fail "$name /health unreachable on $port"
  [[ $body == *'"status":"ok"'* ]] || fail "$name /health returned: $body"
  [[ $body == *"\"role\":\"$name\""* ]] || fail "port $port is not the $name agent: $body"
  echo "ok: $name /health"
}

check_started() {
  local name=$1 port=$2 stream
  # The stream never ends by itself; curl exits 28 on --max-time, which is expected.
  stream=$(curl -ksN --max-time 2 "https://localhost:$port/events" || true)
  [[ $stream == *'"kind":"agent_started"'* ]] || fail "$name /events had no agent_started"
  echo "ok: $name /events agent_started"
}

check_health ops "$OPS_PORT"
check_health station "$STATION_PORT"
check_started ops "$OPS_PORT"
check_started station "$STATION_PORT"
echo "smoke: pass"
