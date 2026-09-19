#!/usr/bin/env bash
# Register one Overpass agent on ANS with ans-cli (gate H2).
#
# DRY RUN BY DEFAULT: prints the exact ans-cli commands and changes nothing.
# Registrations on production are PERMANENT (the transparency log is
# append-only). A human runs this for real, and only with the long flag:
#
#   scripts/register.sh gs-blacksburg.example.com                  # plan only
#   scripts/register.sh gs-blacksburg.example.com --step csr \
#       --i-am-a-human-and-this-is-permanent                        # one step for real
#
# Steps, in order (ACME and DNS need human action in between):
#   csr          ans-cli generate-csr   (EC P-256 identity and server keys into certs/<host>/)
#   register     ans-cli register       (prints the Agent ID; A2A endpoint + agent-card metadata URL)
#   verify-acme  ans-cli verify-acme <agentId>   after publishing the ACME challenge
#   status       ans-cli status <agentId>
#   certs        ans-cli get-identity-certs / get-server-certs <agentId>
#   verify-dns   ans-cli verify-dns <agentId>    after publishing scripts/dns-records.sh output
#
# Options: --version X.Y.Z (default 0.1.0)  --name "Display name"  --agent-id ID
#          --org NAME (default Overpass)   --function id:name:tag1,tag2 (repeatable)
# Env: ANS_BASE_URL (ans-cli default is OTE; production is https://api.godaddy.com),
#      ANS_API_KEY. Credentials come only from the environment, never flags.
set -euo pipefail

LONG_FLAG="--i-am-a-human-and-this-is-permanent"
host="" version="0.1.0" name="" org="Overpass" agent_id="<agentId>" step="" real=0
functions=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) version=$2; shift 2 ;;
    --name) name=$2; shift 2 ;;
    --org) org=$2; shift 2 ;;
    --agent-id) agent_id=$2; shift 2 ;;
    --function) functions+=("$2"); shift 2 ;;
    --step) step=$2; shift 2 ;;
    "$LONG_FLAG") real=1; shift ;;
    -h|--help) sed -n '2,24p' "$0"; exit 0 ;;
    -*) echo "unknown option $1" >&2; exit 2 ;;
    *) host=$1; shift ;;
  esac
done
[[ -n $host ]] || { echo "usage: $0 <host> [--step STEP] [$LONG_FLAG]" >&2; exit 2; }
[[ $host =~ ^[A-Za-z0-9.-]+$ ]] || { echo "bad host: $host" >&2; exit 2; }
[[ -n $name ]] || name=$host
# Values become argv fields separated by \x1f, one command per line: refuse
# anything that could split a field or a line.
for v in "$name" "$org" "$version" "$agent_id" ${functions[@]+"${functions[@]}"}; do
  [[ $v != *$'\n'* && $v != *$'\r'* && $v != *$'\x1f'* ]] || { echo "values may not contain newlines" >&2; exit 2; }
done
out="certs/$host"

fn_flags=()
for f in ${functions[@]+"${functions[@]}"}; do fn_flags+=(--function "$f"); done

# cmds STEP fills CMDS with one argv per line (fields separated by \x1f), so
# commands run as argv arrays: nothing is ever re-parsed by the shell.
cmds() {
  local US=$'\x1f'
  join() { local IFS=$US; echo "$*"; }
  case "$1" in
    csr) join ans-cli generate-csr --host "$host" --org "$org" --version "$version" --key-type ec --out-dir "$out" ;;
    register) join ans-cli register --name "$name" --host "$host" --version "$version" \
        --identity-csr "$out/identity.csr" --server-csr "$out/server.csr" \
        --endpoint-protocol A2A --endpoint-url "https://$host" \
        --metadata-url "https://$host/.well-known/agent-card.json" ${fn_flags[@]+"${fn_flags[@]}"} ;;
    verify-acme) join ans-cli verify-acme "$agent_id" ;;
    status) join ans-cli status "$agent_id" ;;
    certs) join ans-cli get-identity-certs "$agent_id"; join ans-cli get-server-certs "$agent_id" ;;
    verify-dns) join ans-cli verify-dns "$agent_id" ;;
    *) echo "unknown step $1" >&2; return 2 ;;
  esac
}

# plan STEP prints the step's commands, shell-quoted.
plan() {
  local line
  cmds "$1" | while IFS=$'\x1f' read -r -a argv; do
    printf '%q ' "${argv[@]}"; echo
  done
}

steps=(csr register verify-acme status certs verify-dns)
if [[ $real -eq 0 ]]; then
  echo "# DRY RUN: nothing was executed. ANS_BASE_URL=${ANS_BASE_URL:-<unset: ans-cli defaults to OTE>}"
  echo "# Registrations are permanent. To run one step for real a human passes:"
  echo "#   --step <step> $LONG_FLAG"
  for s in ${step:-${steps[@]}}; do echo "## $s"; plan "$s"; done
  exit 0
fi

# Real run: exactly one step, never the whole chain, so a human looks at
# each result (ACME challenge, DNS records) before the next.
[[ -n $step ]] || { echo "a real run needs --step (one of: ${steps[*]})" >&2; exit 2; }
[[ -n ${ANS_BASE_URL:-} ]] || { echo "set ANS_BASE_URL explicitly for a real run" >&2; exit 2; }
if [[ $step != csr && $agent_id == "<agentId>" && $step != register ]]; then
  echo "--agent-id is required for $step" >&2; exit 2
fi
command -v ans-cli >/dev/null || { echo "ans-cli not on PATH (brew install agentnameservice/ans/ans-cli)" >&2; exit 1; }
[[ $step == csr ]] && mkdir -p "$out" && chmod 700 "$out"
echo "# running against $ANS_BASE_URL:"
cmds "$step" >/dev/null || exit 2
while IFS=$'\x1f' read -r -a argv; do
  printf '+ '; printf '%q ' "${argv[@]}"; echo
  "${argv[@]}"
done < <(cmds "$step")
