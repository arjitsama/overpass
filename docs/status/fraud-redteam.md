# GoDaddy fraud battery (fraud.webmesh.ai) vs gs-blacksburg — red-team log

Status 2026-09-20 05:00: **in progress, no pass claimed.** `run_battery` has
not been run against us yet. Wording rule: "passed GoDaddy's battery" is
written nowhere because their `run_battery` has not returned CLEAN against us.

Target: `target_ans=ans://v0.1.0.gs-blacksburg.blacksburgbytes.club`,
`target_url=https://gs-blacksburg.blacksburgbytes.club/mcp/`. Opt-in TXT
`_fraud-allow.gs-blacksburg.blacksburgbytes.club "v1"` live at 1.1.1.1/8.8.8.8.
Our surface: `internal/supplieradapter` (commit 577d47e). Station log lines are
`msg=mcp_request` / `msg=mcp_result` in `journalctl -u overpass@gs-blacksburg`.

| # | their check | run by | their verdict | code we returned | right reason? | evidence |
|---|---|---|---|---|---|---|
| 1 | payto_binding_check | their agent, 04:56:45 then 05:08:44 | first WARNING "payTo present but absent from signed card extension" (`attested_in_signed_card: null`); after the fix **PASS** "payTo matches attested identity in signed card", `attested_in_signed_card: 0x0000…0000`, `card_signed: true` | get_quote x402 challenge, payTo `0x0000…0000` | yes (structural probe: payTo in the challenge equals the payTo attested in the signed card extension) | station log 08:56:46Z / 09:08:4xZ `python-httpx/0.28.1` tools/call get_quote {MAD,SIN,2026-10-14}. Fix from evidence: `null` meant "no extension carried payTo at params level"; the signed `supplier-mcp/v1` extension now declares `payTo`, `network`, `asset` (USDC contract) and `scheme` flat in `params` as well as under `params.x402`. Card re-frozen: b97de46a…, served == frozen. Raw: fraud-probes/payto_binding_check-31.json. |
| 2 | card_drift_watch (run 1) | their agent, 04:56:46 | INCONCLUSIVE "target_ans not in FRAUD_TARGET_ALLOWLIST" | — (not called) | n/a | this tool checks only their env allowlist, not the `_fraud-allow` TXT; needs Scott to add us. |
| 3 | card_drift_watch (run 2) | their agent, 04:56:47 | INCONCLUSIVE (same) | — | n/a | same. |
| 4 | unknown_key_mandate | their agent, 04:56:47 and 04:57:57 | `{"error":"RATE_LIMITED","code":"RATE_LIMITED","battery_verdict":"INELIGIBLE"}` | — (nothing reached us) | n/a | their per-target rate limit; retry later. |
| 5 | superseded_format_attack | their agent, 04:58:22 | RATE_LIMITED / INELIGIBLE | — | n/a | as above. |
| 6 | corrupt_jws_attack | their agent, 04:58:48 | RATE_LIMITED / INELIGIBLE | — | n/a | as above. |
| 7-16 | underpay_booking, tamper_mandate, underpay_valid_sig, quote_swap_attack, wrong_audience_attack, wrong_scope_attack, wrong_dpop_key_attack, replay_booking, replay_settled, canonicalization_probe | not yet | — | — | — | blocked by their rate limit and, for the mint-dependent ones, by their fixture mint (`request_mandate failed: REQUEST_NOT_SIGNED`, see docs/webmesh-interop.md). |

Raw responses: `docs/status/fraud-probes/*.json`.

## What our surface enforces today (unit-tested, `internal/supplieradapter`)
MANDATE_PARSE_ERROR (missing scope/jkt/audience/quote_id/max_amount/signature);
MANDATE_REJECTED:unknown_key (kid not in authority.webmesh.ai's published keys,
pinned by host; keys carried in the request are never used);
MANDATE_REJECTED:signature (tampered amount, flipped signature bytes);
:audience; :quote (swap, unknown, option not on quote); :scope; :amount
(numeric, after JCS canonicalization, 620 == 620.0); :expired; DPOP_REJECTED:key
(thumbprint != mandate jkt, bad proof); DPOP_REJECTED:replay (jti seen);
MANDATE_REJECTED:consumed (mandate reused with a fresh proof); PAYMENT_REQUIRED
for a fully valid booking (no EIP-3009 settlement, no ticket: Honest limits).

## Evidence gaps (not guessed)
- The AP2 mandate wire format as their agent sends it: not yet observed (no
  attack has reached book_flight). The parser accepts a JSON object with a
  detached/compact JWS `signature` over its JCS form, or a compact JWS whose
  payload is the mandate; the first captured request decides which is real.
- The extension shape payto_binding_check expects for an "attested" payTo.
- Their paid get_quote response shape (x402 fee not paid).

## Step log (mission 3, 2026-09-20)

- 05:12 **Step 1** apex opt-in: `_fraud-allow.blacksburgbytes.club TXT "v1"`
  created (per-host already existed); both PASS at 1.1.1.1 and 8.8.8.8.
- 05:13 **Step 2** supplier rejection envelope captured (two calls, invalid
  input): HTTP 200, `{"result":{"content":[{"type":"text","text":"Error executing
  tool book_flight: MANDATE_REJECTED: authority_ans None host does not match
  pinned authority 'ans://v1.0.2.authority.webmesh.ai'"}],"isError":true}}`; no
  structuredContent. Both a stripped and a complete-but-bogus mandate hit the
  same first check: the mandate's `authority_ans` vs a pinned authority (their
  pin still says v1.0.2 while the authority is v1.0.4). Our book_flight now
  returns that envelope byte-for-byte (family code + ": " + sub-reason +
  detail) and checks `authority_ans` against the pinned authority first.
- 05:16 **Step 3b** get_policy: `{"max_per_trip": 1000.0, "currency": "USD",
  "allowed_merchants": ["*"], "categories": ["travel"]}` -> our station is an
  allowed merchant.
- 05:18 **Step 3a** ops serves `/.well-known/jwks.json` (OKP Ed25519, kid
  aeVO9iXJuXK_Js9DfLNNOHdjU71PbbtJd9E5sSKp52A, alg EdDSA, use sig) via a new
  `well_known_files` config; verified with stock curl; ops and station cards
  unchanged (72f3e657…, b97de46a…). Smoke PASS.
