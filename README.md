# Overpass

Multi-agent broker for satellite ground station passes, built for VTHacks 14.
A booking gives a station time-boxed authority to relay commands, and Agent
Name Service (ANS) verification gates every step. Design: `docs/master-plan.md`.

## Quick start

Needs Go 1.25+ (the version ans-sdk-go requires), `make`, `curl`, and a C
toolchain for `go test -race` (Xcode command line tools on macOS).

```sh
make build        # bin/agent
make test         # go test -race ./...
make lint         # gofmt + go vet
make run-local    # ops on :8443, gs-blacksburg station on :8444; Ctrl-C stops both
make smoke        # in a second terminal: /health and /events checks
make accept       # every scripts/accept/phase-N.sh in order
```

Local agents make a throwaway self-signed cert in memory, so use `curl -k`:

```sh
curl -k https://localhost:8443/health
curl -kN https://localhost:8443/events    # server-sent events, starts with agent_started
```

## Running one agent

```sh
bin/agent --config configs/local/ops.yaml [--role ops|authority|station|auditor|spacecraft]
```

Config is one YAML file per agent: `role`, `host`, `port`, `cert.cert_file`,
`cert.key_file`, `peers[{name,url}]`, `trust_roots`, `registry_url`, `log_url`.
Unknown fields are rejected. Environment overrides: `OVERPASS_ROLE`,
`OVERPASS_HOST`, `OVERPASS_PORT`, `OVERPASS_CERT_FILE`, `OVERPASS_KEY_FILE`,
`OVERPASS_REGISTRY_URL`, `OVERPASS_LOG_URL`. Keys and certs live under `certs/`,
which is gitignored.

## Endpoints

| Path | Method | Returns |
| --- | --- | --- |
| `/health` | GET | `{"status":"ok","role":...,"host":...}` |
| `/events` | GET | `text/event-stream` of `{ts,agent,kind,subject,result,reason,data}`; honors `Last-Event-ID` |

Every rejection is JSON `{"code":...,"detail":...}` with a code from `internal/errs`.

## Layout

`cmd/agent` (the one binary), `cmd/battery` (attack battery, later),
`internal/{config,errs,bus}` (agent plumbing), `internal/{jose,schema,chain}`
(wire formats and crypto; see `docs/schemas.md`, frozen after Phase 1),
`internal/{wellknown,a2a,verify,mandate,passes,planner,session}` (later phases),
`web/` dashboard, `deploy/`, `scripts/accept/` per-phase acceptance,
`docs/plans/` and `docs/status/`.
