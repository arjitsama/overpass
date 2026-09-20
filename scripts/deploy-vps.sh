#!/usr/bin/env bash
# Push the current checkout to the VPS: build the Linux binaries, copy them and
# the deploy tree, re-render the configs and nginx, restart Ops.
#
# DRY RUN BY DEFAULT: prints what it would do. Pass --apply to perform it.
#
#   scripts/deploy-vps.sh                       # plan
#   scripts/deploy-vps.sh --apply               # do it
#   VPS=root@1.2.3.4 scripts/deploy-vps.sh --apply
#
# It never touches an agent's certificate, DNS record or ANS registration, and
# it renders the authority config the box already runs (AUTHORITY_CONFIG below)
# so a deploy cannot silently swap the authority's flight rules.
set -euo pipefail
cd "$(dirname "$0")/.."

VPS=${VPS:-root@ops.blacksburgbytes.club}
# The box runs the operator-allow-list variant for the live pass; install.sh
# would otherwise render deploy/prod/authority.yaml over it.
AUTHORITY_CONFIG=${AUTHORITY_CONFIG:-authority-tonight}
APPLY=0
[[ "${1:-}" == "--apply" ]] && APPLY=1

say() { printf '%s\n' "$*"; }
run() {
  if [[ $APPLY -eq 1 ]]; then say "+ $*"; "$@"; else say "DRY-RUN would: $*"; fi
}
remote() {
  if [[ $APPLY -eq 1 ]]; then say "+ ssh $VPS $*"; ssh "$VPS" "$@"; else say "DRY-RUN would: ssh $VPS $*"; fi
}

say "== deploy to $VPS ($([[ $APPLY -eq 1 ]] && echo APPLY || echo DRY-RUN)) =="

# 1. build for the box (pure Go: no cgo, so this cross-compiles from anywhere)
run env GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/linux/ \
  ./cmd/agent ./cmd/battery ./cmd/opsflow

# 2. copy the binaries and the deploy tree (certs and secrets stay on the box)
if [[ $APPLY -eq 1 ]]; then
  say "+ scp bin/linux/{agent,battery,opsflow} $VPS:/tmp/"
  scp bin/linux/agent bin/linux/battery bin/linux/opsflow "$VPS:/tmp/"
  say "+ scp -r deploy docs/status $VPS:/tmp/overpass-deploy/"
  ssh "$VPS" 'rm -rf /tmp/overpass-deploy && mkdir -p /tmp/overpass-deploy/docs'
  scp -r deploy "$VPS:/tmp/overpass-deploy/deploy"
  scp -r docs/status "$VPS:/tmp/overpass-deploy/docs/status"
else
  say "DRY-RUN would: scp bin/linux/{agent,battery,opsflow} and deploy/, docs/status/ to $VPS"
fi

# 3. install the binaries, re-render configs + nginx, restart Ops
remote "install -m 0755 /tmp/agent /usr/local/bin/agent"
remote "install -m 0755 /tmp/battery /usr/local/bin/battery"
remote "install -m 0755 /tmp/opsflow /usr/local/bin/opsflow"
remote "cd /tmp/overpass-deploy && AUTHORITY_CONFIG=$AUTHORITY_CONFIG deploy/install.sh --apply"
remote "systemctl restart overpass@ops"

# 4. confirm it came back
remote "sleep 2; curl -sS -o /dev/null -w 'health %{http_code}\n' https://ops.\$(grep -oP '(?<=^BASE_DOMAIN=).*' /etc/overpass/agents.env)/health"

say ""
say "next: check the dashboard, and that /events stays open:"
say "  curl -N --max-time 60 https://ops.<domain>/events | head"
say "== done ($([[ $APPLY -eq 1 ]] && echo applied || echo dry-run)) =="
