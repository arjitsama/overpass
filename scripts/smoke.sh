#!/usr/bin/env bash
# Read-only smoke checks. Two modes:
#
#   scripts/smoke.sh                      # LOCAL: running agents on SMOKE_*_PORT
#   scripts/smoke.sh <base-domain> [--gate]   # PRODUCTION: per-host checks over TLS
#
# Local mode (used by phase-0) checks /health and /events for the ops and station
# agents. Production mode checks, for every Overpass host under <base-domain>:
# /health, the Tier-1 well-known files, the served agent-card hash (bin/cardhash),
# DNS _ans/_ans-badge/TLSA/SVCB, TLSA == SHA-256 of the served cert, and
# bin/agent --verify. It changes nothing. Report-only by default (exit 0);
# --gate makes it exit non-zero if any host is not PASS.
set -uo pipefail
cd "$(dirname "$0")/.."

fail() { echo "FAIL: $*" >&2; exit 1; }

# ---------------- local mode (no base-domain argument) -----------------------
if [[ $# -eq 0 ]]; then
  OPS_PORT=${SMOKE_OPS_PORT:-8443}
  STATION_PORT=${SMOKE_STATION_PORT:-8444}
  CURL=(curl -ks --max-time 5)
  check_health() {
    local name=$1 port=$2 body
    body=$("${CURL[@]}" "https://localhost:$port/health") || fail "$name /health unreachable on $port"
    [[ $body == *'"status":"ok"'* ]] || fail "$name /health returned: $body"
    [[ $body == *"\"role\":\"$name\""* ]] || fail "port $port is not the $name agent: $body"
    echo "ok: $name /health"
  }
  check_started() {
    local name=$1 port=$2 stream
    stream=$(curl -ksN --max-time 2 "https://localhost:$port/events" || true)
    [[ $stream == *'"kind":"agent_started"'* ]] || fail "$name /events had no agent_started"
    echo "ok: $name /events agent_started"
  }
  check_health ops "$OPS_PORT"
  check_health station "$STATION_PORT"
  check_started ops "$OPS_PORT"
  check_started station "$STATION_PORT"
  echo "smoke: pass"
  exit 0
fi

# ---------------- production mode (base-domain given) -------------------------
BASE=$1; shift || true
GATE=0
[[ "${1:-}" == "--gate" ]] && GATE=1
MAXT=${SMOKE_MAX_TIME:-4}
# Registered agents only. The spacecraft is internal (no public ANS identity) and
# gs-sva1bard-eu is the unregistered impostor, so neither is expected to verify;
# they are checked by the demo's refusal beat, not this gate.
HOSTS=(ops authority gs-blacksburg gs-awarua gs-svalbard-eu gs-rogue gs-spare auditor)
# SMOKE_HOSTS="ops authority gs-blacksburg" limits the gate to the hosts that
# are registered so far (staged go-live); the default is every registered host.
if [[ -n ${SMOKE_HOSTS:-} ]]; then read -r -a HOSTS <<< "$SMOKE_HOSTS"; fi
AGENT=bin/agent
CARDHASH=bin/cardhash
# Optional: a config for bin/agent --verify (needs environments + trust roots).
VERIFY_CFG=${SMOKE_VERIFY_CONFIG:-}

have() { command -v "$1" >/dev/null 2>&1; }
overall=0

printf '%-30s %-6s %s\n' "HOST" "STATE" "CHECKS"
for name in "${HOSTS[@]}"; do
  host="$name.$BASE"
  notes=""
  add() { notes="$notes $1"; }

  # Liveness first: if /health is unreachable, mark DOWN and skip the rest.
  if ! curl -ks --max-time "$MAXT" "https://$host/health" >/dev/null 2>&1; then
    printf '%-30s %-6s %s\n' "$host" "DOWN" "unreachable on 443"
    overall=1
    continue
  fi

  body=$(curl -ks --max-time "$MAXT" "https://$host/health" || true)
  [[ $body == *'"status":"ok"'* ]] && add "health" || { add "health!"; overall=1; }

  # Tier-1 well-known files present.
  for wk in ".well-known/agent-card.json" ".well-known/ans/trust-card.json"; do
    code=$(curl -ks -o /dev/null -w '%{http_code}' --max-time "$MAXT" "https://$host/$wk" || echo 000)
    [[ $code == 200 ]] && add "${wk##*/}" || { add "${wk##*/}!"; overall=1; }
  done

  # Served agent-card hash (compare to <NAME>_METADATA_HASH from agents.env if set).
  if [[ -x $CARDHASH ]]; then
    ch=$("$CARDHASH" "https://$host/.well-known/agent-card.json" 2>/dev/null | awk 'NR==1{print $NF}')
    envvar="$(echo "$name" | tr 'a-z-' 'A-Z_')_METADATA_HASH"
    want="${!envvar:-}"
    if [[ -n $want ]]; then
      [[ $ch == "$want" ]] && add "cardhash=meta" || { add "cardhash!=meta"; overall=1; }
    else
      add "cardhash=${ch:0:8}"
    fi
  fi

  # DNS records + TLSA == served cert (needs dig + openssl).
  if have dig && have openssl; then
    for rec in "_ans.$host TXT" "_ans-badge.$host TXT" "_443._tcp.$host TLSA" "$host SVCB"; do
      set -- $rec
      dig +short "$1" "$2" >/dev/null 2>&1 && [[ -n $(dig +short "$1" "$2" 2>/dev/null) ]] \
        && add "$2" || add "$2?"
    done
    served=$(printf '' | openssl s_client -connect "$host:443" -servername "$host" 2>/dev/null \
      | openssl x509 -outform DER 2>/dev/null | openssl dgst -sha256 -r 2>/dev/null | awk '{print $1}')
    tlsa=$(dig +short TLSA "_443._tcp.$host" 2>/dev/null | awk '{print tolower($NF)}' | tr -d '\n')
    if [[ -n $served && -n $tlsa ]]; then
      [[ $tlsa == *"$served"* ]] && add "tlsa=cert" || { add "tlsa!=cert"; overall=1; }
    fi
  else
    add "dns?(need dig/openssl)"
  fi

  # VerifyPeer (only if a verify config was provided).
  if [[ -n $VERIFY_CFG && -x $AGENT ]]; then
    "$AGENT" --config "$VERIFY_CFG" --verify "$host" >/dev/null 2>&1 && add "verify" || { add "verify!"; overall=1; }
  fi

  # A host PASSes when no note ends with '!' (a hard failure).
  if [[ $notes == *"!"* ]]; then
    printf '%-30s %-6s %s\n' "$host" "FAIL" "$notes"
  else
    printf '%-30s %-6s %s\n' "$host" "PASS" "$notes"
  fi
done

if [[ $GATE -eq 1 && $overall -ne 0 ]]; then
  echo "smoke ($BASE): FAIL (gate)"; exit 1
fi
echo "smoke ($BASE): reported (read-only)"
