#!/usr/bin/env bash
# Register one agent on the LOCAL ANS reference stack and drive it to ACTIVE,
# writing its RA-issued keys and certificates for bin/agent to serve:
#
#   scripts/local-register.sh <host> <version> <out-dir> [agent-config]
#     -> <out-dir>/{identity.key,identity.pem,server.key,server.pem,agent-id}
#
# With an agent config (whose identity/cert paths point into <out-dir> and
# whose card.signed_file is set), the agent first writes its signed card with
# a temporary self-signed identity cert (the card depends only on the key),
# and the registration pins that card: metaDataHash = SHA256:<hex>, with the
# card's skills as function tags. The RA-issued certs then replace the
# temporary ones.
#
# Same lifecycle as the reference repo's run-lifecycle.sh: register (A2A
# endpoint, agent-card metadata URL), publish the ACME challenge into the local
# DNS zone, verify-acme, install the agent's DNS records, verify-dns.
# Refuses any RA that is not on localhost: production registrations are
# permanent and only a human runs them (scripts/register.sh, gate H2).
set -euo pipefail
cd "$(dirname "$0")/.."
[[ $# -eq 3 || $# -eq 4 ]] || { sed -n '2,19p' "$0" >&2; exit 2; }
host=$1 version=$2 out=$3 agent_config=${4:-}
ANS_REPO=${ANS_REPO:-../ans}
RA=${LOCAL_RA_URL:-http://localhost:18080}
KEY=${LOCAL_RA_API_KEY:-ans-dev-key-change-me}   # the reference stack's public dev key
ZONE=$ANS_REPO/data/demo/ans-dns.zone.json
[[ $RA =~ ^http://(localhost|127\.0\.0\.1)(:[0-9]+)?$ ]] || { echo "refusing non-local RA $RA" >&2; exit 1; }
[[ $host =~ ^[A-Za-z0-9.-]+$ && $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "bad host or version" >&2; exit 2; }
[[ -f $ZONE ]] || { echo "no local DNS zone at $ZONE; run scripts/local-ans.sh start" >&2; exit 1; }
mkdir -p "$out" && chmod 700 "$out"
ans="ans://v$version.$host"

csr() { # name cn san  (a config file, because -subj splits on the "/" in ans://)
  printf '[req]\ndistinguished_name=dn\nreq_extensions=ext\nprompt=no\n[dn]\nCN=%s\n[ext]\nsubjectAltName=%s\n' "$2" "$3" > "$out/$1.cnf"
  openssl ecparam -name prime256v1 -genkey -noout -out "$out/$1.key"
  openssl req -new -key "$out/$1.key" -config "$out/$1.cnf" -out "$out/$1.csr"
}
csr identity "$ans" "URI:$ans"
csr server "$host" "DNS:$host"

endpoint=$(jq -n --arg host "$host" '{agentUrl: ("https://" + $host), metaDataUrl: ("https://" + $host + "/.well-known/agent-card.json"), protocol: "A2A", transports: ["JSON_RPC"]}')
if [[ -n $agent_config ]]; then
  # Temporary identity cert so the agent can start; the card depends only on the key.
  openssl req -new -x509 -days 1 -key "$out/identity.key" -config "$out/identity.cnf" -extensions ext -out "$out/identity.pem" 2>/dev/null
  openssl req -new -x509 -days 1 -key "$out/server.key" -config "$out/server.cnf" -extensions ext -out "$out/server.pem" 2>/dev/null
  rm -f "$out/agent-card.json"
  bin/agent --config "$agent_config" --write-card "$out/agent-card.json" 2>/dev/null
  hash="SHA256:$(openssl dgst -sha256 -r < "$out/agent-card.json" | awk '{print $1}')"
  endpoint=$(jq --arg h "$hash" --slurpfile card "$out/agent-card.json" '. + {metaDataHash: $h,
    functions: [$card[0].skills[] | {id: .id[0:64], name: .name[0:64], tags: [.tags[]?[0:20]][0:5]}]}' <<<"$endpoint")
fi
api() { curl -sS --fail-with-body -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' "$@"; }
req=$(jq -n --arg host "$host" --arg v "$version" --arg id "$(cat "$out/identity.csr")" --arg srv "$(cat "$out/server.csr")" --argjson ep "$endpoint" '{
  agentDisplayName: $host, agentDescription: ("Overpass agent " + $host), version: $v, agentHost: $host,
  endpoints: [$ep], identityCsrPEM: $id, serverCsrPEM: $srv }')
id=$(api -X POST "$RA/v2/ans/agents" -d "$req" | jq -r '.agentId')
[[ -n $id && $id != null ]] || { echo "register failed" >&2; exit 1; }
echo "$id" > "$out/agent-id"

pending=$(api "$RA/v2/ans/agents/$id")
name=$(jq -r '.registrationPending.challenges[0].dnsRecord.name' <<<"$pending")
value=$(jq -r '.registrationPending.challenges[0].dnsRecord.value' <<<"$pending")
jq --arg id "$id-acme" --arg n "$name" --arg v "$value" '.records[$id] = [{name:$n, type:"TXT", value:$v, ttl:60}]' "$ZONE" > "$ZONE.tmp" && mv "$ZONE.tmp" "$ZONE"
api -X POST "$RA/v2/ans/agents/$id/verify-acme" >/dev/null
for kind in identity server; do
  api "$RA/v2/ans/agents/$id/certificates/$kind" | jq -r '.[0].certificatePEM' > "$out/$kind.pem"
done
"$ANS_REPO/bin/ans-dns" install --zone "$ZONE" --api-key "$KEY" "$RA" "$id" >/dev/null
api -X POST "$RA/v2/ans/agents/$id/verify-dns" >/dev/null
status=$(api "$RA/v2/ans/agents/$id" | jq -r '.agentStatus')
[[ $status == ACTIVE ]] || { echo "agent $id is $status, not ACTIVE" >&2; exit 1; }
echo "$id"
