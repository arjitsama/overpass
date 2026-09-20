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
    # The templates contain only ${KNOWN_VAR} references and no literal '$'.
    set -a; # shellcheck disable=SC1090
    . "$ENV_FILE"; set +a
    envsubst < "$src" > "$dest"
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

# 4. render per-agent configs
for a in "${AGENTS[@]}"; do
  render "$REPO_DIR/deploy/prod/$a.yaml" "/etc/overpass/$a.yaml"
done

# 5. systemd unit
run install -m 0644 "$REPO_DIR/deploy/overpass@.service" /etc/systemd/system/overpass@.service
run systemctl daemon-reload

# 6. nginx SNI routing
run install -d /etc/nginx/streams-enabled
render "$REPO_DIR/deploy/nginx-sni.conf.template" "/etc/nginx/streams-enabled/overpass.conf"
say "note: ensure nginx.conf has 'include /etc/nginx/streams-enabled/*.conf;' at top level (outside http{})"
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
