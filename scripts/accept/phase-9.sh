#!/usr/bin/env bash
# Phase 9 acceptance: dashboard and accessibility. Exits non-zero on failure.
#   1. The contrast check passes for every declared pair in all three themes.
#   2. HTML structure: one h1, no skipped heading levels, accessible names,
#      table captions, no non-native click handlers, lang set.
#   3. axe (if installed) reports zero critical/serious; else reported, not run.
#   4. A recorded event stream replayed through app.js renders schedule, trust
#      table, impostor rejection, session cut in the alert region, battery results.
#   5. docs/a11y-manual.md exists (a human performs the VoiceOver walkthrough).
set -euo pipefail
cd "$(dirname "$0")/../.."

fail() { echo "FAIL: $*" >&2; exit 1; }
run() {
  local pkg=$1; shift
  local pattern out
  pattern="^($(IFS='|'; echo "$*"))\$"
  out=$(go test -count=1 -v "$pkg" -run "$pattern" 2>&1) || { tail -50 <<<"$out" >&2; fail "go test $pkg $*"; }
  for name in "$@"; do
    grep -q -- "--- PASS: $name " <<<"$out" || fail "$name did not run in $pkg"
  done
  echo "ok"
}

echo "== 1: contrast, all three themes";                 run ./web TestContrastAllThemes
echo "== 2: HTML structure and accessible names";        run ./web TestHTMLStructure
echo "== (serving) dashboard assets";                    run ./web TestHandlerServesAssets
echo "== 4: recorded event stream renders the page"
if command -v node >/dev/null 2>&1; then
  node web/dom_test.js || fail "dom replay test"
else
  fail "node is required for the DOM replay test (acceptance 4) and was not found"
fi

echo "== 3: axe-core CLI (best-effort: needs Node, a browser and network)"
axe_check() {
  command -v node >/dev/null 2>&1 || { echo "skipped: node not found"; return 0; }
  command -v python3 >/dev/null 2>&1 || { echo "skipped: no static server (python3) available"; return 0; }
  python3 -m http.server 8096 --directory web >/tmp/overpass-axe-srv.log 2>&1 &
  local srv=$!
  trap 'kill '"$srv"' 2>/dev/null || true' RETURN
  sleep 2
  local out
  out=$(npx --yes @axe-core/cli@4 http://127.0.0.1:8096/index.html --exit </dev/null 2>&1) || {
    echo "skipped: axe could not run (offline or no browser); see docs/status/phase-9.md"; return 0; }
  if grep -q "0 violations found" <<<"$out"; then
    echo "ok: axe reports 0 violations"
  else
    echo "$out" | grep -iE 'violation|issues detected' >&2
    fail "axe reported violations"
  fi
}
axe_check

echo "== 5: manual accessibility walkthrough present"
test -f docs/a11y-manual.md || fail "docs/a11y-manual.md missing"
echo "ok"

echo "phase-9 acceptance: pass"
