# Overpass — project status

**Verified command authority for rented ground stations.** A multi-agent broker
for satellite ground-station passes (VTHacks 14). A booking gives a station
time-boxed authority to relay commands, and Agent Name Service (ANS)
verification gates every step.

- **Repo:** https://github.com/arjitsama/overpass · Go module `github.com/arjitsama/overpass`
- **State:** Phases 0–11 shipped and tagged **`v1.0`**. `go test -race ./...`,
  `make lint`, and every `scripts/accept/phase-*.sh` (0–11) pass locally.
- **Live dashboard (once deployed):** `https://ops.blacksburgbytes.club/ui/`
- This file is the single narrative + deviation audit + human-work handbook.
  Companion docs: the compact table in [PROGRESS.md](PROGRESS.md), per-phase
  detail in [docs/status/](docs/status/), and the runbooks in [docs/](docs/).

## The 8-step flow (what it does)

1. Ops predicts tonight's real passes from public orbit data (SGP4).
2. It discovers stations through ANS by capability and verifies each (badge,
   cert, TLSA/DANE, SCITT receipt, a `jku`-pinned signed agent card).
3. A trust index scores each agent on five dimensions; access tiers gate what a
   station may do.
4. An LLM planner proposes bookings through tools; the **authority**, not the
   model, signs a mandate or refuses on policy.
5. The mandate names one station, one satellite, one window; Ops signs each
   command with a per-satellite counter.
6. The station may relay but cannot forge or replay; the spacecraft checks typ,
   signature, NORAD ID and a strictly increasing counter.
7. Revocation (`CERTIFICATE_HOLD`) cuts the session on the next status-token
   fetch and triggers a re-plan; a network blip alone does not.
8. An auditor re-verifies each pass and probes stations with canaries; a station
   that accepts a forged mandate loses its tier.

---

## Status at a glance

| Phase | Title | Tag | What it delivers |
| --- | --- | --- | --- |
| 0 | Preflight, skeleton, smoke | `phase-0` | One binary, `/health` + `/events`, run-local, smoke. |
| 1 | Schemas, JCS, chains, thumbprints | `phase-1` | Wire formats (JCS/RFC 8785, ES256), hash chains, RFC 7638 thumbprints. **Schema frozen.** |
| 2 | Agent cards, A2A, DPoP inbound | `phase-2` | Signed agent/trust cards, A2A JSON-RPC, DPoP-gated routes. |
| 3 | ANS verification, webmesh, jku pinning | `phase-3` | `VerifyPeer` (badge/cert/TLSA/receipt), webmesh client, `jku` pinning. |
| 4 | Passes (SGP4) + greedy planner | `phase-4` | Real pass prediction, explainable greedy scheduler, replan. |
| 5 | Quote, mandate, booking | `phase-5` | Quotes, `issue_mandate`, `book_pass` (12 ordered checks), receipts. |
| 6 | Pass session (commands, revocation) | `phase-6` | Windowed relay, counters, token loop, session cut + replan. |
| 7 | Adversaries, battery, auditor | `phase-7` | 23-attack battery, auditor + canary probes, deploy gate. |
| 8 | Trust index and tiers | `phase-8` | Forked trust index + `pass_delivery` signal; tiers on the truthful vector. |
| 9 | Dashboard and accessibility | `phase-9` | Keyboard/screen-reader-first `/ui/`; axe-clean; 3 themes. |
| 10 | LLM planner | `phase-10` | Tool-use planner bounded by policy; greedy fallback. |
| 11 | Deploy, demo, submission | `v1.0` | Prod configs, systemd/nginx-SNI, install.sh, smoke, preflight, docs. |

Every phase followed the loop protocol (PLAN → CODE → hostile REVIEW → TEST →
SHIP) with its own `docs/plans/phase-N.md`, `docs/status/phase-N.md`, and
`scripts/accept/phase-N.sh`.

## Architecture

One binary, `bin/agent --role ops|authority|station|auditor|spacecraft`. Ops
finds and verifies stations through ANS; the authority signs a mandate only if
the flight rules and the station's trust tier allow it; the station relays
Ops-signed commands to the (simulated) spacecraft without being able to forge or
replay them; the auditor scores behavior back into a forked trust index. See the
Mermaid diagram and section-by-section detail in the [README](README.md).

---

## Changes & additions vs the master plan

The build stayed faithful to `docs/master-plan.md`, with these deliberate,
documented deviations and additions (each made to keep the system **truthful**
and to match the real external APIs read at build time, per hard rule 9):

1. **Trust tiers computed from the truthful vector, not `recommendedProfile`
   (Phase 8).** Reading the real `agent-trust-discovery` engine showed identity
   is derived from `certtype` alone (DV=40), and its FIDUCIARY rule needs every
   active dimension ≥ 80 — unreachable for our demo-CA (DV) certs without faking
   an EV cert or patching the classifier. Per the user's decision, Overpass does
   **neither**: the fork adds only the `pass_delivery` behavior signal (upstream
   thresholds/classifier untouched), and the **authority's flight rules** gate
   access on the real five-dimension vector — availability (verified), downlink
   (integrity ≥ 50, no audit failures), uplink (integrity ≥ 80, behavior ≥ 80,
   ≥ 3 audited passes, no failures). Identity is displayed, never fabricated; a
   new `min_cert_type` flight rule (default DV; OV/EV in production) is the cert
   gate. Details + `file:line` refs: [docs/status/phase-8.md](docs/status/phase-8.md).
2. **Frozen schema respected for the injection demo (Phases 1 & 10).**
   `schema.Quote` has no free-text note and the wire schema is frozen (changing
   it needs a human), so the demo's "ignore prior rules…" quote-note injection is
   modelled as planner `Input.Notes` (hostile station free text the LLM planner
   strips) rather than a new wire field.
3. **LLM planner bounded by policy (Phase 10).** Uses the current Anthropic
   Messages API tool-use loop (verified from the live docs, not memory), default
   model `claude-sonnet-5`, `net/http` only — **no new dependency**.
   `propose_booking` only forwards to the authority; a 10s timeout falls back to
   the greedy plan; the model never sees station free text.
4. **Trust index vendored as a fork (Phase 8).** `third_party/agent-trust-discovery`
   at a pinned commit (MIT, `third_party/PATCHES.md`), imported via a `replace`
   directive with a tiny exported `overpassapi` shim (Go forbids importing another
   module's `internal/`). This bumped the Go toolchain to **1.26.5** to match the
   fork's `go.mod`.
5. **Additions:** `bin/agent --verify <host>` (read-only VerifyPeer for smoke);
   the auditor role wired end to end; a **23-check** attack battery; SNI-passthrough
   deploy so each host keeps its **own** ANS-issued cert; a repo secret scanner in
   `make lint`; and `make preflight` as a deploy gate.
7. **DANE reported by name (Phase 12):** `bin/agent --verify` and the dashboard
   show the DANE outcome — `Verified` / `Skipped` / `NoRecords` / `Mismatch` —
   never a bare pass. TLSA-without-DNSSEC is `Skipped` (present but not relied on),
   a warning that still verifies; only a `Mismatch`/DNSSEC failure rejects. The
   earlier "ANS rejects TLSA without DNSSEC" wording was corrected across the docs.
6. **DNS is on Porkbun, not GoDaddy.** The plan assumed GoDaddy DNS; the domain
   `blacksburgbytes.club` is at Porkbun (ANS registration is still at GoDaddy —
   separate systems). DNSSEC + TLSA + SVCB support must be confirmed in the
   Porkbun UI (H1); documented fallback if unavailable is badge + SCITT receipt +
   cert-fingerprint, reported truthfully.

## Honest limits

- The **spacecraft and the RF link are simulated** — no real radio, no real bus.
  The spacecraft is an **internal process with no public ANS identity** (it is not
  registered on ANS; its security is the Ops signature on each command).
- **No on-chain settlement**; x402 payment options are described, not executed.
  (The external supplier at supplier.webmesh.ai does require EIP-3009 on Base
  Sepolia for a *valid* booking; we would answer `PAYMENT_REQUIRED` — see
  docs/webmesh-interop.md.)
- Trust observations are **seeded** for the demo (behavior only, marked as seed
  data). Identity and integrity scores are never fabricated.
- We **run our own trust index and auditor**; in production a neutral party would.
- Local identity uses demo-CA (DV) certs, so the index's own FIDUCIARY tier isn't
  reachable locally; Overpass gates on the truthful vector instead.

## Stubbed / deferred (consolidated from the phase status docs)

- **Repeatable session-cut demo (Phase 12):** a `session.Compromise` test control
  makes a chosen peer read as non-ACTIVE on the next status-token check, so the
  session cuts (`SESSION_CUT:revoked`) and re-plans — repeatable and resettable
  without a real `ans-cli revoke` (which is terminal). Exposed as a labelled
  dashboard button (*Simulate compromise*) and a `test_controls`-gated
  `/control/compromise` route; the real revoke is optional, once, `gs-spare` only
  (docs/demo-runbook.md). Unit-tested (cut + reset).
- **Demo drivers:** the dashboard's *Run demo pass* and *Run battery* buttons
  replay a recorded event stream (`web/testdata/demo-events.json`); *Ask GoDaddy's
  agent to verify* is a real webmesh call. A fully live pass/battery run is the
  agent flow / `cmd/battery`. (Phase 9)
- **Cross-process pass flow (Phase 12, in progress):** `internal/opsflow` now
  drives a real pass over A2A — verify the station, quote, get a mandate from the
  authority (`issue_mandate` over A2A, DPoP-signed), book, and relay one command —
  with `cmd/opsflow` as its CLI and hermetic unit tests (full flow + the
  unverified-peer-refused-before-quote ordering). **Still to land for item 1:** a
  real-process `scripts/accept/phase-12.sh` on the local ANS stack (`../ans`) that
  registers ops/authority/gs-blacksburg, runs them plus the spacecraft as separate
  processes, and asserts true two-way `VerifyPeer` end to end. (Note: phase-3
  verifies the local stack via a Go test, not runtime agent-to-agent VerifyPeer, so
  wiring the local `environments`/root-keys into the ops+authority configs is new.)
- **Planner wiring:** the greedy and LLM planners are libraries with tests;
  `authority.Propose` is exercised in-process and now also reachable cross-process
  via the opsflow path above. An HTTP planning route on Ops is not yet wired. (Phase 10)
- **Trust index provenance:** `provenance.source` is carried via `aimId`
  (`overpass-seed` / `overpass-auditor`) because the index's API has no free
  `source` field. (Phase 8)
- **Local integrity/identity have no source** (no prober against real DNS/certs),
  so a behavior-only-seeded honest station reads READ_ONLY locally; the tier
  *logic* is verified with representative test vectors. (Phase 8)
- Per-phase specifics: see each `docs/status/phase-*.md` "Stubbed / deferred".

---

## Human-gated work (H1–H6)

Everything code-side is done and tested. The rest is human by design. Full,
copy-pasteable version: **[docs/deploy-runbook.md](docs/deploy-runbook.md)**;
tracked as a checklist in **[docs/human-actions.md](docs/human-actions.md)**.
Order: **H1 → H2 → H3 → H4**, rehearse, then **H6** live during the demo.

> **Hard rule 1:** never run `ans-cli register` / `revoke` or any write against
> `https://api.godaddy.com` except intentionally at H2 and H6. Reads are fine.
> ANS registrations are **permanent** (append-only transparency log).

Hosts (all `.blacksburgbytes.club`): `ops`, `authority`, `gs-blacksburg`,
`gs-awarua`, `gs-svalbard-eu`, `gs-rogue`, `auditor`, `spacecraft`.

### H1 — Domain & DNSSEC (Porkbun)
1. Confirm `blacksburgbytes.club` is active.
2. Porkbun → DNSSEC → **enable it**. Without DNSSEC the DANE check is `DANESkipped`
   (TLSA present but not relied on) and verification still passes on badge +
   receipt + cert-fingerprint; DNSSEC makes DANE count (`DANEVerified`), and only a
   `DANEMismatch`/DNSSEC failure rejects.
3. Confirm Porkbun can add **TXT, TLSA, and SVCB/HTTPS** records; if not, use the
   badge + receipt + cert-fingerprint fallback and report it truthfully.
4. `export ANS_API_KEY=…` and `export ANS_BASE_URL=https://api.godaddy.com`.

### H2 — Register each agent on ANS (PERMANENT; one host, one step at a time)
1. `bin/agent --config deploy/prod/<name>.yaml --write-card certs/<name>/agent-card.json`
2. `scripts/register.sh <name>.blacksburgbytes.club` (dry-run: prints the plan)
3. `scripts/register.sh <name>.blacksburgbytes.club --step csr --i-am-a-human-and-this-is-permanent`
4. `--step register …` → copy the printed **Agent ID** into `deploy/agents.env`
5. Publish the ACME challenge, then `--step verify-acme`
6. `--step status`, `--step certs`, and (after H3) `--step verify-dns`
7. Repeat for all 8 hosts; record every Agent ID + log URL in `deploy/agents.env`.

### H3 — DNS records (Porkbun UI)
For each host: run `scripts/dns-records.sh <name>.blacksburgbytes.club 0.1.0
<agentId> certs/<name>/server.pem <log-url>`, create the printed `_ans` TXT,
`_ans-badge` TXT, `_443._tcp` TLSA (`3 0 1 <sha256 of served cert>`) and SVCB
records, then verify with `dig +dnssec` and `dig TLSA`.

### H4 — VPS bring-up
1. Rent a VPS; point every `<name>.blacksburgbytes.club` A record at it; open :443.
2. `make build`; `scp` `bin/agent`, `deploy/`, and per-host `certs/<name>/` to the box.
3. `sudo apt-get install -y gettext-base nginx libnginx-mod-stream`; copy
   `deploy/agents.env.example` → `/etc/overpass/agents.env` and fill it in
   (Agent IDs, log URLs, `BASE_DOMAIN`).
4. `sudo deploy/install.sh --dry-run` (review), then `sudo deploy/install.sh --apply`.
5. Put secrets in systemd drop-ins: `sudo systemctl edit overpass@authority`
   (`Environment=TRUST_ADMIN_KEY=…`), `overpass@ops` (`ANTHROPIC_API_KEY=…`).
6. `systemctl start overpass@…`, `systemctl reload nginx`, then
   `scripts/smoke.sh blacksburgbytes.club --gate`; fix any non-PASS host.
7. Seed trust (behavior only, marked seed): `bin/trustseed -config <prod seed cfg>`.

### H5 — Model key
`export ANTHROPIC_API_KEY=…` (or the systemd drop-in) so the LLM planner runs;
without it the planner falls back to the greedy plan.

### H6 — The revoke beat (live, during the demo, 1:45)
`scripts/demo.sh` prints the exact line. In a second terminal, only at the beat:
`ans-cli revoke <gs-blacksburg AgentID> --reason CERTIFICATE_HOLD`. The next
status-token fetch cuts the session and the planner re-books elsewhere.

### Rehearse & submit
- `docs/demo-runbook.md` cold start under 3 minutes, keyboard only.
- The `docs/a11y-manual.md` VoiceOver + keyboard pass.
- Save a screen recording as the fallback; run the master-plan §13 checklist.
- Submit via `docs/devpost.md` with all three tracks (GoDaddy ANS, Peraton
  Mission-Critical AI, Best Accessibility).

---

## Build / test / run

```sh
make build        # bin/agent, battery, cardhash, passes, satreg, trustseed
make test         # go test -race ./...
make lint         # gofmt + go vet + scripts/secret-scan.sh
make preflight    # tests + honest-station battery + local smoke (deploy gate)
make trust-up     # forked trust index on :8080
make run-local    # ops :8443 + station :8444
make smoke        # local health/events checks (in a second terminal)
make accept       # every scripts/accept/phase-N.sh in order
scripts/demo.sh   # cold start to the dashboard; prints the revoke command
scripts/smoke.sh blacksburgbytes.club   # per-host production checks (read-only)
```

## Repo map

- `cmd/agent` — the one binary (roles, dashboard routes, `--verify`, `--write-card`).
- `cmd/{battery,cardhash,passes,satreg,trustseed}` — tools.
- `internal/{schema,jose,chain}` — frozen wire formats + crypto (`docs/schemas.md`).
- `internal/{a2a,wellknown,verify,webmesh}` — A2A, identity files, ANS verification.
- `internal/{passes,planner,llmplan}` — prediction, greedy + LLM planners.
- `internal/{authority,station,session,spacecraft,ops}` — the roles' logic.
- `internal/{battery,auditor,trust,bus,store,config,errs}` — adversaries, audit,
  trust client, event bus, storage, config, error codes.
- `web/` — the dashboard (HTML/JS/CSS, embed, tests).
- `third_party/agent-trust-discovery/` — the vendored trust-index fork.
- `deploy/` — prod configs, systemd/nginx templates, `install.sh`, local profiles.
- `scripts/` — register/dns/smoke/demo/preflight/secret-scan + `accept/`.
- `docs/` — `master-plan.md`, `schemas.md`, `plans/`, `status/`, the runbooks,
  `a11y-manual.md`, `human-actions.md`, `devpost.md`.
