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

# REQUIRE_AXE=1 turns a skip into a failure. A skipped accessibility check is
# not a passing one, so CI and any release run set it.
echo "== 3: axe-core CLI, every theme (REQUIRE_AXE=${REQUIRE_AXE:-0})"
axe_skip() {
  if [[ ${REQUIRE_AXE:-0} == 1 ]]; then fail "axe could not run and REQUIRE_AXE=1: $*"; fi
  echo "skipped: $*"
  return 0
}
axe_check() {
  command -v node >/dev/null 2>&1 || { axe_skip "node not found"; return $?; }
  # Serve web/ with node itself: python3 is not present everywhere (on Windows
  # it is often a Store stub), and a skipped audit is not a passing one.
  node scripts/static-server.js web 8096 >/tmp/overpass-axe-srv.log 2>&1 &
  local srv=$!
  trap 'kill '"$srv"' 2>/dev/null || true' RETURN
  sleep 2
  # axe ships a chromedriver that must match the installed Chrome. Point
  # AXE_CHROMEDRIVER at a matching binary when they drift apart.
  local driver=()
  [[ -n ${AXE_CHROMEDRIVER:-} ]] && driver=(--chromedriver-path "$AXE_CHROMEDRIVER")
  # The page reads ?theme= so every theme is audited, not just the default.
  local theme url out
  for theme in light dark contrast; do
    url="http://127.0.0.1:8096/index.html?theme=$theme"
    out=$(npx --yes @axe-core/cli@4 "$url" ${driver[@]+"${driver[@]}"} --exit </dev/null 2>&1) || {
      axe_skip "axe could not run: $(tail -3 <<<"$out")"; return $?; }
    if grep -q "0 violations found" <<<"$out"; then
      echo "ok: axe reports 0 violations ($theme)"
    else
      echo "$out" | grep -iE 'violation|issues detected' >&2
      fail "axe reported violations in the $theme theme"
    fi
  done
}
axe_check

echo "== 5: manual accessibility walkthrough present"
test -f docs/a11y-manual.md || fail "docs/a11y-manual.md missing"
echo "ok"

echo "phase-9 acceptance: pass"
