#!/usr/bin/env bash
# Phase 2 acceptance: identity surface and A2A envelope. Exits non-zero on failure.
#   1. Every generated JSON file parses and has the webmesh-spec section 6 fields we serve.
#   2. The card signature verifies with only the key fetched from the card's own jku.
#   3. Changing any card field without re-signing fails verification.
#   4. A station card never contains noAuth; its claims equal the mounted guards.
#   5. send-message returns the skill list; an unknown method is a JSON-RPC error, not a 500.
#   6. Tier 2 can be switched off and the agent still starts.
# Plus a live run: both local agents serve their files, the station refuses an
# unauthenticated call with a named code, and cardhash agrees with itself.
set -euo pipefail
cd "$(dirname "$0")/../.."

fail() { echo "FAIL: $*" >&2; exit 1; }
# run PKG TEST... runs the named tests and requires each to report PASS, so a
# renamed or deleted test cannot pass as "no tests to run".
run() {
  local pkg=$1; shift
  local pattern out
  pattern="^($(IFS='|'; echo "$*"))\$"
  out=$(go test -count=1 -v "$pkg" -run "$pattern" 2>&1) || { tail -40 <<<"$out" >&2; fail "go test $pkg $*"; }
  for name in "$@"; do
    grep -q -- "--- PASS: $name " <<<"$out" || fail "$name did not run in $pkg"
  done
  echo "ok"
}

echo "== 1: required fields";            run ./internal/wellknown TestRequiredFields TestOmitsUnconfigured TestCardHasNoDefaultValues
echo "== 2: verify via jku";             run ./internal/wellknown TestCardVerifiesViaJKU TestJKUPinning
                                         run ./cmd/agent TestServedCardVerifies
echo "== 3: tamper";                     run ./internal/wellknown TestCardTamper
echo "== 4: claims == mounted";          run ./cmd/agent TestStationCardMatchesMounted
                                         run ./internal/a2a TestDPoPGuardRejectsUnauthenticated TestSkillGuards TestSecurityDeclarations
echo "== 5: JSON-RPC";                   run ./internal/a2a TestSendMessageListsSkills TestJSONRPCErrors TestSkillDispatchBothDialects
                                         run ./cmd/agent TestSendMessageOverHTTP
echo "== 6: tier 2 off";                 run ./internal/wellknown TestTier2Off
                                         run ./cmd/agent TestTier2OffStarts

echo "== extra: forgery, key binding, DPoP success path"
run ./internal/wellknown TestSelfConsistentForgeryRejected TestTrustCardKeyBinding TestIdentityChainChecks
run ./internal/a2a TestDPoPGuardAcceptsProvenCaller TestPartShapeStrict

echo "== live: run-local"
command -v nc >/dev/null || fail "nc is required for the port check"
make build >/dev/null
for port in 8443 8444; do
  nc -z localhost "$port" 2>/dev/null && fail "port $port already in use"
done
mkdir -p .run
scripts/run-local.sh >.run/phase-2.log 2>&1 &
runner=$!
trap 'kill -TERM $runner 2>/dev/null || true; wait $runner 2>/dev/null || true' EXIT
for _ in $(seq 1 50); do
  curl -ks --max-time 1 https://localhost:8444/health >/dev/null && curl -ks --max-time 1 https://localhost:8443/health >/dev/null && break
  sleep 0.2
done

C=(curl -ks --max-time 5)
for p in /.well-known/agent-card.json /.well-known/ans/trust-card.json /health / \
         /.well-known/jwks.json /.well-known/did.json /.well-known/ard.json /.well-known/ai-catalog.json /robots.txt /llms.txt; do
  code=$("${C[@]}" -o /dev/null -w '%{http_code}' "https://localhost:8444$p")
  [[ $code == 200 ]] || fail "station $p returned $code"
done
echo "ok: station serves tier 1 and tier 2"

card=$("${C[@]}" https://localhost:8444/.well-known/agent-card.json)
[[ $card != *noAuth* ]] || fail "station card declares noAuth"
[[ $card == *ansDPoP* && $card == *overpassMandate* ]] || fail "station card lacks its schemes"
echo "ok: station card declares DPoP + mandate, not noAuth"

resp=$("${C[@]}" -w '\n%{http_code}' -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m","role":"ROLE_USER","parts":[{"text":"hi"}]}}}' \
  https://localhost:8444/)
[[ ${resp##*$'\n'} == 401 && $resp == *CALLER_REJECTED* ]] || fail "station accepted or mis-rejected an unauthenticated call: $resp"
echo "ok: station refuses unauthenticated A2A with CALLER_REJECTED"

resp=$("${C[@]}" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m","role":"ROLE_USER","parts":[{"text":"hi"}]}}}' \
  https://localhost:8443/)
[[ $resp == *plan_passes* && $resp == *ROLE_AGENT* ]] || fail "ops SendMessage: $resp"
echo "ok: ops answers SendMessage with its skills"

hashes=$(bin/cardhash -k https://localhost:8444/.well-known/agent-card.json)
raw=$(awk '/^raw_sha256/{print $2}' <<<"$hashes"); jcs=$(awk '/^jcs_sha256/{print $2}' <<<"$hashes")
[[ -n $raw && $raw == "$jcs" ]] || fail "cardhash: $hashes"
echo "ok: cardhash $raw (raw == JCS)"

echo "phase-2 acceptance: pass"
