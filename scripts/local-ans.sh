#!/usr/bin/env bash
# Start or stop the ANS reference stack (RA, transparency log, Finder and a
# small authoritative DNS server) from a clone of
# https://github.com/agentnameservice/ans next to this repo (master plan 4).
# Everything runs on localhost; nothing touches api.godaddy.com.
#
#   scripts/local-ans.sh start     # ../ans/scripts/demo/start.sh --with-dns (wipes its demo data)
#   scripts/local-ans.sh stop
#   scripts/local-ans.sh env       # prints the environment block for an agent config
#
# Ports: RA 18080, TL 18081 (plain HTTP; badges advertise https://localhost:18081),
# Finder 18082, DNS 127.0.0.1:15353. Needs go, curl, jq, openssl.
# ANS_REPO overrides the clone path.
set -euo pipefail
cd "$(dirname "$0")/.."
ANS_REPO=${ANS_REPO:-../ans}
[[ -x $ANS_REPO/scripts/demo/start.sh ]] || {
  echo "no ANS reference repo at $ANS_REPO; clone it: git clone https://github.com/agentnameservice/ans.git $ANS_REPO" >&2
  exit 1
}
case "${1:-}" in
  start) (cd "$ANS_REPO" && scripts/demo/start.sh --with-dns) ;;
  stop)
    # The reference stop.sh trips over spaces in paths; stop the stack's own
    # processes (ans-ra, ans-tl, ans-finder, ans-dns) by the ports they hold.
    for port in 18080 18081 18082 15353; do
      for pid in $(lsof -ti "tcp:$port" 2>/dev/null) $(lsof -ti "udp:$port" 2>/dev/null); do
        ps -o command= -p "$pid" | grep -q '/bin/ans-' && kill "$pid" 2>/dev/null || true
      done
    done
    sleep 1 ;;
  env)
    key=$(curl -sf http://localhost:18081/root-keys) || { echo "local log not reachable on :18081; run: $0 start" >&2; exit 1; }
    cat <<YAML
  local:
    registry_url: http://localhost:18080
    log_url: http://localhost:18081
    log_public_url: https://localhost:18081
    finder_url: http://localhost:18082/v1
    dns_server: 127.0.0.1:15353
    insecure_http: true
    root_keys:
      - "$key"
YAML
    ;;
  *) sed -n '2,13p' "$0" >&2; exit 2 ;;
esac
