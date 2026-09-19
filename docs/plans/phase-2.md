# Phase 2 plan: Identity surface and A2A envelope

Goal: each agent serves a signed, truthful set of well-known files and answers A2A JSON-RPC.

## Files
- internal/config: add `public_url` (default https://host[:port]), `identity{key_file,chain_file}`,
  `card{version,display_name,description,org_name,org_url,agent_id,receipt_file,tl_agent_url,
  dns_aid,tier2(default true),skills[{id,name,description,tags,examples}]}`. Env overrides unchanged.
- internal/wellknown: `Build(Config) (Files, error)`; `Files` = path -> {bytes, content type}.
  Tier 1: agent-card.json, ans/trust-card.json, /health, / (HTML). Tier 2 (if on): jwks.json,
  did.json, ard.json == ai-catalog.json (same bytes), robots.txt, llms.txt.
  `Security` input: the schemes the server actually mounts (below); the card is built from it.
  Card: webmesh-spec 2.1 fields, no default-valued properties (A2A 8.4 strips defaults before JCS),
  signed detached ES256 over JCS(card without signatures), typ agent-card+jws, kid thumbprint,
  jku = own trust card. Served bytes are JCS canonical, so raw hash == JCS hash.
  `VerifyCard(card, fetchTrustCard)` for tests/Phase 3: jku -> trust card -> key by kid -> verify.
- internal/a2a: JSON-RPC 2.0 at POST /. Methods `SendMessage` (A2A 1.0, PascalCase, ROLE_*,
  parts `{text}`/`{data}`) and `message/send` (0.3, `kind` parts, role user/agent); reply in the
  caller's dialect. Data part `{"skill": id, ...args}` dispatches to a registered handler with
  per-skill guards; text-only -> text part listing skills. Errors: -32700/-32600/-32601/-32602,
  -32004 unsupported (skill without handler), guard rejections as -32602 with errs code in data.
  Body cap 1 MiB; batch requests rejected; never 500.
- internal/a2a guards: `DPoP` wraps ans-sdk-go `pop.Middleware` (rule 5; keys = scitt.KeyStore
  from config trust_roots C2SP strings; empty store = fail closed) and rewrites its 401 into
  errs `CALLER_REJECTED`. `Mandate` skill guard runs schema.VerifyMandate (book_pass steps 1-2).
- cmd/agent: mounts per role: station = DPoP on POST / + mandate on book_pass; authority =
  DPoP; ops/auditor/spacecraft = none (noAuth). The same `Security` value feeds card and mux.
- cmd/cardhash: fetch URL (or --file), print SHA-256 of raw bytes and of JCS form.

## Tests, one per acceptance criterion
1. TestRequiredFields: every JSON file parses and has webmesh-spec 6 fields we can truthfully serve
   (exceptions listed below).
2. TestCardVerifiesViaJKU: verify using only the key found in the trust card fetched from jku.
3. TestCardTamper: change each top-level field (and a nested skill tag) -> verification fails.
4. TestStationCardMatchesMounted (httptest, real routes): no noAuth; schemes == mounted guards;
   unauthenticated POST / -> 401 CALLER_REJECTED; book_pass without mandate -> MANDATE_PARSE_ERROR.
5. TestSendMessage (1.0 and 0.3) returns skills; unknown method -> -32601 with HTTP 200.
6. TestTier2Off: config tier2 false -> tier-2 paths absent, agent starts, /health ok.

## Deviations / exceptions (rule 6 wins over webmesh-spec 6)
- No MCP extension, WIMSE, botProfile, mcp.json, agentfacts, signature-agent-card aliases.
- Authority declares DPoP only, not mandate: it issues mandates, it does not require them.
- agentId, transparencyReceipt, tl links, ans_registered, dns_aid_svcb omitted until configured.
- securitySchemes use webmesh's `{type, scheme}` shape; mandate scheme type is `mandate`.
## Dependencies: none new (tests import ans-sdk-go's examples/a2a-no-mtls/demokit, same module).
## Risks
- metaDataHash definition unknown (raw vs JCS): cards served as JCS so both agree; cardhash prints both.
- Supplier's exact data-part shape was not captured; ours is documented in README.

## Review
Separate review subagent. (H) VerifyCard pinned jku to the card's own url, so a self-consistent
forgery verified: now `Expect{Host, LeafSHA256}` from ANS; JWK must equal its x5c leaf.
(M) public_url: no path/query/userinfo, host must equal `host`. (M) invalid JSON-RPC got 204:
validate before notification. (M) DPoP success path untested: demokit test. (L) part shapes,
fractional ids, EscapedPath pin, trust-card size cap, chain linkage/expiry, stop() on all
exits, accept scripts assert each test PASSed and require nc, unnamed errors logged.
