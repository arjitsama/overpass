#!/usr/bin/env bash
# Phase 3 acceptance: verification. Exits non-zero on failure.
#   1. Local ANS stack: a registered agent verifies (all 8 checks), an unregistered one fails with reasons.
#   2. Live read-only check of agent.webmesh.ai: only with ANS_LIVE=1 (fixture: internal/verify/testdata).
#   3. Forged jku -> CARD_REJECTED:jku with zero requests to the forged host.
#   4. Status token policy table.
#   5. No valid DPoP proof -> named rejection; replayed proof rejected.
#   6. register.sh without the long flag changes nothing and exits 0 after printing the plan.
#   7. Every check emits a bus event.
# The local stack comes from ../ans (git clone https://github.com/agentnameservice/ans.git ../ans).
set -euo pipefail
cd "$(dirname "$0")/../.."

fail() { echo "FAIL: $*" >&2; exit 1; }
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

echo "== 3: forged jku, no request";   run ./internal/verify TestForgedJKUNoRequest
                                       run ./internal/wellknown TestJKUPinning TestSelfConsistentForgeryRejected
echo "== 4: token policy";             run ./internal/verify TestTokenPolicyTable TestKeeperDropsRevoked TestKeeperOutOfOrderFetch
echo "== 5: DPoP reject and replay";   run ./internal/verify TestOutboundInboundAndReplay
                                       run ./internal/a2a TestDPoPGuardRejectsUnauthenticated TestDPoPGuardAcceptsProvenCaller
echo "== 7: events";                   run ./internal/verify TestEveryCheckEmits TestMalformedHostsFailClosed TestRewriteOnlyMatchesOrigin
echo "== webmesh MCP client";          run ./internal/webmesh TestVerifyBuildsCallFromSchema TestDiscoverWithoutToolIsNamed

echo "== 6: register.sh is dry by default"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cat >"$tmp/ans-cli" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$@" >> "$ANS_CLI_LOG"
SH
chmod +x "$tmp/ans-cli"
export ANS_CLI_LOG="$tmp/calls"
out=$(PATH="$tmp:$PATH" scripts/register.sh gs-test.example --function 'get_pass_quote:Pass quote:uplink-uhf') || fail "dry run exited non-zero"
[[ ! -e $ANS_CLI_LOG ]] || fail "dry run invoked ans-cli"
grep -q 'DRY RUN' <<<"$out" && grep -q 'ans-cli register' <<<"$out" && grep -q 'ans-cli verify-dns' <<<"$out" || fail "plan not printed"
PATH="$tmp:$PATH" scripts/register.sh gs-test.example --i-am-a-human-and-this-is-permanent >/dev/null 2>&1 && fail "real run without --step was allowed"
[[ ! -e $ANS_CLI_LOG ]] || fail "refused run still invoked ans-cli"
PATH="$tmp:$PATH" ANS_BASE_URL=http://fake.invalid scripts/register.sh gs-test.example --name 'GS $(id) One' \
  --step register --i-am-a-human-and-this-is-permanent >/dev/null || fail "flagged step failed"
grep -qx 'GS $(id) One' "$ANS_CLI_LOG" || fail "argv not passed through intact"
echo "ok: dry by default, refuses without --step, argv intact with the flag"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj /CN=gs-test.example -days 1 \
  -keyout "$tmp/k.pem" -out "$tmp/c.pem" 2>/dev/null
recs=$(scripts/dns-records.sh gs-test.example 0.1.0 agent-1 "$tmp/c.pem")
want=$(openssl x509 -in "$tmp/c.pem" -outform DER | openssl dgst -sha256 -r | awk '{print $1}')
grep -q "TLSA  3 0 1 $want" <<<"$recs" && grep -q '_ans-badge.gs-test.example' <<<"$recs" && grep -q 'alpn=a2a' <<<"$recs" \
  || fail "dns-records.sh: $recs"
echo "ok: dns-records.sh prints _ans, _ans-badge, TLSA (cert hash) and SVCB"

echo "== 1: local ANS stack"
command -v nc >/dev/null || fail "nc is required"
for port in 8443 8444; do nc -z localhost "$port" 2>/dev/null && fail "port $port already in use"; done
make build >/dev/null
mkdir -p .run
scripts/local-ans.sh stop >/dev/null 2>&1 || true
scripts/local-ans.sh start >.run/phase-3-ans.log 2>&1 || { tail -20 .run/phase-3-ans.log >&2; fail "local stack did not start"; }
agents=()
cleanup() {
  for p in ${agents[@]+"${agents[@]}"}; do kill "$p" 2>/dev/null || true; done
  scripts/local-ans.sh stop >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT
rm -rf certs/local/gs-blacksburg
scripts/local-register.sh gs-blacksburg.localhost 0.1.0 certs/local/gs-blacksburg configs/local/station-ans.yaml >/dev/null \
  || fail "local registration"
bin/agent --config configs/local/station-ans.yaml >.run/phase-3-station.log 2>&1 & agents+=($!)
bin/agent --config configs/local/ops.yaml >.run/phase-3-ops.log 2>&1 & agents+=($!)
for _ in $(seq 1 50); do
  curl -ks --max-time 1 https://localhost:8444/health >/dev/null && curl -ks --max-time 1 https://localhost:8443/health >/dev/null && break
  sleep 0.2
done
ANS_LOCAL=1 run ./internal/verify TestLocalStack

echo "== 2: live webmesh"
if [[ ${ANS_LIVE:-} == 1 ]]; then
  ANS_LIVE=1 run ./internal/verify TestLiveWebmesh
  ANS_LIVE=1 run ./internal/webmesh TestLiveTools
else
  [[ -s internal/verify/testdata/webmesh-result.json ]] || fail "no recorded live fixture"
  echo "skipped (set ANS_LIVE=1); recorded fixture present: internal/verify/testdata/webmesh-result.json"
fi
echo "phase-3 acceptance: pass"
