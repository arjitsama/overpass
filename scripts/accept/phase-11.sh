#!/usr/bin/env bash
# Phase 11 acceptance: deploy, demo, submission. Exits non-zero on failure.
#   1. deploy/install.sh --dry-run prints every action and changes nothing.
#   2. scripts/smoke.sh local profile passes; production mode runs read-only per host.
#   3. make preflight passes locally.
#   4. Secret scan is in `make lint` and fails on a planted fake key.
#   5. README quick start works on a clean checkout (build + vet).
set -uo pipefail
cd "$(dirname "$0")/../.."

fail() { echo "FAIL: $*" >&2; exit 1; }

echo "== 1: install.sh --dry-run changes nothing and prints actions"
before=$(git status --porcelain | sort | md5 2>/dev/null || git status --porcelain | sort | md5sum)
out=$(bash deploy/install.sh --dry-run 2>&1) || fail "install.sh --dry-run exited non-zero"
grep -q "DRY-RUN would:" <<<"$out" || fail "dry-run printed no actions"
grep -q "envsubst .*deploy/prod/ops.yaml" <<<"$out" || fail "dry-run did not plan the ops config"
after=$(git status --porcelain | sort | md5 2>/dev/null || git status --porcelain | sort | md5sum)
[[ "$before" == "$after" ]] || fail "install.sh --dry-run changed the working tree"
echo "ok"

echo "== 2a: local smoke (run-local + /health + /events)"
make build >/dev/null || fail "build"
mkdir -p .run
scripts/run-local.sh >.run/phase-11.log 2>&1 &
runner=$!
cleanup() { kill -TERM "$runner" 2>/dev/null || true; wait 2>/dev/null || true; }
trap cleanup EXIT INT TERM
up=""
for _ in $(seq 1 20); do
  if curl -ks --max-time 1 https://localhost:8443/health >/dev/null &&
     curl -ks --max-time 1 https://localhost:8444/health >/dev/null; then up=1; break; fi
  kill -0 "$runner" 2>/dev/null || { cat .run/phase-11.log >&2; fail "agents exited"; }
  sleep 0.5
done
[[ -n $up ]] || { cat .run/phase-11.log >&2; fail "agents not up"; }
scripts/smoke.sh >/dev/null || fail "local smoke"
cleanup; trap - EXIT INT TERM
echo "ok"

echo "== 2b: production smoke runs read-only and reports per host"
pout=$(SMOKE_MAX_TIME=2 scripts/smoke.sh overpass.invalid 2>&1) || fail "production smoke exited non-zero (report mode)"
grep -q "ops.overpass.invalid" <<<"$pout" || fail "production smoke printed no per-host rows"
grep -q "reported (read-only)" <<<"$pout" || fail "production smoke did not report"
echo "ok"

echo "== 4: secret scan wired into lint and fails on a planted key"
grep -q "secret-scan.sh" Makefile || fail "secret-scan not in Makefile lint"
scripts/secret-scan.sh >/dev/null || fail "secret scan should be clean on the repo"
# Build the PEM header from split strings so THIS script's source does not itself
# contain the pattern (the written file does), then confirm the scanner catches it.
beg="-----BEGIN EC PRIVATE"; fin="-----END EC PRIVATE"
printf -- '%s KEY-----\nMHcCAQEEIA0123456789abcdef0123456789abcdef0123456789abcdefoAoGCC\n%s KEY-----\n' "$beg" "$fin" > .planted-secret.md
if scripts/secret-scan.sh >/dev/null 2>&1; then rm -f .planted-secret.md; fail "secret scan missed a planted private key"; fi
rm -f .planted-secret.md
echo "ok"

echo "== 5: README quick start works on a clean checkout"
tmp=$(mktemp -d)
# Copy the current tracked + untracked-non-ignored files (a fresh clone of HEAD+wip).
git ls-files -co --exclude-standard -z | rsync -a --files-from=- -0 . "$tmp/" 2>/dev/null || \
  git ls-files -co --exclude-standard | (cd . && tar -cf - -T -) | (cd "$tmp" && tar -xf -)
( cd "$tmp" && make build >/dev/null && go vet ./... >/dev/null ) || { rm -rf "$tmp"; fail "clean checkout build/vet"; }
rm -rf "$tmp"
echo "ok"

echo "== 3: make preflight (full: tests + honest battery + local smoke)"
make preflight >/dev/null 2>&1 || fail "make preflight"
echo "ok"

echo "phase-11 acceptance: pass"
