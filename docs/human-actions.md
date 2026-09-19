# Needs a human

One running list of everything Claude Code must not or cannot do. It is updated every phase.
Tick an item (`[x]`) when it's done, and note when.

## Gates from the prompts doc (section 1)

| | Gate | What a human does | Needed before |
| --- | --- | --- | --- |
| [ ] | H1 | Buy the domain(s). Export `ANS_API_KEY` and `ANS_BASE_URL=https://api.godaddy.com` in the shell. | Live checks on our hosts, Phase 11 |
| [ ] | H2 | Register each agent: `scripts/register.sh <host> --step <step> --i-am-a-human-and-this-is-permanent`, one step at a time. Production registrations are permanent. | Live checks, Phase 11 |
| [ ] | H3 | Create the DNS records that `scripts/dns-records.sh` prints, with DNSSEC on for the zone. | Live checks |
| [ ] | H4 | Rent the VPS, add SSH access, run `deploy/install.sh`. | Phase 11 |
| [ ] | H5 | Provide the LLM API key as an environment variable. | Phase 10 |
| [ ] | H6 | Run `ans-cli revoke … --reason CERTIFICATE_HOLD` during the demo. | Demo |

## Before registering (H2)

- [ ] **Freeze each card first (master plan 5A).** Finish skill ids, tags, `payTo` and `securitySchemes`.
  Set `card.signed_file`, run `bin/agent --config <cfg> --write-card <path>`, then register with
  `metaDataHash = SHA256:<hex of that file>`. Any later card change means re-registering. (Phase 2, 3)
- [ ] **Confirm how production computes `metaDataHash`.** The RA spec says `SHA256:<hex>` over the
  metadata descriptor. Our cards are served as JCS bytes, so the raw and JCS hashes agree. (Phase 2)
- [ ] **Confirm the endpoint `transports` value for A2A on production.** ans-cli defaults to
  `STREAMABLE-HTTP`; the RA spec also allows `JSON_RPC`. (Phase 3)
- [ ] Note: `ans-cli generate-csr` defaults to an RSA server key. `register.sh` passes `--key-type ec`. (Phase 3)

## Configuration to supply

- [ ] **Production log root key** in the prod environment's `root_keys`:
  `transparency.ans.godaddy.com+c9e2f584+…` (full value at https://transparency.ans.godaddy.com/root-keys,
  pinned in `internal/verify/live_test.go`). Without it, stations and the authority reject every
  inbound call (fail closed). (Phase 2, 3)
- [ ] **Production ANS Finder URL.** It's unknown, so `FindByTag` is unavailable in prod until it's set
  as `environments.prod.finder_url`. (Phase 3)

## Approvals (frozen docs)

- [ ] **Add `CALLER_REJECTED` to the codes table in `docs/schemas.md`.** The code is live in
  `internal/errs` and the DPoP guard. (Phase 2)

## Decisions to confirm (Claude chose a default)

- [ ] **Authority card declares DPoP only, not "DPoP plus mandate."** It issues mandates; it doesn't
  consume them (rule 6). (Phase 2)
- [ ] **New JWS type `overpass-receipt+jws` for BookingReceipt**, following rule 3. (Phase 1)
- [ ] **Quote, Ack and CommandRecord are unsigned.** Risk: a station could forge Acks. Consider a
  spacecraft-signed Ack in Phase 5. (Phase 1)
- [ ] **x402 `accepts[].amount` is an integer**, following rule 3. Real x402 uses a string. (Phase 1)
- [ ] **Skill call shape `{"skill": id, ...args}` in the A2A data part.** supplier.webmesh.ai's exact
  shape wasn't captured. (Phase 2)

- [ ] **Satellite: CUTE-1 (CO-55), NORAD 27844**, from CelesTrak's cubesat group. It's
  sun-synchronous (98.7°), so Blacksburg, Svalbard and Awarua all get passes. Its TLE is cached in
  `internal/passes/testdata/27844.tle` (epoch 2026-09-19). Swap it via `satellite:` in config if you
  prefer another. (Phase 4)
- [ ] **Planner defaults to confirm:**
  - score = 100·max_elevation + 100·priority_bonus − points_per_dollar·cents
  - grazing passes under 60 s are dropped
  - uplink needs FIDUCIARY; downlink needs TRANSACTIONAL or better (master plan 11)

  (Phase 4)

## Before the demo

- [ ] **Refresh the TLE** a day or two before judging: `bin/passes -refresh-tle`. It reads CelesTrak
  and atomically replaces `data/27844.tle`, the runtime cache. The test fixture in
  `internal/passes/testdata` is left alone. Predictions more than 30 days from the TLE epoch
  (2026-09-19) are refused. (Phase 4)

## Worth knowing (no action unless you disagree)

- agent.webmesh.ai currently fails our verification: its WARNING status, a served cert that isn't the
  attested one, and no TLSA record. See `internal/verify/testdata/webmesh-result.json`. It's good demo
  contrast. (Phase 3)
- The webmesh MCP server has `verify_agent` but no discover tool. (Phase 3)
