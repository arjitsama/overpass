# Phase 11 status: deploy, demo, submission

## What works
- **`bin/agent --verify <host>`**: a read-only VerifyPeer mode (loads the config's
  verifier, prints one line, exits 0/1). Used by smoke; serves nothing, writes nothing.
- **`deploy/`**: `agents.env.example` (the domain in one place — `BASE_DOMAIN=blacksburgbytes.club`
  — plus per-agent Agent IDs / log URLs / ports, and secret *names* only); one
  `deploy/prod/<name>.yaml` per host (`<name>.${BASE_DOMAIN}`, `${..}`-templated);
  `overpass@.service` systemd template (dedicated user, hardened); `nginx-sni.conf.template`
  (SNI passthrough so each host reaches its own process with its own ANS-issued
  cert); and `deploy/install.sh` — idempotent, **`--dry-run` by default**, reads
  `agents.env` on the box, never writes secrets to the repo.
- **scripts**: `smoke.sh` now takes a base domain for per-host production checks
  (health, Tier-1 well-known, `bin/cardhash`, `dig` `_ans`/`_ans-badge`/TLSA/SVCB,
  TLSA == served cert, `--verify`), read-only, report-only by default (`--gate` to
  gate); local mode unchanged (phase-0 still passes). `demo.sh` cold-starts to the
  dashboard and prints the exact `ans-cli revoke … CERTIFICATE_HOLD` line **without
  running it** (H6). `secret-scan.sh` fails on any committed private key / cert /
  token. `preflight.sh` = tests + honest-station battery + local smoke.
- **Makefile**: `make lint` now runs the secret scan; `make preflight` gates a deploy.
- **Docs**: `README.md` expanded (Mermaid architecture diagram, how verification
  works, trust & tiers, dashboard & LLM planner, deploy/preflight, honest limits);
  `docs/devpost.md` (submission draft, §17); `docs/demo-runbook.md` (the 3-minute
  script, command/keypress + fallback per beat); `docs/deploy-runbook.md` (the full
  human-gated H1–H6 step-by-step). `PROGRESS.md` updated.

## How it was tested
- `gofmt -l .` clean, `go vet ./...` clean, `go test -race ./...` green,
  `make lint` (incl. secret scan) clean.
- `scripts/accept/phase-11.sh` passes all five criteria:
  1. `install.sh --dry-run` prints every action and changes nothing.
  2. local smoke passes; production mode runs read-only and reports per host.
  3. `make preflight` passes locally.
  4. secret scan is wired into `make lint` and fails on a planted private key.
  5. README quick start builds/vets on a clean checkout of the tree.
- Regression: phases 0–10 acceptance all still pass.

## What is human-gated (by design — see docs/deploy-runbook.md)
- **H1** buy/confirm the domain and enable DNSSEC at Porkbun (and confirm it can
  serve TLSA/SVCB; documented fallback if not).
- **H2** register each agent on ANS (permanent; `scripts/register.sh`, dry-run by
  default). Agent IDs and log URLs go into `agents.env`.
- **H3** create the DNS records `scripts/dns-records.sh` prints, in the Porkbun UI.
- **H4** rent the VPS and run `deploy/install.sh --apply`; put secrets in systemd
  drop-ins; `scripts/smoke.sh blacksburgbytes.club --gate`.
- **H5** `ANTHROPIC_API_KEY` for the LLM planner.
- **H6** the live `ans-cli revoke … CERTIFICATE_HOLD` at the 1:45 demo beat.

## Honest limits (also in README/devpost)
Simulated spacecraft and RF; no on-chain settlement; seeded trust (behavior only,
marked as seed data — identity/integrity never fabricated); self-run trust index
and auditor; local DV certs, so Overpass gates on the truthful trust vector rather
than the index's own FIDUCIARY tier.

## Ship
Committed as "phase 11: deploy, demo, submission", tagged **v1.0**.
