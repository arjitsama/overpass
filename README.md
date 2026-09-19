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

Config is one YAML file per agent: `role`, `host`, `port`, `public_url`, `cert.{cert_file,key_file}` (TLS),
`identity.{key_file,chain_file}` (ANS identity key and chain), `card.{version,display_name,description,
org_name,org_url,agent_id,receipt_file,tl_agent_url,dns_aid,tier2,skills}`, `peers[{name,url}]`,
`trust_roots`, `registry_url`, `log_url`. Anything left out of `card` is left out of the served files.
Unknown fields are rejected. Environment overrides: `OVERPASS_ROLE`,
`OVERPASS_HOST`, `OVERPASS_PORT`, `OVERPASS_CERT_FILE`, `OVERPASS_KEY_FILE`,
`OVERPASS_REGISTRY_URL`, `OVERPASS_LOG_URL`. Keys and certs live under `certs/`,
which is gitignored.

## Endpoints

| Path | Method | Returns |
| --- | --- | --- |
| `/` | POST | A2A JSON-RPC 2.0: `SendMessage` (A2A 1.0) and `message/send` (0.3) |
| `/` | GET | accessible HTML page about the agent |
| `/.well-known/agent-card.json` | GET | A2A agent card, signed (detached JWS, ES256, `typ agent-card+jws`, `jku` = own trust card) |
| `/.well-known/ans/trust-card.json` | GET | ANS trust card: EC P-256 key with `x5c` |
| `/health` | GET | `{"status":"ok","a2aProtocolVersion":"1.0",...}` |
| `/.well-known/{jwks,did,ard,ai-catalog}.json`, `/robots.txt`, `/llms.txt` | GET | tier 2; `card.tier2: false` turns them off |
| `/events` | GET | `text/event-stream` of `{ts,agent,kind,subject,result,reason,data}`; honors `Last-Event-ID` |

**Calling a skill.** Send a message whose data part is `{"skill": "<id>", ...args}`.
A message without one gets a text reply listing the skills. Rejections are JSON-RPC
`-32602` with a `google.rpc.ErrorInfo` whose `reason` is the `internal/errs` code.

**Security by role** (the card is generated from what is mounted):
- **station:** DPoP (ans-sdk-go `pop`) on every A2A call, plus a mandate on `book_pass`.
- **authority:** DPoP.
- **ops, auditor, spacecraft:** `noAuth`.

DPoP needs the transparency log's root keys in `trust_roots` (C2SP strings). With none,
every inbound call gets `401 CALLER_REJECTED` (fail closed).

**Card hash.** `bin/cardhash [-k] <card URL>` prints the SHA-256 to compare with the
registered `metaDataHash`. Cards are served in JCS form, so the raw and JCS hashes agree.

Every rejection is JSON `{"code":...,"detail":...}` with a code from `internal/errs`.

## Verification (`internal/verify`)

`Verifier.VerifyPeer(ctx, host)` runs eight checks, emits one bus event per check, and fails closed:

| Check | What passes |
| --- | --- |
| `registered` | `_ans-badge` TXT points at this environment's log, and the badge is for this host |
| `badge` | badge status ACTIVE (WARNING or DEPRECATED is a warning) |
| `scitt_receipt` | the receipt verifies against the log's root keys |
| `status_token` | the token verifies, is for this agent and host, and is ACTIVE; a fetch error passes for 10 min on the last good token |
| `cert_chain` | the server leaf the peer presents is attested in the status token and covers the host |
| `tlsa` | DANE `_443._tcp` matches (no record is a warning) |
| `card_hash` | the served card's SHA-256 equals the registered `metaDataHash` |
| `card_signature` | the card verifies with the trust-card key at a pinned `jku`, and its `x5c` leaf is an attested identity cert |

**Other pieces:**
- `FindByTag` uses the ANS Finder.
- `Outbound.Attach` signs outbound requests with DPoP and adds SCITT headers.
- `internal/webmesh` asks agent.webmesh.ai's MCP `verify_agent` tool for its verdict.
- Environments (production and a local stack) and peers are listed in config under
  `environments` and `peers[{name,url,env,dial}]`.

## Local ANS stack

```sh
git clone https://github.com/agentnameservice/ans.git ../ans
scripts/local-ans.sh start            # RA :18080, log :18081, Finder :18082, DNS :15353
scripts/local-ans.sh env              # environment block (with root key) for a config
scripts/local-register.sh gs-blacksburg.localhost 0.1.0 certs/local/gs-blacksburg configs/local/station-ans.yaml
bin/agent --config configs/local/station-ans.yaml
scripts/local-ans.sh stop
```

`scripts/accept/phase-3.sh` does all of this and runs the checks.

**Registering on production is a human step:**
- `scripts/register.sh <host>` only prints the plan. Each step needs `--step <step>`
  and `--i-am-a-human-and-this-is-permanent`.
- `scripts/dns-records.sh` prints the DNS records to create.

## Layout

`cmd/agent` (the one binary), `cmd/battery` (attack battery, later),
`internal/{config,errs,bus}` (agent plumbing), `internal/{a2a,wellknown}` (A2A server, identity files), `cmd/cardhash`, `internal/{jose,schema,chain}`
(wire formats and crypto; see `docs/schemas.md`, frozen after Phase 1),
`internal/{wellknown,a2a,verify,mandate,passes,planner,session}` (later phases),
`web/` dashboard, `deploy/`, `scripts/accept/` per-phase acceptance,
`docs/plans/` and `docs/status/`.
