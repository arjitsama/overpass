# Go-live status: ops, gs-blacksburg, authority on production ANS

Running log for the 2026-09-20 go-live (user runbook, 8 gated steps). Every
production write is printed first and run only after an explicit "yes".
Credentials never appear here.

## Step 1 — .env check + read-only resolve (2026-09-20)

- 02:18 `.env` structure check: ANS_API_KEY line PASS, exactly one colon PASS,
  both halves non-empty PASS, no quotes/spaces PASS, ANS_BASE_URL exact PASS.
- `ans-cli resolve agent.webmesh.ai` against https://api.godaddy.com: success
  (ans://v1.0.14.agent.webmesh.ai). Credential works; no writes.

## Step 2 — ordering decision (2026-09-20)

- The RA never fetches the metadata URL (no outbound card fetch in
  `ans/internal/ra`; `metaDataHash` is registrant-supplied,
  `ans/internal/domain/endpoint.go:104-117`). So there is no TLS-validation
  question and no cert swap. ans-cli v0.1.18 sends no hash at all.
- Path: plain CSR path via ans-cli (generate-csr -> register -> DNS-01 TXT ->
  verify-acme -> get-*-certs -> DNS records -> verify-dns). TLSA is computed by
  the RA from the ANS-issued server cert; we serve that same cert.
- Decisions (user): verifier treats "no hash registered" as Warn only when the
  receipt verified and card_signature passed (else Fail); station payTo is the
  zero address with a "simulated" note. Cards are frozen after the Agent ID is
  known.

## Step 0/3 — prep and provisional card freeze (2026-09-20 02:22)

- Code: `internal/verify` card_hash Warn path (receipt verified + card_signature
  passed + no hash registered), three unit tests in `cardhash_test.go`; warns
  printed by name in `--verify` and the dashboard log; README Honest limits.
  `pricing.note` -> card `x-payment.note`; gs-blacksburg payTo = null address.
  `SMOKE_HOSTS` override in smoke.sh; `AUTHORITY_CONFIG` in install.sh;
  `session.spacecraft_ca` on the station. Tests: config, wellknown, station,
  verify, cmd/agent, web all ok.
- `ans-cli generate-csr` (default key types: EC P-256 identity, RSA-2048
  server; the registry refuses an EC server key, so register.sh's `--key-type ec`
  is stale) for ops, gs-blacksburg, authority -> `certs/<name>/` (git-ignored).
- `certs/authority.pub`, `certs/ops/command.pub` (satreg pubkey),
  `certs/registry.jws` signed by the authority identity key from
  `deploy/prod/registry.json`; `certs/spacecraft/server.{key,pem}` self-signed.
- Provisional cards (temporary self-signed identity chains; no agentId yet):
  ops 120a91ed…f7b6, gs-blacksburg 89c92b8d…ee1f, authority ab16e459…084b.
  securitySchemes: ops noAuth; authority ansDPoP; gs-blacksburg ansDPoP +
  overpassMandate on book_pass. Matches cmd/agent/roles.go. Final freeze after
  each Agent ID is known.

## Step 4 — VPS prep, dry-run (2026-09-20 02:26)

- VPS 45.76.253.108: fresh Ubuntu, root ssh, envsubst present, no nginx yet.
- Copied `deploy/` to `/tmp/overpass/deploy`; stripped Linux amd64 binaries
  (agent, cardhash, satreg) uploading as `/tmp/overpass/overpass-linux.tgz`
  (link is ~20 KB/s from the Mac, hence the compressed archive).
- `AUTHORITY_CONFIG=authority-tonight bash deploy/install.sh --dry-run` on the
  box: plan printed (user, dirs, binary, 10 rendered configs with
  authority-tonight as /etc/overpass/authority.yaml, unit, nginx stream map,
  enable-only). Nothing changed. Waiting for "apply".
- 02:30 APPLIED. nginx + stream module installed; ufw active with 22/tcp
  and 443/tcp; `include /etc/nginx/streams-enabled/*.conf;` added at top level
  of nginx.conf (idempotent); default site listens on 80 only; nginx -t ok,
  :443 SNI passthrough live. First apply failed: envsubst blanked nginx's own
  `$ssl_preread_server_name`; fixed install.sh to expand only agents.env
  variables. Enabled: ops, authority, gs-blacksburg, spacecraft, gs-sva1bard-eu;
  disabled: gs-awarua, gs-svalbard-eu, gs-rogue, gs-spare, auditor. No service
  started. authority.yaml rendered from authority-tonight.yaml.

## Step 5 — ops registered (2026-09-20 02:34)

- 02:33:58 `ans-cli register` ops (A2A, JSON-RPC, description set) -> 202
  PENDING_VALIDATION. Agent ID `40ec16cb-d52d-476d-8bd5-1ce1a709a27b`,
  ans://v0.1.0.ops.blacksburgbytes.club. Saved as OPS_AGENT_ID in deploy/agents.env.
- DNS-01 challenge: `_acme-challenge.ops` TXT (value in the ans-cli output;
  expires 2026-09-21T06:34Z). Waiting for the Porkbun record.
- Ops card re-frozen with the Agent ID (final bytes for the VPS).

## Step 6/7 — ops verify-acme; gs-blacksburg + authority registered (2026-09-20 02:45)

- 02:43:59 `verify-acme` ops -> PENDING_DNS (DOMAIN_VALIDATION complete). Identity
  and server certs issued and fetched into certs/ops/.
- 02:44:24 `register` gs-blacksburg -> PENDING_VALIDATION, Agent ID
  `e9b5abcd-1e74-4ff3-86cf-cecfcc3161e2`.
- 02:44:27 `register` authority -> PENDING_VALIDATION, Agent ID
  `9cb537b3-5820-4741-90e1-8dbcdce67ab4`.
- Both IDs saved in deploy/agents.env; both cards re-frozen with their IDs.
- Waiting: _acme-challenge TXT for gs-blacksburg and authority; _ans, _ans-badge,
  TLSA, HTTPS records for ops.

## DNS via Porkbun API (2026-09-20 02:56)

- Plan: docs/plans/go-live-dns.md. New `scripts/porkbun-dns.sh` (bash +
  python3 stdlib; retrieve-then-create, skip identical, never delete, keys
  redacted; `--dry-run`). Porkbun API v3 spec confirms TXT, TLSA, HTTPS, SVCB
  in the type enum; name = label only; errors carry `code`
  (DOMAIN_NOT_ALLOWED = API toggle). Review caught a stdin bug (python heredoc
  ate the rows; fixed by reading rows in bash first). secret-scan clean.
- 02:51:11 created: _acme-challenge.gs-blacksburg TXT, _acme-challenge.authority
  TXT, _ans.ops TXT, _ans-badge.ops TXT, _443._tcp.ops TLSA, ops HTTPS, and
  ops SVCB (accepted). Re-run: 6 skipped (idempotent).
- `scripts/dns-records.sh` now emits `version=v<ver>` and the HTTPS record.
- Trust note: the ops server cert chains to "GoDaddy TLS Root CA - R1", which
  is NOT in this Mac's system root store (the older G2 root is). Cross-sign
  check pending; browser trust of the dashboard to be confirmed at step 8 with
  `curl -sv` from the Mac.
- dig PASS at 1.1.1.1 and 8.8.8.8 for all seven ops/ACME records (TLSA, HTTPS
  and SVCB shown in generic form by the Mac's dig 9.10; AD flag set = DNSSEC
  validated).
- 02:57:50 `verify-dns` ops -> **ACTIVE** (DOMAIN_VALIDATION, CERTIFICATE_ISSUANCE,
  DNS_PROVISIONING complete).
- 02:57:53 `verify-acme` gs-blacksburg -> PENDING_DNS.
- 02:57:54 `verify-acme` authority -> PENDING_DNS.
- 03:01 gs-blacksburg + authority: identity certs (GoDaddy Private ANS
  Issuing CA) and server certs (GoDaddy TLS Intermediate CA DV - R1v1, public)
  fetched; keys match; server SHA-256 equals the RA's TLSA for both
  (8b6f60c4…544e, a070f1ed…6218). DV intermediate appended to all three
  server.pem (leaf + intermediate). Cards re-frozen with the real identity
  chains: bytes unchanged (ops 72f3e657…, gs-blacksburg d75895d7…, authority
  898ffc5d…). First attempt of this step failed on bash 3.2 (`declare -A`) and
  a heredoc quoting error; redone from a script file.
- 02:59:15 Porkbun API: created _ans, _ans-badge, TLSA, HTTPS, SVCB for
  gs-blacksburg and authority (10 records); dig PASS at 1.1.1.1 and 8.8.8.8 for
  all ten.
- 03:02:15 `verify-dns` gs-blacksburg -> **ACTIVE**. 03:02:16 `verify-dns`
  authority -> **ACTIVE**.

## Registry state (2026-09-20 03:02) — all three ACTIVE

| host | Agent ID | status | server cert (TLSA) |
|---|---|---|---|
| ops | 40ec16cb-d52d-476d-8bd5-1ce1a709a27b | ACTIVE | 26311c83… |
| gs-blacksburg | e9b5abcd-1e74-4ff3-86cf-cecfcc3161e2 | ACTIVE | 8b6f60c4… |
| authority | 9cb537b3-5820-4741-90e1-8dbcdce67ab4 | ACTIVE | a070f1ed… |

Public badges on transparency.ans.godaddy.com: ACTIVE, AGENT_REGISTERED, server
cert fingerprints match the served certs. No service started yet.

## Needs a human
- [ ] "go" to start services (step 8): copy certs to the VPS, re-render configs
  with the Agent IDs, start ops/authority/gs-blacksburg/spacecraft, verify from
  the Mac with `curl -sv https://ops.blacksburgbytes.club/health`.
- [ ] Browser trust: the DV chain roots at "GoDaddy TLS Root CA - R1", absent
  from this Mac's system store; confirm with curl at step 8, else note it.
- [ ] Optional `ANTHROPIC_API_KEY` drop-in for overpass@ops (H5).

## Step 8 — services started (2026-09-20 03:17)

- a1d0511 pushed to origin/main after confirming no certs/, .env, *.key, *.pem
  or deploy/agents.env are tracked; secret-scan clean.
- 07:11:41Z started overpass@ops, authority, gs-blacksburg, spacecraft,
  gs-sva1bard-eu: all active, listening on their ports; overpass user reads
  certs (root:overpass, 750/640). Unregistered agents (spacecraft, impostor)
  write their card to /var/lib/overpass (their signed_file was under read-only
  /etc). Registered agents reuse the frozen cards.
- Finding: ops/authority/gs-blacksburg stopped resolving from outside once
  HTTPS/SVCB records existed at those names (the wildcard `*` A no longer
  matches an existing name, RFC 4592). Fix: explicit A records via
  scripts/porkbun-dns.sh. spacecraft and the impostor (wildcard only) were fine.
- Trust finding: the served ops chain (leaf + GoDaddy DV intermediate,
  fingerprint 26311c83… = ANS-issued) is rejected by stock macOS curl with
  system trust ("unable to get local issuer certificate"): "GoDaddy TLS Root
  CA - R1" is not in the macOS root store. Agent-to-agent verification pins the
  log-attested fingerprint and is unaffected. Dashboard plan pending user.
- 03:29 Explicit A records for ops, gs-blacksburg, authority live at
  1.1.1.1/8.8.8.8; the Mac's upstream resolver (router) holds the negative
  answer for up to 1800 s (zone SOA minimum), so the smoke gate waits for it.
- Smoke fixes: `bin/cardhash -ca <bundle>` (root the OS lacks) and
  `SMOKE_CA`; TLSA parse joins dig's wrapped hex fields (was comparing only
  the last fragment -> false `tlsa!=cert`). Served ops card == frozen
  (72f3e657…); `bin/agent --verify ops` passed in the first smoke run.
- 03:48 Smoke gate: ops PASS (health, cards, cardhash=72f3e657, TXT TXT
  TLSA SVCB, tlsa=cert, verify). authority + gs-blacksburg answer HTTP 200 with
  DNS bypassed (`--resolve`) but the Mac's mDNSResponder still caches the
  negative A answer from before the explicit A records; needs a local flush
  (`sudo dscacheutil -flushcache; sudo killall -HUP mDNSResponder`).
- Browser trust: stock macOS curl (SecureTransport, system store) rejects the
  served chain (leaf + DV intermediate): "GoDaddy TLS Root CA - R1" (issued
  Aug 2025) is not in the store. GoDaddy publishes a cross-certificate
  `gd_tls_root-r1-cross-g2.crt` (R1 signed by the trusted G2 root, valid to
  2037); `security verify-cert` with it in the chain: verified. Options
  presented to the user; agents' certs untouched.
- 04:09 Smoke gate PASS for ops, authority, gs-blacksburg (health,
  agent-card, trust-card, cardhash == frozen, _ans/_ans-badge TXT, TLSA, SVCB,
  tlsa=cert, bin/agent --verify) once the Mac's resolver cache expired.
- 04:09:49 `bin/opsflow … -mode uplink` from the Mac: first failing check
  `get_pass_quote`: the A2A POST client trusts the OS store, which lacks
  "GoDaddy TLS Root CA - R1" (verify itself pins the attested fingerprint and
  passed). Fix attempt 1: `-ca certs/ca/godaddy-r1-bundle.pem` (the genuine
  GoDaddy R1 root + DV intermediate, not -insecure).
- 04:10:08 retry: verify, quote, mandate and booking succeeded; stopped at
  `relay_command: WINDOW_CLOSED` (the booked pass window has not opened).
  Stopped after one fix attempt, per the user's rule.
- The VPS's own trust store (Ubuntu ca-certificates) also lacks the R1 root:
  curl from the box to gs-blacksburg by name fails the same way, so the ops
  agent's planner on the box will hit it too until the root is installed
  system-wide there (does not touch the agents' certs).

## Chain fix + VPS root (2026-09-20 04:13)

- Option 1 applied: each server.pem now serves leaf + GoDaddy DV intermediate +
  `gd_tls_root-r1-cross-g2` (R1 cross-signed by the stock-trusted G2 root).
  Leaf certs unchanged (fingerprints = TLSA). Three agents restarted.
- VPS stock curl (no -k) to all three hosts: HTTP 200 before installing any
  root, so the chain alone fixes OS-store clients. Then "GoDaddy TLS Root CA -
  R1" installed system-wide on the box (update-ca-certificates), ops restarted.
- Mac stock curl (system trust, no -k/-ca): HTTP 200 for ops, authority,
  gs-blacksburg; served chain = 3 certs; leaf == TLSA and served card == frozen
  for all three.
- `bin/opsflow -demo` added (lead 15 s: quotes need AOS in the future, relay
  accepts 30 s slack). First -demo run with lead 0 was refused by the station
  (QUOTE_REJECTED:window, AOS not in the future) -> 15 s.
- 04:13:47 LIVE UPLINK PASS, no -ca: station ans://v0.1.0.gs-blacksburg…,
  quote q-dfb4ab41…, mandate m-ece7b51a…, booking b-505eaf2f…, ack accepted
  (counter 1789892031006, telemetry sha256 c1a42a3d…).
- 04:14 `bin/agent --verify` (Mac): ops, authority, gs-blacksburg
  VERIFIED (warn; DANE Verified) with the named warn "card_hash: card hash: not
  registered; card bound by signature to log-attested key"; gs-sva1bard-eu
  FAILED "registered: no _ans-badge TXT record: not an ANS agent".
- GoDaddy webmesh verify_agent via ops /ui/verify-station: gs-blacksburg
  ans_verified=true, ans_registered=true, tl=verified, dnssec=verified,
  can_traveler_transact=yes (identity/protocol/auth pass; attestations
  "unable-to-check", their Phase 2). gs-sva1bard-eu: ans_registered=false,
  identity=fail, can_traveler_transact=unknown, card unreachable (self-signed).
