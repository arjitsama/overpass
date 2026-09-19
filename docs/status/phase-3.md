# Phase 3 status: Verification

## What works
- **`internal/verify.Verifier.VerifyPeer(ctx, host)`** returns a `Result`. It has eight checks,
  each with a verdict (pass, warn, fail or skip) and a reason, and it fails closed: any fail or
  skip fails the peer. It never panics.
  1. `registered`: the `_ans-badge` TXT exists, its badge URL is on this environment's log, and
     the badge is for this host.
  2. `badge`: status ACTIVE (WARNING or DEPRECATED is a warning).
  3. `scitt_receipt`: verifies against the log's root keys (`scitt.VerifyReceipt`).
  4. `status_token`: `scitt.VerifyStatusToken`, the token is for this agent and ANS-name host,
     then `Policy{MaxAge: 10m}`. The token's `iat` goes in the Result.
  5. `cert_chain`: the server leaf the peer presents is in the token's `ValidServerCerts`
     (`scitt.MatchesServerCert`), covers the host and is unexpired. The card is then fetched
     over a transport pinned to that leaf.
  6. `tlsa`: SDK `DANEVerifier` at `_443._tcp`. No record is a warning; a mismatch or
     DNSSEC failure fails.
  7. `card_hash`: SHA-256 of the served card equals the registered `metaDataHash`. None
     registered fails.
  8. `card_signature`: `wellknown.VerifyCard` with `jku` pinned to the verified host before any
     fetch, the key selected by `kid`, and the `x5c` leaf required to be one of the token's
     `ValidIdentityCerts`.
- **Environments per config** (`environments{prod,local,...}`, `peers[{name,url,env,dial}]`).
  One peer list can mix production and a local stack. The local stack's log serves plain HTTP but
  advertises https, so a transport maps `log_public_url` to `log_url`. It matches on origin, and
  no redirects are followed.
- **Status tokens**
  - `FreshToken` fetches and verifies a new token.
  - `Policy.Decide` applies master plan 9.6.
  - `Keeper` holds the newest good token per agent and never lets a pre-revocation token back
    in, even when fetches finish out of order.
- **Inbound:** `a2a.DPoPGuard` (the SDK's `pop.Middleware`), with the proven identity read via
  `verify.Caller(ctx)`. Stations and the authority now trust every configured environment's root
  keys.
- **Outbound:** `Outbound.Attach(req)` uses `pop.NewSigner`, `pop.AttachIdentity`, and SCITT
  headers from `scitt.HeaderSupplier` (or static bytes).
- **Discovery:** `FindByTag(ctx, env, tag)` posts to the ANS Finder `POST /search` (reference
  repo spec) and runs VerifyPeer on each publisher host in that environment. The production
  Finder URL is unknown, so it returns `unavailable` there.
- **`internal/webmesh`:** an MCP client for https://agent.webmesh.ai/mcp/. It runs `initialize`,
  then `tools/list`, and builds calls from the returned schemas. The live tool is
  `verify_agent{agent_host, environment}`. There is no discover tool live, so `Discover` returns
  `ErrNoTool`.
- **Card freeze:** `card.signed_file` persists the signed card, so its bytes (and so
  `metaDataHash`) survive restarts. `bin/agent --write-card <path>` writes it before
  registration.
- **Scripts**
  - `scripts/register.sh`: ans-cli wrapper. Dry run by default. For real it runs only one
    `--step`, and only with `--i-am-a-human-and-this-is-permanent`. Commands run as argv arrays.
  - `scripts/dns-records.sh`: prints `_ans`, `_ans-badge`, TLSA `3 0 1 <sha256(cert DER)>` and SVCB.
  - `scripts/local-ans.sh start|stop|env`: runs the reference stack from `../ans`.
  - `scripts/local-register.sh`: registers our agent on the **local** RA only, with the card
    hash and function tags, and writes the RA-issued certs.

## How it was tested
- `gofmt` is clean, `go vet` passes, and `go test -race ./...` passes.
- `scripts/accept/phase-3.sh` passes:
  1. **Local ANS stack, end to end.** A fresh stack is started, our station registered, and
     `bin/agent` run with the RA-issued certs.
     - The station passes all 8 checks, including TLSA, `card_hash` against its registered
       `metaDataHash`, and the card signature against the attested identity leaf.
     - The unregistered ops agent fails: "no _ans-badge TXT record: not an ANS agent".
     - `FindByTag("uplink-uhf")` finds and verifies the station.
  2. **Live read-only check** (`ANS_LIVE=1`) of agent.webmesh.ai. The Result is recorded in
     `internal/verify/testdata/webmesh-result.json`. See "Live findings" below.
  3. **Forged `jku`:** `CARD_REJECTED:jku`, and a counting server proves zero requests reached
     the forged host.
  4. **Token policy table:**
     - fresh ACTIVE passes
     - a 5-minute-old token after a fetch error passes with a warning
     - an 11-minute-old token fails
     - non-ACTIVE fails at once
     - plus revocation ordering tests
  5. **DPoP:** no proof → `401 CALLER_REJECTED`, and a replayed proof is rejected. The outbound
     helper passes the inbound middleware using real SCITT credentials (ans-sdk-go demokit).
  6. **`register.sh`:** without the flag it prints the plan, exits 0 and never invokes a fake
     ans-cli. With the flag it refuses to run without `--step`. It passes argv intact, including
     `$(...)`.
  7. **Events:** every check (skipped ones included) emits one `verify_check` event, plus one
     `verify_peer` summary event.
- The Phase 0, 1 and 2 acceptance scripts still pass.
- A separate review subagent reviewed the diff. Its 11 findings are listed in
  `docs/plans/phase-3.md`, and all are fixed. The high-severity one: a malformed host,
  including from Finder content, could panic VerifyPeer.

## Live findings (read-only, 2026-09-19)
- **agent.webmesh.ai verifies as FAIL.** Registered and receipt pass, but:
  - The badge and status token say `WARNING`: its cert expires within 30 days.
  - The cert it serves (`a2ec49…`, valid to Dec 2026) is **not** the one its registration
    attests (`6504d1…`). It looks like a renewed cert that was never re-registered.
  - It publishes no TLSA record.
  - Its own MCP `verify_agent` reports the card as "unreachable".
- Useful demo contrast: identity drift is caught even on GoDaddy's reference agent.

## Deviations and decisions
- **Badge path:** cert, identity and `metaDataHash` checks use the TL-signed status token, not
  the SDK's badge `ServerVerifier`. SDK v0.1.18's `models.Badge` reads only V1 `serverCert`,
  while the reference stack emits V2 `serverCerts[]`, so the SDK path would always mismatch
  there. Production serves V1.
- **Tokens that fail policy:** a verified token that is not ACTIVE still attests certs, so the
  Result is complete. The peer has already failed at that point.
- **`metaDataHash`:** its format is `SHA256:<hex>` over the served card (RA spec). Our cards
  are JCS bytes persisted via `signed_file`.
- **`register.sh` transports:** ans-cli's default is `STREAMABLE-HTTP`. The RA spec also allows
  `JSON_RPC`, which `local-register.sh` uses. Confirm the production value before gate H2.

## Needs a human
- **H1–H3 for our real hosts:**
  - Run `scripts/register.sh <host> --step … --i-am-a-human-and-this-is-permanent` one step at a
    time, then publish `scripts/dns-records.sh` output with DNSSEC on.
  - Set `card.signed_file` and freeze the card **before** registering, and pass its hash.
- **Production root key:** put `transparency.ans.godaddy.com+c9e2f584+…` (from
  https://transparency.ans.godaddy.com/root-keys) in the prod environment's `root_keys`. It's
  pinned in `internal/verify/live_test.go`.
- **Production Finder URL:** unknown, so `FindByTag` is unavailable in prod until it's supplied.
- **Pending from Phase 2:** approve adding `CALLER_REJECTED` to the frozen `docs/schemas.md`.
