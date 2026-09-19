#!/usr/bin/env bash
# Start ops and one station locally; Ctrl-C or SIGTERM stops both.
set -euo pipefail
cd "$(dirname "$0")/.."

BIN=bin/agent
[[ -x $BIN ]] || { echo "run 'make build' first" >&2; exit 1; }

pids=()
cleanup() {
  trap - EXIT INT TERM
  for p in ${pids[@]+"${pids[@]}"}; do kill -TERM "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

"$BIN" --config configs/local/ops.yaml &
pids+=($!)
"$BIN" --config configs/local/station.yaml &
pids+=($!)

echo "ops      https://localhost:8443  (/health, /events)" >&2
echo "station  https://localhost:8444  (/health, /events)" >&2
# Portable stand-in for `wait -n` (macOS ships bash 3.2): return when either exits.
while kill -0 "${pids[0]}" 2>/dev/null && kill -0 "${pids[1]}" 2>/dev/null; do
  sleep 1
done
echo "an agent exited; stopping the other" >&2
exit 1
