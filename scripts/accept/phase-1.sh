#!/usr/bin/env bash
# Phase 1 acceptance: wire formats and crypto core. Exits non-zero on failure.
#   1. JCS output matches RFC 8785 sample vectors.
#   2. Sign/verify round-trips; one-byte changes fail with a named code; the
#      verifiers are fuzzed for 30 s each without panics or unnamed errors.
#   3. A mandate JWS presented as a command fails with TYP_REJECTED.
#   4. 100.0 is rejected at decode where 100 is accepted.
#   5. Equal command sequences give equal chain heads; drop/reorder changes it.
#   6. Thumbprint matches the RFC 7638 vector (and ans-sdk-go's JKT).
set -euo pipefail
cd "$(dirname "$0")/../.."

FUZZTIME=${FUZZTIME:-30s}

step() { echo "== $1"; }
run() { go test -count=1 "$@" >/dev/null || { go test -count=1 -v "$@" | tail -40 >&2; echo "FAIL: $*" >&2; exit 1; }; echo "ok"; }

step "1: RFC 8785 vectors";           run ./internal/jose -run '^TestJCSVectors$'
step "2: round trip and tamper";      run ./internal/schema -run '^(TestSignVerifyRoundTrip|TestTamperEveryByte)$'
                                      run ./internal/jose -run '^TestEveryByteFlipFails$'
step "2: fuzz mandate ($FUZZTIME)";   run ./internal/schema -run '^$' -fuzz '^FuzzVerifyMandate$' -fuzztime "$FUZZTIME"
step "2: fuzz command ($FUZZTIME)";   run ./internal/schema -run '^$' -fuzz '^FuzzVerifyCommand$' -fuzztime "$FUZZTIME"
step "3: typ confusion";              run ./internal/schema -run '^TestTypConfusion$'
                                      run ./internal/jose -run '^TestWrongTypRejectedFirst$'
step "4: floats rejected";            run ./internal/schema -run '^TestFloatRejectedAtDecode$'
                                      run ./internal/jose -run '^TestStrictJSONIntegersOnly$'
step "5: chain heads";                run ./internal/chain -run '^TestChainDeterministic$'
step "6: RFC 7638 thumbprint";        run ./internal/jose -run '^(TestThumbprintRFC7638|TestThumbprintMatchesSDK)$'
step "docs/schemas.md examples";      run ./internal/schema -run '^TestSchemasDocExamples$'
echo "phase-1 acceptance: pass"
