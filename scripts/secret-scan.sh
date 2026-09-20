#!/usr/bin/env bash
# Fail if any file that could be committed contains a private key, certificate,
# or an obvious API token/secret. Scans tracked AND untracked-not-ignored files
# (so a leak is caught before it is committed); .gitignore already excludes
# certs/, *.key, *.pem and .env. Read-only. Wired into `make lint`.
set -uo pipefail
cd "$(git rev-parse --show-toplevel 2>/dev/null || echo .)"

# name=pattern (extended regex). PRIVATE KEY / CERTIFICATE catch PEM material;
# the rest catch common token shapes and secret-looking assignments.
patterns=(
  'private-key:-----BEGIN ([A-Z0-9 ]+ )?PRIVATE KEY-----'
  'certificate:-----BEGIN CERTIFICATE-----'
  'anthropic-key:sk-ant-[A-Za-z0-9_-]{20,}'
  'openai-key:sk-[A-Za-z0-9]{32,}'
  'aws-key:AKIA[0-9A-Z]{16}'
  'github-pat:ghp_[A-Za-z0-9]{36}'
  'slack-token:xox[baprs]-[A-Za-z0-9-]{10,}'
  'secret-assign:(API_KEY|APIKEY|SECRET|ACCESS_TOKEN|BEARER_TOKEN|PRIVATE_KEY)[[:alnum:]_]*[[:space:]]*[:=][[:space:]]*['"'"'"]?[A-Za-z0-9/+_.=-]{20,}'
)

# Files in scope: tracked + untracked, honoring .gitignore. Exclude *.example
# templates and this scanner (which necessarily names the patterns).
hits=0
while IFS= read -r -d '' f; do
  [[ -f "$f" ]] || continue
  case "$f" in
    *.example|scripts/secret-scan.sh) continue ;;
  esac
  for p in "${patterns[@]}"; do
    name=${p%%:*}; re=${p#*:}
    if match=$(grep -nEI -- "$re" "$f" 2>/dev/null); then
      # Ignore lines that are clearly placeholders, not real values.
      match=$(grep -vE '<[^>]*>|REPLACE|EXAMPLE|example|xxxx|\.\.\.|\$\{' <<<"$match" || true)
      if [[ -n "$match" ]]; then
        echo "SECRET ($name) in $f:" >&2
        sed 's/^/  /' <<<"$match" >&2
        hits=1
      fi
    fi
  done
done < <(git ls-files -co --exclude-standard -z)

if [[ $hits -ne 0 ]]; then
  echo "secret-scan: FAIL -- remove the secrets above (they must never be committed)" >&2
  exit 1
fi
echo "secret-scan: clean"
