# Phase 0 status: Bootstrap

## What works
- `go.mod` for `github.com/arjitsama/overpass`, `go 1.25.0` (from ans-sdk-go v0.1.18).
  The SDK packages `ans`, `verify`, `verify/scitt` and `pop` compile through `internal/ansdeps`.
- Directory layout from master plan section 4, plus `internal/errs`, `internal/config`,
  `scripts/accept/`, `docs/plans/` and `docs/status/`. Empty packages hold a `.gitkeep`.
- `internal/errs`: named codes (`bad_request`, `not_found`, `method_not_allowed`,
  `payload_too_large`, `unavailable`, `internal`) and the JSON body `{code, detail}`.
- `internal/config`: one YAML file per agent (role, host, port, cert paths, peers,
  trust roots, registry and log URLs).
  - Rejects unknown fields and more than one document. Size cap is 64 KiB.
  - Environment overrides use `OVERPASS_*`.
  - Registry and log URLs default to production GoDaddy and must be https.
- `internal/bus`: event bus. Events have the shape `{ts, agent, kind, subject, result, reason, data}`.
  - SSE is served at `/events`, with a 256-event replay and `Last-Event-ID` resume.
    A stale ID from a previous boot replays everything.
  - Slow clients are dropped instead of blocking publishers. Writes time out after 10 s.
  - The subscriber cap is 64; past it, new clients get 503 `unavailable`.
- `cmd/agent`: `--config` and `--role`.
  - Serves HTTPS on HTTP/1.1 and HTTP/2, with `GET /health` and `/events`.
  - Unknown routes get 404 `not_found`; wrong methods get 405.
  - Panics are recovered and return 500 `internal`. Request bodies are capped at 1 MiB.
  - On SIGTERM or SIGINT it publishes `agent_stopping`, closes the streams and shuts down within 5 s.
  - If no cert is configured, it creates a throwaway self-signed ECDSA P-256 cert in memory only.
- Makefile targets: `build`, `test`, `lint`, `run-local`, `smoke`, plus `accept` and `clean`.
  README has a quick start.

## How it was tested
- `gofmt -l` is clean, `go vet ./...` passes, and `go test -race ./...` passes (30 test functions across 4 packages).
- `scripts/accept/phase-0.sh` passes. It covers:
  1. A clean copy of the repo passes `make build test`.
  2. `run-local` starts ops on :8443 and the station on :8444. `/health` returns `ok`,
     with the expected role on each port.
  3. `/events` on both agents carries `agent_started`.
  4. `smoke.sh` exits non-zero when pointed at a port with no agent.
- Manual checks:
  - With 8444 held by another agent, `run-local.sh` exits 1 and `phase-0.sh` fails with a named port error.
  - Both agents log `stopped` on SIGTERM.
- A separate review subagent reviewed the diff. Its 11 findings are listed in
  `docs/plans/phase-0.md` under "Review", and all are fixed.

## Stubbed or deferred
- `internal/{wellknown,a2a,verify,mandate,passes,planner,session}`, `cmd/battery`, `web/`
  and `deploy/` are empty placeholders for later phases.
- `internal/ansdeps` is temporary. Delete it once Phase 1 or 3 imports the SDK directly.
- `peers`, `trust_roots`, `registry_url` and `log_url` are loaded and validated, but nothing uses them yet.
- No ANS calls, crypto or business logic, per the phase prompt.

## Needs a human
- Gate H1: buy the domain(s), then export `ANS_API_KEY` and `ANS_BASE_URL`.
- Gate H4: the VPS.
- The master plan's section 4 checklist items (ans-cli install, the `ans-cli search` smoke test,
  a wildcard A record, the reference repo clones) are outside the code in this phase.
- Note: `go test -race` needs cgo and a C toolchain (Xcode CLT on macOS).
