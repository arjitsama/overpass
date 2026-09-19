#!/usr/bin/env bash
# Print the DNS records for one registered Overpass agent (gate H3). Prints
# only; a human creates them in the GoDaddy DNS UI. Read-only, so no flag.
#
#   scripts/dns-records.sh <host> <version> <agentId> <server-cert.pem> [log-url]
#
# Records (docs/webmesh-spec.md section 6; badge format as served live):
#   _ans.<host>        TXT   v=ans1; version=<ver>; p=a2a; mode=direct; url=https://<host>
#   _ans-badge.<host>  TXT   v=ans-badge1; version=v<ver>; url=<log>/v1/agents/<agentId>
#   _443._tcp.<host>   TLSA  3 0 1 <SHA-256 of the full server certificate DER>
#   <host>             SVCB  1 . alpn=a2a,h2
# DNSSEC must be on for the zone or verifiers will not trust the TLSA record.
set -euo pipefail
[[ $# -ge 4 ]] || { sed -n '2,13p' "$0" >&2; exit 2; }
host=$1 version=$2 agent_id=$3 cert=$4 log=${5:-https://transparency.ans.godaddy.com}
[[ $host =~ ^[A-Za-z0-9.-]+$ ]] || { echo "bad host: $host" >&2; exit 2; }
[[ -f $cert ]] || { echo "no such certificate: $cert" >&2; exit 2; }
tlsa=$(openssl x509 -in "$cert" -outform DER | openssl dgst -sha256 -r | awk '{print $1}')
[[ ${#tlsa} -eq 64 ]] || { echo "could not hash $cert" >&2; exit 1; }
printf '%-28s %-5s %s\n' "_ans.$host" TXT "\"v=ans1; version=$version; p=a2a; mode=direct; url=https://$host\""
printf '%-28s %-5s %s\n' "_ans-badge.$host" TXT "\"v=ans-badge1; version=v$version; url=${log%/}/v1/agents/$agent_id\""
printf '%-28s %-5s %s\n' "_443._tcp.$host" TLSA "3 0 1 $tlsa"
printf '%-28s %-5s %s\n' "$host" SVCB "1 . alpn=a2a,h2"
