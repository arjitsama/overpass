# Phase 11 plan: Deploy, demo, submission

Prompt: master plan §13, §15, §17, §18 + all docs/status/phase-*.md. Ship as
"phase 11: deploy, demo, submission", tag v1.0. Human gates: H2, H3, H4, H6.

Domain (from the user): BASE_DOMAIN=blacksburgbytes.club (Porkbun DNS), one
variable in agents.env; hosts are <name>.${BASE_DOMAIN}. Agent IDs / log URLs are
agents.env placeholders (only known after registration). Hosts: ops, authority,
gs-blacksburg, gs-awarua, gs-svalbard-eu, gs-rogue, auditor, spacecraft.

## Files to touch
- cmd/agent/main.go: add read-only `--verify <host>` mode (verify.New + VerifyPeer,
  print one line, exit 0/1; no serving).
- deploy/agents.env.example, deploy/prod/<name>.yaml (8), deploy/overpass@.service,
  deploy/nginx-sni.conf.template, deploy/install.sh (idempotent, --dry-run default).
- scripts/smoke.sh (extend: base-domain mode + keep local mode for phase-0),
  scripts/demo.sh, scripts/secret-scan.sh, scripts/preflight.sh.
- Makefile: lint += secret-scan; new preflight target.
- README.md (Mermaid diagram, verification, honest limits, battery),
  docs/devpost.md, docs/demo-runbook.md, docs/deploy-runbook.md.
- scripts/accept/phase-11.sh.

## Reuse
register.sh (H2), dns-records.sh (H3), /health (wellknown), cmd/cardhash,
run-local.sh, deploy/local/*, verify.New/VerifyPeer, config.Load.

## Tests (one per acceptance criterion)
1. install.sh --dry-run prints actions, creates nothing (assert file set unchanged).
2. smoke.sh local profile passes; base-domain mode runs read-only per host.
3. make preflight passes locally.
4. secret-scan in make lint; plant a fake EC PRIVATE KEY -> scan exits non-zero.
5. README quick start works on a clean `git archive` checkout (build+test).

## Risks / assumptions
- Porkbun must enable DNSSEC and serve TLSA/SVCB; if not, fallback is badge +
  receipt + cert-fingerprint (documented, reported truthfully). Noted in H1.
- No new dependency. install.sh needs envsubst (gettext) on the VPS; documented.
- Production smoke needs live hosts; the acceptance exercises local + a stub.

## Review / status appended after the loop.

## Review (hostile pass, findings fixed)
1. secret-scan used bash-4 `mapfile`; macOS ships bash 3.2 -> rewrote as a
   portable `while read -d ''` loop.
2. phase-11.sh embedded a literal PEM header for the plant test, tripping the
   scanner on itself; build the header from split strings so the source is clean.
3. install.sh dry-run hid the useradd action behind a redirect; print it. Also
   corrected the envsubst comment (it expands all set vars; templates use only
   ${KNOWN}, no literal $).
4. `make lint` runs `gofmt -l .` (whole tree) and flagged a Phase 8 fork test
   file that was never whole-tree gofmt'd; gofmt -w it. (Latent since phase 8.)
5. Confirmed: install.sh dry-run is default and changes nothing; smoke and
   --verify and demo are read-only; demo prints the revoke line but never runs it;
   no write to api.godaddy.com anywhere in phase 11; no new Go dependency.
