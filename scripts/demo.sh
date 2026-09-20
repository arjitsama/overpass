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
# The real revoke is OPTIONAL, done at most once, and only against the spare
# station gs-spare (never gs-blacksburg): ANS revocation is terminal.
SPARE_AGENT_ID=${SPARE_AGENT_ID:-<gs-spare AgentID from agents.env>}

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
  Buttons:     Ask GoDaddy's agent to verify (live) · Simulate compromise (test)
               Run demo pass (recorded) · Run battery (recorded)

  SESSION-CUT BEAT (1:45) — DEFAULT, repeatable for every judge:
      Press "Simulate compromise" on the dashboard (press again to reset), or:
      curl -ksX POST https://localhost:$OPS_PORT/ui/simulate-compromise -d '{"on":true}'
      curl -ksX POST https://localhost:$OPS_PORT/ui/simulate-compromise -d '{"on":false}'   # reset
    A live station with test_controls set can be cut for real (still resettable):
      curl -ksX POST https://<station>/control/compromise -d '{"on":true}'

  OPTIONAL, ONCE, SPARE STATION ONLY — a real terminal revocation:
      ans-cli revoke $SPARE_AGENT_ID --reason CERTIFICATE_HOLD
      (Only gs-spare, never gs-blacksburg. This script does NOT run it.)
============================================================

Press Ctrl-C to stop.
EOF

# Stay up until interrupted or an agent exits.
while true; do sleep 1; done
