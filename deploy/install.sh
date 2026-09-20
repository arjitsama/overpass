#!/usr/bin/env bash
# Overpass VPS installer (gate H4). Idempotent. DRY RUN BY DEFAULT: prints every
# action and changes nothing. Pass --apply to perform them. Run as root on the
# VPS, from a checkout of this repo, with /etc/overpass/agents.env already filled
# in from deploy/agents.env.example. Secrets are read from that file / the
# environment and are NEVER written into the repo.
#
#   sudo deploy/install.sh              # dry run: prints the plan
#   sudo deploy/install.sh --apply      # performs it
set -euo pipefail

APPLY=0
[[ "${1:-}" == "--apply" ]] && APPLY=1
[[ "${1:-}" == "--dry-run" ]] && APPLY=0

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
ENV_FILE=/etc/overpass/agents.env
# Registered agents + the internal spacecraft + the unregistered impostor. All
# run as processes; only the registered ones get ANS identities (see H2).
AGENTS=(ops authority gs-blacksburg gs-awarua gs-svalbard-eu gs-rogue gs-spare auditor spacecraft gs-sva1bard-eu)

say() { printf '%s\n' "$*"; }
run() {
  if [[ $APPLY -eq 1 ]]; then
    say "+ $*"; "$@"
  else
    say "DRY-RUN would: $*"
  fi
}
render() { # render <src-template> <dest>
  local src=$1 dest=$2
  if [[ $APPLY -eq 1 ]]; then
    say "+ envsubst < $src > $dest"
    # Load agents.env into the environment, then expand ${VAR} in the template.
    set -a; # shellcheck disable=SC1090
    . "$ENV_FILE"; set +a
    # Expand ONLY the variables agents.env defines, so nginx's own $variables
    # ($ssl_preread_server_name, $overpass_upstream) survive untouched.
    local vars
    vars=$(grep -oE '^[A-Z_][A-Z0-9_]*=' "$ENV_FILE" | sed 's/=$//' | sed 's/.*/${&}/' | tr '\n' ' ')
    envsubst "$vars" < "$src" > "$dest"
  else
    say "DRY-RUN would: envsubst < $src > $dest"
  fi
}

command -v envsubst >/dev/null 2>&1 || { echo "need 'envsubst' (package gettext-base)"; [[ $APPLY -eq 1 ]] && exit 2; }

say "== Overpass install ($([[ $APPLY -eq 1 ]] && echo APPLY || echo DRY-RUN)) from $REPO_DIR =="

# 1. user, dirs
if [[ $APPLY -eq 1 ]]; then
  id -u overpass >/dev/null 2>&1 || run useradd --system --home /var/lib/overpass --shell /usr/sbin/nologin overpass
else
  say "DRY-RUN would: ensure system user 'overpass' exists (useradd if missing)"
fi
run install -d -m 0750 -o overpass -g overpass /var/lib/overpass
run install -d -m 0755 /etc/overpass /etc/overpass/certs

# 2. binary
run install -m 0755 "$REPO_DIR/bin/agent" /usr/local/bin/agent

# 3. env file check (never created here; the human provides it)
if [[ $APPLY -eq 1 && ! -f $ENV_FILE ]]; then
  echo "missing $ENV_FILE -- copy deploy/agents.env.example there and fill it in"; exit 2
fi
say "config source: $ENV_FILE (not modified)"

# 4. render per-agent configs. AUTHORITY_CONFIG=authority-tonight renders
# deploy/prod/authority-tonight.yaml as /etc/overpass/authority.yaml (the
# operator allow-list variant for the first live pass); default authority.yaml.
AUTHORITY_CONFIG=${AUTHORITY_CONFIG:-authority}
for a in "${AGENTS[@]}"; do
  src=$a
  [[ $a == authority ]] && src=$AUTHORITY_CONFIG
  render "$REPO_DIR/deploy/prod/$src.yaml" "/etc/overpass/$a.yaml"
done

# 4b. the attack battery's own config (not an agent: it is the red-team tool),
# plus the records the dashboard reads. The battery record is produced by
# `battery run -record` and committed; the fraud log is written by hand.
render "$REPO_DIR/deploy/prod/battery-live.yaml" "/etc/overpass/battery-live.yaml"
for rec in docs/status/battery-live.json docs/status/fraud-redteam.md; do
  if [[ -f "$REPO_DIR/$rec" ]]; then
    run install -m 0644 "$REPO_DIR/$rec" "/etc/overpass/$(basename "$rec")"
  else
    say "note: $rec not present; the dashboard will say no live run is recorded"
  fi
done

# 5. systemd unit
run install -m 0644 "$REPO_DIR/deploy/overpass@.service" /etc/systemd/system/overpass@.service
run systemctl daemon-reload

# 6. nginx SNI routing
run install -d /etc/nginx/streams-enabled
render "$REPO_DIR/deploy/nginx-sni.conf.template" "/etc/nginx/streams-enabled/overpass.conf"
say "note: ensure nginx.conf has 'include /etc/nginx/streams-enabled/*.conf;' at top level (outside http{})"

# 6b. bare domain (apex + www) -> the dashboard, over an ordinary Let's Encrypt
# certificate. No agent's certificate, DNS record or registration is touched.
# Before the certificate exists only the port-80 ACME/redirect server can be
# rendered; once certbot has issued, the full config adds the TLS redirect.
run install -d /var/www/acme
WEB_CERT=/etc/letsencrypt/live/${BASE_DOMAIN:-blacksburgbytes.club}/fullchain.pem
if [[ -f $WEB_CERT ]]; then
  render "$REPO_DIR/deploy/nginx-web.conf.template" "/etc/nginx/sites-enabled/overpass-web.conf"
else
  say "note: $WEB_CERT missing; rendering the port-80 bootstrap only. Then run:"
  say "  certbot certonly --webroot -w /var/www/acme -d \${BASE_DOMAIN} -d www.\${BASE_DOMAIN} \\"
  say "    --agree-tos -m <email> -n --deploy-hook 'systemctl reload nginx'"
  say "  and re-run this script to add the TLS redirect."
  render "$REPO_DIR/deploy/nginx-web-bootstrap.conf.template" "/etc/nginx/sites-enabled/overpass-web.conf"
fi
# The stock default site also answers on :80 and would shadow ours.
if [[ -e /etc/nginx/sites-enabled/default ]]; then
  run rm -f /etc/nginx/sites-enabled/default
fi

run nginx -t
run systemctl reload nginx

# 7. enable services (start is a separate, explicit human step after certs are in place)
for a in "${AGENTS[@]}"; do
  run systemctl enable "overpass@$a"
done

say ""
say "next (human): put per-host certs under /etc/overpass/certs/<name>/, add secrets via"
say "  systemctl edit overpass@<name>  (Environment=TRUST_ADMIN_KEY=... etc.), then"
say "  systemctl start overpass@ops overpass@authority overpass@gs-blacksburg ... and"
say "  scripts/smoke.sh \"\${BASE_DOMAIN:-blacksburgbytes.club}\""
say "== done ($([[ $APPLY -eq 1 ]] && echo applied || echo dry-run)) =="
