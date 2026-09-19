# Phase 3 plan: Verification

Goal: one package decides whether a peer may be talked to, in both directions, and explains why.

## What the SDK gives (read in module cache, v0.1.18) and what it lacks
- verify.StandardDNSResolver.FindPreferredBadge (_ans-badge TXT), HTTPTransparencyLogClient.FetchBadge,
  DANEVerifier + StandardDANEResolver(WithDANEServer), CertIdentityFromX509; scitt.HTTPClient
  (FetchReceipt/FetchStatusToken/FetchRootKeys), VerifyReceipt, VerifyStatusToken, MatchesServerCert,
  MatchesIdentityCert, NewKeyStore(C2SP), HeaderSupplier; pop.Middleware/Signer/AttachIdentity.
- Gap: models.Badge reads only V1 `serverCert`; the local stack serves V2 `serverCerts[]`, so
  ServerVerifier would always report a fingerprint mismatch there. Cert, identity and metadata
  checks therefore use the TL-signed status token (ValidServerCerts, ValidIdentityCerts,
  MetadataHashes), which both V1 (prod) and V2 (local) logs serve. The badge gives registration
  and status only.
- No tag search in the SDK: FindByTag uses the Finder API (ans/spec/api-spec-finder-v1.yaml,
  POST {finder}/search, query.filter.tags); prod Finder URL unknown -> unset, returns named error.

## Files
- internal/config: `environments{name:{registry_url,log_url,log_public_url,finder_url,dns_server,
  root_keys[],insecure_http}}`; peers gain `env`, `dial`. http URLs only when insecure_http.
- internal/verify: `Verifier.VerifyPeer(ctx, host) Result` (checks: registered, badge, cert_chain,
  tlsa, scitt_receipt, status_token, card_hash, card_signature; verdict pass|warn|fail|skip, reason,
  overall fail closed). env.go (per-env HTTP client rewriting log_public_url->log_url, DNS resolver,
  key store), token.go (`FreshToken`, `Policy{MaxAge}.Decide`, `Keeper`), outbound.go (pop.Signer +
  scitt.HeaderSupplier -> Attach(req)), discover.go (FindByTag), events via an emit func -> bus.
- internal/wellknown: Expect.LeafSHA256 -> LeafSHA256s (set, from status token identity certs).
- internal/webmesh: MCP client (initialize, notifications/initialized, tools/list, tools/call);
  picks the verify tool from tools/list (live name is `verify_agent{agent_host,environment}`) and
  fills the required host property; discover tool absent live -> ErrNoTool.
- scripts/register.sh, scripts/dns-records.sh (ans-cli wrappers, dry-run default, long flag),
  scripts/local-ans.sh (start/stop the reference stack from ../ans), scripts/local-register.sh
  (register our station on the LOCAL RA only; refuses any non-localhost RA).

## Tests, one per acceptance criterion
1. TestLocalStack (ANS_LOCAL=1, run by phase-3.sh): registered station with RA-issued certs
   passes; unregistered ops fails with reasons.
2. TestLiveWebmesh (ANS_LIVE=1): full Result for agent.webmesh.ai, saved to testdata fixture.
3. TestForgedJKUNoRequest: counting transport proves zero requests to the forged host.
4. TestTokenPolicyTable: the four rows from the prompt.
5. TestDPoPReplay + existing reject test: replayed proof -> CALLER_REJECTED.
6. register.sh without the flag: exits 0, prints plan, runs nothing (PATH has a fake ans-cli
   that fails if invoked).
7. TestEveryCheckEmits: one bus event per check plus a summary event.
## Dependencies: none new.
## Risks
- DANE on the local DNS has no DNSSEC; TLSA without DNSSEC is reported, not trusted blindly.
- Webmesh MCP shape may change; the client reads tools/list each time.

## Review
Separate review subagent. (H) VerifyPeer panicked on a malformed host (also reachable from Finder
URNs): recover armed first, peer URL/host validated, URNs host-checked. (M) Keeper out-of-order
fetch could revive a revoked agent: revocation marker, newest-iat-only, stale ACTIVE refused.
(M) FindByTag verified hits in prod: now in the searched env, no name matching for Finder hits.
(M) transport per fetch leaked conns: one per run, closed. (M) card_hash with no metaDataHash now
fails; DANESkipped reason fixed. (L) OK() fail-closed, kid selection, rewrite/onLog origin match,
log/Finder/MCP redirects refused, MCP id check + session mutex + SSE fix + exact tool names,
register.sh rejects newlines, phase-3.sh makes .run.
