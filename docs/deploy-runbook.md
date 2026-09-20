# Deploy runbook — the human-gated work

Everything in Phases 0–11 is built and tested. What remains is human by design:
buying/registering identity, DNS, a VPS, and the live revoke beat. Do them in
order **H1 → H2 → H3 → H4**, rehearse, then **H6** live during the demo.

**Hard rule 1:** never run `ans-cli register` / `revoke` or any other write
against `https://api.godaddy.com` except intentionally at H2 and H6. Reads are
fine. Registrations are PERMANENT (append-only transparency log).

Hosts (all `.blacksburgbytes.club`): `ops`, `authority`, `gs-blacksburg`,
`gs-awarua`, `gs-svalbard-eu`, `gs-rogue`, `auditor`, `spacecraft`.
`BASE_DOMAIN` is set once in `deploy/agents.env`.

---

## H1 — Domain & DNSSEC (Porkbun)
1. Confirm **blacksburgbytes.club** is active in your Porkbun account.
2. Porkbun → Domain Management → **DNSSEC**: enable it. ANS verifiers reject the
   TLSA record without a DNSSEC-signed zone.
3. Confirm Porkbun lets you add **TXT**, **TLSA**, and **SVCB/HTTPS** records
   (custom record types). If it cannot serve TLSA/SVCB or sign the zone, the
   documented fallback is badge + SCITT receipt + cert-fingerprint only — record
   that outcome truthfully in the UI, don't hide it.
4. In your shell for the registration steps:
   ```sh
   export ANS_API_KEY=…                     # GoDaddy ANS key (H-gate; from the workshop)
   export ANS_BASE_URL=https://api.godaddy.com
   ```
   (ANS registry is at GoDaddy; DNS is at Porkbun — independent systems.)

## H2 — Register each agent on ANS (PERMANENT)
Do one host, one step at a time. `scripts/register.sh` is **dry-run by default**;
real writes need `--i-am-a-human-and-this-is-permanent`.
1. Freeze the card, then write it to compute its hash:
   ```sh
   bin/agent --config deploy/prod/<name>.yaml --write-card certs/<name>/agent-card.json
   ```
2. Plan (prints the exact `ans-cli` commands, changes nothing):
   ```sh
   scripts/register.sh <name>.blacksburgbytes.club
   ```
3. Keys/CSR for real:
   ```sh
   scripts/register.sh <name>.blacksburgbytes.club --step csr --i-am-a-human-and-this-is-permanent
   ```
4. Register — copy the printed **Agent ID** into `deploy/agents.env` as
   `<NAME>_AGENT_ID` (e.g. `GS_BLACKSBURG_AGENT_ID`):
   ```sh
   scripts/register.sh <name>.blacksburgbytes.club --step register --i-am-a-human-and-this-is-permanent
   ```
5. Publish the ACME challenge it prints, then `--step verify-acme`.
6. `--step status`, `--step certs` (fetches identity + server certs into
   `certs/<name>/`), and after H3, `--step verify-dns`.
7. Repeat for all eight hosts. Record every Agent ID and the log URL in
   `deploy/agents.env`.

## H3 — DNS records (Porkbun UI)
For each registered host:
1. Print the exact records:
   ```sh
   scripts/dns-records.sh <name>.blacksburgbytes.club 0.1.0 <agentId> \
       certs/<name>/server.pem <log-url>
   ```
2. In Porkbun DNS, create each: `_ans` TXT, `_ans-badge` TXT, `_443._tcp` TLSA
   (`3 0 1 <sha256 of the served cert>`), and the SVCB record for the host.
3. Verify:
   ```sh
   dig +dnssec _ans.<name>.blacksburgbytes.club TXT
   dig TLSA _443._tcp.<name>.blacksburgbytes.club
   ```
   The TLSA hash must equal the SHA-256 of the cert served on :443 (the
   production smoke checks this for you once the agents are up).

## H4 — VPS bring-up
1. Rent a VPS with a public IPv4. In Porkbun DNS, add an **A record** for every
   `<name>.blacksburgbytes.club` pointing at the VPS IP. Open TCP **443**.
2. Copy the artifacts to the box (certs never live in the repo):
   ```sh
   make build
   scp bin/agent root@VPS:/tmp/agent
   scp -r deploy root@VPS:/tmp/deploy
   scp -r certs  root@VPS:/tmp/certs      # per-host keys/certs from H2
   ```
3. On the VPS, install gettext (`envsubst`) and nginx with the stream module,
   then place configs and secrets:
   ```sh
   sudo apt-get install -y gettext-base nginx libnginx-mod-stream
   sudo mkdir -p /etc/overpass/certs
   sudo cp -r /tmp/certs/* /etc/overpass/certs/
   sudo cp /tmp/deploy/agents.env.example /etc/overpass/agents.env
   sudo "$EDITOR" /etc/overpass/agents.env   # fill Agent IDs, log URLs, BASE_DOMAIN
   ```
   Ensure `nginx.conf` has `include /etc/nginx/streams-enabled/*.conf;` at the top
   level (outside the `http {}` block).
4. Dry-run, review, then apply (idempotent):
   ```sh
   cd /tmp/deploy/..; sudo deploy/install.sh --dry-run
   sudo deploy/install.sh --apply
   ```
5. Put secrets in per-agent systemd drop-ins (not in the repo, not in agents.env
   in the repo):
   ```sh
   sudo systemctl edit overpass@authority   # [Service]\nEnvironment=TRUST_ADMIN_KEY=…
   sudo systemctl edit overpass@ops         # Environment=ANTHROPIC_API_KEY=…  (H5)
   ```
6. Start and check:
   ```sh
   sudo systemctl start overpass@ops overpass@authority overpass@gs-blacksburg \
        overpass@gs-awarua overpass@gs-svalbard-eu overpass@gs-rogue \
        overpass@auditor overpass@spacecraft
   sudo systemctl reload nginx
   scripts/smoke.sh blacksburgbytes.club --gate
   ```
   Fix any host that isn't PASS before the demo.
7. Seed trust and register agents in the index (behavior only, marked seed):
   ```sh
   bin/trustseed -config deploy/local/trustseed.yaml   # adapt url/ids for prod
   ```

## H5 — Model key
`export ANTHROPIC_API_KEY=…` (or the systemd drop-in above) so the LLM planner
runs; without it the planner falls back to the greedy plan.

## H6 — The revoke beat (live, during the demo, 1:45)
`scripts/demo.sh` prints the exact line. In a second terminal, only at the beat:
```sh
ans-cli revoke <gs-blacksburg AgentID> --reason CERTIFICATE_HOLD
```
The next status-token fetch cuts the session and the planner re-books elsewhere.
To restore afterwards, check whether `REMOVE_FROM_CRL` returns the agent to
ACTIVE on the hosted registry (unverified — test before relying on it).

## Rehearse before judging
- Full `docs/demo-runbook.md` cold start in under 3 minutes, keyboard only.
- The `docs/a11y-manual.md` VoiceOver pass.
- Save a screen recording of a clean run as the fallback.
- Run the Phase 9 accessibility checklist (master plan §13) and record results.
- Submit to Devpost (`docs/devpost.md`) with all three tracks.
