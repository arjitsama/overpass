# Keys and go-live — the one ordered checklist

Everything code-side is done and tested. This is the exact order for **you** to
plug in real credentials and take Overpass live. **Never paste a secret into a
chat or a tracked file.** Secrets live in exactly two places, both git-ignored /
out of the repo:

- **`.env` at the repo root** (git-ignored; used by your shell for registration
  and DNS scripts you run locally).
- **systemd drop-ins on the VPS** (never in the repo) for the services.

`.env` is in `.gitignore`, and `scripts/secret-scan.sh` (run by `make lint`)
flags any secret that is force-added or committed anyway — verified: a tracked
`.env` with an `ANS_API_KEY=…` is caught.

## Step 0 — Secrets, where each one goes

**Local shell / `.env` at repo root** (create it; it is git-ignored):
```sh
# .env  (repo root — NEVER commit; git-ignored + secret-scanned)
ANS_API_KEY=<key>:<secret>              # GoDaddy ANS SSO key, as key:secret
ANS_BASE_URL=https://api.godaddy.com
PORKBUN_API_KEY=<pk1_...>
PORKBUN_SECRET_API_KEY=<sk1_...>
```
Load it before running scripts: `set -a; . ./.env; set +a`.

**On the VPS, per-service systemd drop-ins** (not the repo, not agents.env):
```sh
sudo systemctl edit overpass@authority   # [Service]\nEnvironment=TRUST_ADMIN_KEY=<bearer>
sudo systemctl edit overpass@auditor     # Environment=TRUST_ADMIN_KEY=<bearer>
sudo systemctl edit overpass@ops         # Environment=ANTHROPIC_API_KEY=<key>   (H5)
```
`/etc/overpass/agents.env` on the box holds only **non-secret** values
(`BASE_DOMAIN`, Agent IDs, log URLs, ports). Confirm it never contains a secret.

## Step 1 — Domain & DNS (Porkbun) — H1
1. `set -a; . ./.env; set +a`
2. Porkbun: delete the default parking records (`ALIAS` on `@`, `CNAME` on `*` →
   `pixie.porkbun.com`); enable **DNSSEC**; use TTL 600. (See docs/deploy-runbook.md H1.)

## Step 2 — Register, Stage 1 first — H2 (PERMANENT)
Register **`ops`** and **`gs-blacksburg`** only, one step at a time
(`scripts/register.sh <host> --step <step> --i-am-a-human-and-this-is-permanent`).
Put each printed Agent ID into `/etc/overpass/agents.env`. Do **not** register
`spacecraft` (internal) or `gs-sva1bard-eu` (impostor).

## Step 3 — DNS records for Stage 1 — H3
For `ops` and `gs-blacksburg`, run `scripts/dns-records.sh …` and create the
printed `_ans`/`_ans-badge` TXT, `_443._tcp` TLSA, and SVCB records in Porkbun.

## Step 4 — VPS bring-up — H4
`scp` `bin/agent`, `deploy/`, and the per-host `certs/<name>/` to the box; fill
`/etc/overpass/agents.env`; `sudo deploy/install.sh --dry-run` then `--apply`;
add the systemd secret drop-ins (Step 0); `systemctl start overpass@ops
overpass@gs-blacksburg …`; `systemctl reload nginx`.

## Step 5 — Smoke (read-only)
```sh
scripts/smoke.sh blacksburgbytes.club --gate
```
Every registered host should read PASS. Fix any before continuing.

## Step 6 — One real cross-process pass
```sh
bin/opsflow -config /etc/overpass/ops.yaml \
  -station-host gs-blacksburg.blacksburgbytes.club \
  -station-url  https://gs-blacksburg.blacksburgbytes.club/ \
  -authority-url https://authority.blacksburgbytes.club/ \
  -authority-key /etc/overpass/certs/authority/identity.pub -norad 27844 -mode uplink
```
Expect `PASS station=… quote=… mandate=… booking=… ack=accepted`.
*(Stage 2 — `authority`, `gs-svalbard-eu` — must be registered first for the
mandate step. The demo runs correctly with Stage 1 + Stage 2.)*

## Step 7 — Stage 2, then optional Stage 3
Register `authority` + `gs-svalbard-eu` (Stage 2). Then, if time,
`auditor`, `gs-rogue`, `gs-awarua`, `gs-spare` (Stage 3). Re-run Steps 3 + 5 for
each. Seed trust (`bin/trustseed`, behavior only) and run warm-up passes so
`gs-blacksburg` earns ≥ 3 audited passes.

## Step 8 — Demo & submit
Rehearse `docs/demo-runbook.md` (simulated cut is the default beat; the one
optional real revoke is `gs-spare` only, H6). Submit via `docs/devpost.md`.

> **Open item before uplink is reachable in prod:** see the item-5 finding on
> integrity scoring (the trust index's v1 prober stubs DNSSEC and omits
> `certfingerprint.identity`), which currently caps an honest station's integrity
> below the uplink bar. Resolve that (see the report / your chosen option) before
> promising a live uplink; downlink is reachable regardless.
