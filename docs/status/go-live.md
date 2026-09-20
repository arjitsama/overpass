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
