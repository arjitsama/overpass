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
- [ ] **Add the Phase 4–5 codes to that table.** All are live in `internal/errs`:
  - `POLICY_REFUSED:{caller,unverified,station,classes,amount,daily_limit,window}`
  - `QUOTE_REJECTED:{window,norad_id}`
  - `PLAN_SKIPPED:*`

  (Phase 4, 5)

## Decisions to confirm (Claude chose a default)

- [ ] **Authority card declares DPoP only, not "DPoP plus mandate."** It issues mandates; it doesn't
  consume them (rule 6). (Phase 2)
- [ ] **New JWS type `overpass-receipt+jws` for BookingReceipt**, following rule 3. (Phase 1)
- [ ] **Quote, Ack and CommandRecord are unsigned.** Risk: a station could forge Acks. Consider a
  spacecraft-signed Ack in Phase 5. (Phase 1)
- [ ] **x402 `accepts[].amount` is an integer**, following rule 3. Real x402 uses a string. (Phase 1)
- [ ] **book_pass check 10 (DPoP jti unseen) runs in the transport**, before the mandate is read. The SDK
  doesn't expose the jti to handlers. The code is still `DPOP_REJECTED:replay`. (Phase 5)
- [ ] **A mandate's own booking doesn't count as an overlap**, so replaying a spent mandate gets
  `MANDATE_REJECTED:consumed` (attack 12), not overlap. (Phase 5)
- [ ] **Token checks:** the station re-checks the Ops token on a command at most once every 30 s,
  besides the 30 s background loop. (Phase 6)
- [ ] **Evidence access:** `session_evidence` is readable only by the booking's Ops agent and by ANS
  names listed in `session.auditors`. Add the auditor's ANS name there in Phase 7. (Phase 6)
- [ ] **Acks are unsigned.** A station could drop a command and forge an Ack; the chain heads would
  still match. A spacecraft-signed Ack would close this. Decide whether it matters for the demo.
  (Phase 1, 6)
- [ ] **Local databases are versioned.** After an upgrade that changes the schema, an agent refuses an
  old `data/*.db` with a clear message; delete the file. (Phase 6)
- [ ] **Canary probing runs from `cmd/battery`, not the auditor agent.** The auditor's `audit_pass`
  is passive; canary needs booking credentials, which the battery holds. Decide if the auditor agent
  should own canary for the demo. (Phase 7)
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

## Set up per deployment (Phase 5)

- [ ] **Station pricing:** choose the real `pricing.pay_to` wallet, `network` and `asset` for each
  station. It goes into the signed card's `x-payment`, so set it before freezing the card.
- [ ] **Satellite registry:**
  1. On the authority's host: `bin/satreg sign -key <authority identity key> -in registry.json -out registry.jws`
     and `bin/satreg pubkey -key … -out signer.pub.pem`.
  2. Give every station `satreg.file` and `satreg.signer_key_file`.
  3. List the real Ops ANS name(s) under each NORAD ID.
- [ ] **Authority flight rules:** confirm the values for `flight_rules` (stations or `min_tier`, command
  classes per mode, `max_cents_per_pass`, `max_passes_per_day`), `ops_agents`, and `trust_tiers`.
  `trust_tiers` is a stub until Phase 8.
- [ ] **Authority keys on stations** are pinned statically (`authority_keys` PEM). Fetching them from
  the authority's verified trust card is not wired yet; decide if the demo needs it.
- [ ] **Flight rule `max_passes_per_day` counts mandates issued**, not passes booked. An unused mandate
  still counts. Confirm or ask for release-on-expiry.
- [ ] **Rogue station (demo):** `rogue: true` needs `rogue_ack: i-am-the-rogue-station`. Only on `gs-rogue`.

## Set up per deployment (Phase 6)

- [ ] **Spacecraft agent:** `role: spacecraft` with `spacecraft.norad_id` and `spacecraft.ops_key_file`.
  The key file is the Ops identity key's public half, from `bin/satreg pubkey -key <ops identity key>`.
- [ ] **Each station:**
  - `session.spacecraft_url` is the spacecraft agent. Set `session.spacecraft_ca` if it's self-signed.
  - `session.ops_env` is the environment whose log holds the Ops agent's status token.
  - List `relay_command` and `session_evidence` in the card's skills, and freeze the card after that.
- [ ] **Revocation demo (gate H6, master plan 9.12):**
  - Run `ans-cli revoke <station agentId> --reason CERTIFICATE_HOLD` mid-pass. The next 30 s token
    fetch cuts the session (`SESSION_CUT:revoked`), and Ops replans.
  - Production revocations are permanent. Rehearse on the local stack first, and decide whether
    `REMOVE_FROM_CRL` restores ACTIVE there.

## Set up per deployment (Phase 7)

- [ ] **Adversary profiles** in `deploy/local/`: honest station, `gs-rogue` (rogue+ack), the
  unregistered impostor `gs-sva1bard`, and the registered lookalike. Register the honest/rogue/
  lookalike on the reference stack; leave the impostor unregistered.
- [ ] **Auditor** (`role: auditor`): configure `auditor.station_keys` (each station's identity public
  key, to verify booking receipts) and `authority_keys`. It mounts `audit_pass`. Add its ANS name to
  each station's `session.auditors` so it can read evidence.
- [ ] **Battery credentials** (`deploy/local/battery-*.yaml`): the battery holds two registered Ops
  identities and the authority signing keys the target station trusts. This is deliberate for a
  red-team tool. Fill in the `<...>` placeholders (agent ids, root keys) from the local stack.
- [ ] **Deploy gate:** `bin/battery run -config <cfg>` exits non-zero unless every check is BLOCKED.
  Wire it into the deploy so a vulnerable station cannot ship. Use `-expect-vulnerable` only for the
  rogue demo.

## Phase 8 (trust index and tiers)

- [ ] **Run the trust index.** `make trust-up` runs the fork on `:8080` with admin auth OFF (local
  demo only; it logs a loud warning). For anything shared, run it with the shipped
  `config/runtime.yaml` and a real bearer key, and set `trust_index.admin_key_env` on the authority
  and auditor configs to the env var holding that key. Never put the key in a config file (hard rule 2).
- [ ] **Register + seed.** `bin/trustseed -config deploy/local/trustseed.yaml` imports the seven agents
  and gives the honest stations a baseline `pass_delivery` marked as seed data. It never seeds identity
  or integrity. The lookalike is imported but not seeded (that is why it stays downlink-probation).
- [ ] **Real integrity/identity (the one thing the local stack cannot fake).** The upstream engine
  derives identity from `certtype` alone (DV=40) and integrity from DNSSEC/cert/version signals. On the
  local stack these have no source, so a seeded honest station reads Overpass READ_ONLY. To demo
  downlink/uplink for real, run the prober/hydrator (or `QUERY=webmesh make demo-live` in the fork)
  against real infrastructure so integrity/identity are measured. See `docs/status/phase-8.md` for the
  file:line references on why DV cannot reach the index's FIDUCIARY.
- [ ] **Set `flight_rules.min_cert_type`** to `OV` or `EV` for production uplink (default `DV` for the
  demo). Identity is displayed as measured; this is the only cert gate.
- [ ] **`make demo-live` against production** is out of scope for Phase 8; a human runs it separately.

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
