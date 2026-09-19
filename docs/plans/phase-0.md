# Phase 0 plan: Bootstrap

Goal: a repo that builds, tests and runs an empty agent. No ANS, crypto or business logic.

## Files
- go.mod: module github.com/arjitsama/overpass, `go 1.25.0` (from ans-sdk-go v0.1.18 go.mod).
- internal/ansdeps: blank-imports ans, verify, verify/scitt, pop so the SDK is a direct,
  compiled dependency before Phase 1 uses it. Removed once real imports exist.
- internal/errs: `Code` string type, `Error{Code, Detail}`, `Write(w, status, code, detail)`,
  JSON body `{code, detail}`. Codes: bad_request, not_found, method_not_allowed,
  payload_too_large, unavailable, internal.
- internal/config: `Load(path) (Config, error)`. YAML, unknown fields rejected, 64 KiB cap.
  Fields: role, host, port, cert{cert_file,key_file}, peers[{name,url}], trust_roots[],
  registry_url, log_url. Env overrides OVERPASS_ROLE/HOST/PORT/CERT_FILE/KEY_FILE/
  REGISTRY_URL/LOG_URL. `Validate` checks role, port range, cert+key both or neither.
- internal/bus: `Event{ts,agent,kind,subject,result,reason,data}` (ts = epoch ms int).
  `Bus.Publish`, `Subscribe(afterID)`, `Close`. Ring buffer of 256 for replay so a late
  subscriber still sees agent_started; honors Last-Event-ID. Slow subscribers drop events,
  never block Publish. Max 64 subscribers, then 503 `unavailable`. `Handler()` serves SSE.
- cmd/agent: flags --config, --role (overrides config). HTTPS server, GET /health ->
  {"status":"ok","role","host"}; /events -> SSE; else 404 JSON. Recover middleware -> 500
  `internal` JSON. SIGTERM/SIGINT -> bus.Close then Shutdown(5s). Self-signed ECDSA P-256
  in-memory cert when no cert configured (never written to disk).
- configs/local/ops.yaml (8443), configs/local/station.yaml (gs-blacksburg, 8444).
- scripts/run-local.sh, scripts/smoke.sh, scripts/accept/phase-0.sh. Makefile, README.md,
  .gitignore (certs/, *.key, *.pem, .env, *.db, bin/, .DS_Store).
- Placeholder dirs from master plan section 4 with .gitkeep: cmd/battery, internal/{wellknown,
  a2a,verify,mandate,passes,planner,session}, web, deploy, docs/status.

## Dependencies (rule 7)
- ans-sdk-go v0.1.18: required by stack.
- go.yaml.in/yaml/v3: config parsing; already in the SDK's module graph, no new supplier.

## Tests, one per acceptance criterion
1. make build / make test on a clean clone: verified by cloning into a temp dir and running both.
2. ops + station /health ok: scripts/accept/phase-0.sh; unit test TestHealthOK in cmd/agent.
3. agent_started over /events: phase-0.sh; unit tests TestEventsReplaysAgentStarted (cmd/agent)
   and TestHandlerReplaysBacklog (bus).
4. phase-0.sh exits non-zero on failure: run it with a station port nobody listens on
   (SMOKE_STATION_PORT override) and assert non-zero.
Plus: config (valid, unknown field, env override, bad role, oversize, cert without key),
errs body shape, bus (order, Last-Event-ID, slow subscriber, Close, subscriber cap, race),
agent (404 JSON, 405 JSON, panic -> internal JSON, clean shutdown with SSE client open).

## Risks
- SSE keeps connections open, so http.Server.Shutdown would hang: bus.Close ends handlers first.
- Section 4 lists role `rogue`; CLAUDE.md lists ops|authority|station|auditor|spacecraft.
  CLAUDE.md wins; rogue is a station config variant later.

## Assumptions
- Section 4's human checklist (domain, VPS, ans-cli, keys) is gates H1/H4, not code. Ports 8443/8444 free.

## Review
Separate review subagent; no high-severity findings. All fixed:
1. Stalled SSE client pinned handler/slot/shutdown: 10 s write deadline; Shutdown timeout -> Close.
2. Last-Event-ID from a previous boot skipped agent_started: stale ID replays all backlog.
3. run-local.sh exited 0 when an agent died: exits 1. 4. phase-0.sh now automates checks 1 and 4.
5. Unit tests were HTTP/1.1 only: force h2. 6. Startup wait timeout now fails explicitly.
7. smoke checks role. 8. Late panic keeps body intact. 9. Startup errs coded; 1 YAML doc; cgo note.
