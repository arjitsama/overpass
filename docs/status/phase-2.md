# Phase 2 status: Identity surface and A2A envelope

## What works
- **`internal/wellknown`**: `Build` produces every file an agent serves from one config.
  - Tier 1:
    - The signed A2A agent card.
    - The ANS trust card: EC P-256 JWK with `x5c`; `agentId` and `transparencyReceipt` only when configured.
    - `/health`.
    - An accessible HTML page at `/`: `lang`, headings, labeled definition list, visible focus, dark mode.
  - Tier 2, which `card.tier2: false` turns off: `jwks.json`, `did.json` (`did:web`), `ard.json`,
    `ai-catalog.json` (the same bytes as `ard.json`), `robots.txt` (`Agentmap:`) and `llms.txt`.
  - Output is deterministic: no timestamps, and cards are served as JCS bytes.
- **Agent card**
  - Fields follow webmesh-spec 2.1, with values from A2A 1.0 (`protocolBinding: JSONRPC`).
  - It contains no default-valued properties, per A2A 8.4.
  - It is signed as a detached JWS: ES256, `typ agent-card+jws`, `kid` = RFC 7638 thumbprint,
    `jku` = the agent's own trust card.
  - `securitySchemes` and `securityRequirements` are **generated from the guards the server
    actually mounts**:
    - station: `ansDPoP` on every call, plus `overpassMandate` on `book_pass`
    - authority: `ansDPoP`
    - others: `noAuth`
- **`wellknown.VerifyCard(raw, Expect{Host, LeafSHA256}, fetch)`**
  - It pins `jku` and the card `url` to the **ANS-verified host** before any fetch.
  - It trusts a trust-card key only when the key equals its own `x5c` leaf, and when that leaf
    matches the attested fingerprint (if one is given).
  - Phase 3 supplies `Expect` from the registry and log.
- **`internal/a2a`**: JSON-RPC 2.0 at `POST /`.
  - `SendMessage` (A2A 1.0) and `message/send` (0.3), each answered in the caller's dialect.
  - A data part `{"skill": id, ...}` dispatches to that skill, after its guards run. A text
    message gets the skill list.
  - Errors follow A2A spec 9.5 (-32700, -32600, -32601, -32602, and -32004 for skills whose
    handler lands later). Rejections carry `google.rpc.ErrorInfo.reason` = the errs code.
  - There is no 500 path: panics are recovered into -32603.
- **Guards**
  - `DPoPGuard` wraps ans-sdk-go `pop.Middleware` unchanged (rule 5) and rewrites its 401 into
    `CALLER_REJECTED`.
  - `MandateGuard` runs `book_pass` steps 1–2 (`schema.VerifyMandate`).
- **`cmd/cardhash [-k] <url> | -file <path>`**: prints `raw_sha256` and `jcs_sha256`.
  For our cards they are equal.

## How it was tested
- `gofmt` is clean, `go vet` passes, and `go test -race ./...` passes.
- `scripts/accept/phase-2.sh` passes and now requires each named test to report PASS:
  1. Required fields per webmesh-spec 6, and no default values.
  2. Verification through the key at `jku` only, both in a unit test and end to end over HTTPS
     against a running agent.
  3. Tampering with any field (including a nested skill tag) fails verification.
  4. The station card has no `noAuth`, and its schemes equal the mounted guards. Also:
     - Unauthenticated → 401 `CALLER_REJECTED`.
     - A proven caller with real SCITT receipt, status token and DPoP credentials (ans-sdk-go
       demokit) gets through. The handler sees the proven ANS name, and `book_pass` still
       demands a mandate.
  5. `SendMessage` and `message/send` return the skill list, and an unknown method gives
     -32601 with HTTP 200.
  6. Tier 2 off: those paths return 404, and the agent starts and serves `/health`.
  - A live run of both local agents checks that every file is served, the station refuses
    unauthenticated calls, ops answers with its skills, and `cardhash` raw equals JCS.
- The Phase 0 and Phase 1 acceptance scripts still pass. Phase 1 was rerun with 30 s fuzzing.
- A separate review subagent reviewed the diff. Its 11 findings are listed in
  `docs/plans/phase-2.md`, and all are fixed. The high-severity one: `jku` was pinned to the
  card's own `url`, so a self-consistent forgery verified. There is now a regression test.

## Deviations from the prompt and webmesh-spec (rule 6 wins)
- **Authority declares DPoP only, not "DPoP plus mandate".** It issues mandates; it doesn't require one.
- **Not served or claimed:** MCP extension, `mcp.json`, WIMSE, `botProfile`, agentfacts, the
  signature-agent-card and http-message-signatures aliases, ARD `trustManifest`, `x5c`-free JWKs
  in the trust card.
- **Omitted until configured:** `agentId`, `transparencyReceipt`, `tl_badge`,
  `ans_registered`/`trust_index` (only once registered), `dns_aid_svcb` (only once `card.dns_aid`
  is set after gate H3).
- **Shapes a generic A2A 1.0 client may not expect:**
  - Security schemes use webmesh's `{type, scheme}` shape: `http`/`DPoP`, plus a custom
    `type: mandate`.
  - The card keeps top-level `url` and `protocolVersion`, like webmesh.
  - The JWS `typ` is `agent-card+jws`, per the prompt, not the A2A SHOULD of `JOSE`.

## Needs a human
- **Approve adding `CALLER_REJECTED`** to the codes table in the frozen `docs/schemas.md`.
  The code exists in `internal/errs` and is used by the DPoP guard, but I haven't edited the
  frozen doc.
- **Unknowns behind interfaces:**
  - The `metaDataHash` rule (raw vs JCS) isn't documented. Cards are served in JCS form so both
    agree; confirm with GoDaddy before registering (gate H2).
  - supplier.webmesh.ai's exact data-part shape wasn't captured, so ours
    (`{"skill": id, ...}`) is documented in the README.
- **Fail-closed until Phase 3/4 wiring:**
  - `trust_roots` (log root keys, C2SP strings) must be configured before any station or
    authority accepts a call. With none, every call gets `CALLER_REJECTED`.
  - Authority keys for `MandateGuard` aren't wired yet (Phase 3 and 4), so every mandate is
    currently `MANDATE_REJECTED:signature`.
- **Freeze cards before registering (master plan 5A):** finish skill ids, tags, `payTo` and
  schemes first. Any later card change needs re-registration.
