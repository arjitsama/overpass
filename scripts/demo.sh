#!/usr/bin/env bash
# Cold start to ready for the 3-minute demo (master plan §15). Builds, starts the
# local trust index and the ops + station agents, waits until healthy, prints the
# dashboard URL and the EXACT ans-cli revoke command for the human to paste at the
# revocation beat. It PRINTS that command; it never runs it (gate H6). Ctrl-C
# stops everything. For the full production demo, deploy with deploy/install.sh.
set -uo pipefail
cd "$(dirname "$0")/.."

OPS_PORT=${SMOKE_OPS_PORT:-8443}
STATION_PORT=${SMOKE_STATION_PORT:-8444}
REVOKE_AGENT_ID=${REVOKE_AGENT_ID:-<gs-blacksburg AgentID from agents.env>}

echo "== building =="
make build >/dev/null || { echo "build failed" >&2; exit 1; }

pids=()
cleanup() {
  trap - EXIT INT TERM
  echo; echo "== stopping =="
  for p in ${pids[@]+"${pids[@]}"}; do kill -TERM "$p" 2>/dev/null || true; done
  pkill -f 'agent-trust-discovery -config' 2>/dev/null || true
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

echo "== starting trust index (:8080) =="
( cd third_party/agent-trust-discovery && mkdir -p data && \
  exec go run ./cmd/agent-trust-discovery -config config/overpass-local.runtime.yaml ) >.run/demo-trust.log 2>&1 &
pids+=($!)

echo "== starting ops + station agents =="
mkdir -p .run
scripts/run-local.sh >.run/demo-agents.log 2>&1 &
pids+=($!)

echo "== waiting for /health =="
for _ in $(seq 1 30); do
  if curl -ks --max-time 1 "https://localhost:$OPS_PORT/health" >/dev/null &&
     curl -ks --max-time 1 "https://localhost:$STATION_PORT/health" >/dev/null; then
    ready=1; break
  fi
  sleep 0.5
done
[[ -n ${ready:-} ]] || { echo "agents did not become ready; see .run/demo-*.log" >&2; exit 1; }

cat <<EOF

============================================================
  Overpass is up. Drive the demo from the keyboard only.

  Dashboard:   https://localhost:$OPS_PORT/ui/
  Ops health:  https://localhost:$OPS_PORT/health
  Events (SSE):https://localhost:$OPS_PORT/events

  Demo beats:  docs/demo-runbook.md
  Buttons:     Run demo pass · Ask GoDaddy's agent to verify · Run battery

  AT THE REVOCATION BEAT (1:45), in another terminal, run:

      ans-cli revoke $REVOKE_AGENT_ID --reason CERTIFICATE_HOLD

  (This script does NOT run it. Set REVOKE_AGENT_ID to the real Agent ID.)
============================================================

Press Ctrl-C to stop.
EOF

# Stay up until interrupted or an agent exits.
while true; do sleep 1; done
