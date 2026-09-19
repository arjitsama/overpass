# Phase 1 plan: Wire formats and crypto core

Goal: every signed object is a Go type with sign, verify, canonicalize; formats frozen in docs/schemas.md.

## Files
- internal/jose: `Canonicalize(v) ([]byte, error)` (json.Marshal then JCS);
  `StrictJSON(b, max) error` (size cap, integers only within ±2^53-1, no duplicate keys, one value);
  `Sign(typ, payload, key, jku)` compact; `SignDetached(...)`; `Thumbprint(pub)` (RFC 7638, b64url);
  `Verify(token, Profile, keys, decode)`/`VerifyDetached(...)`. Profile = {Typ, ParseCode, SigCode}.
  Verify order (matches master plan 8.5 steps 1-2): size cap -> 3 segments, strict base64url
  (no lenient trailing bits) -> protected header strict {alg,typ,kid[,jku]} -> **typ first**
  (TYP_REJECTED) -> alg ES256 -> decode(payload) hook (ParseCode) -> kid picks key by thumbprint
  -> go-jose signature check (SigCode). Every entry point recovers panics into ParseCode.
- internal/schema: Quote, Mandate, SatRegistry, Command, CommandRecord, Ack, AuditReport,
  BookingReceipt. `Decode<T>` strict + `Validate()` (required fields, enums, lengths, hex/b64url).
  Signed ones get `Sign<T>(v, key)` / `Verify<T>(token, keys)`. Payload must equal JCS(payload).
- internal/chain: `Chain.Append(counter, mandateID, class, cmdSHA256)`, `Head()`, `Walk(records)`.
  hash = SHA-256(prev_hash_bytes || JCS(record without hash)); genesis prev = 32 zero bytes;
  counters strictly increase; mismatch -> CHAIN_MISMATCH.
- internal/errs: all codes in master plan sections 8, 9, 10, plus per-object PARSE/signature codes.
- docs/schemas.md, scripts/accept/phase-1.sh. internal/ansdeps stays (SDK used only in tests).

## Dependencies (rule 7)
- github.com/go-jose/go-jose/v4 v4.1.5: maintained, ES256, compact + detached (ParseDetached),
  custom typ via SignerOptions.WithType, algorithm allow-list at parse, RFC 7638 Thumbprint.
  ans-sdk-go's own JWS/thumbprint code is unexported (pop/jws.go), so it cannot be reused.
- github.com/gowebpki/jcs v1.0.1: packaged RFC 8785 reference code by the RFC's author; ships
  the RFC sample vectors. No other Go JCS is as close to the spec.

## Tests, one per acceptance criterion
1. TestJCSVectors: every file in testdata/jcs (RFC 8785 samples, incl. section 3.2.2 values.json).
2. TestRoundTrip<T> per signed type; TestTamper flips every byte of payload and signature
   segments -> named code, never panic; FuzzVerifyMandate, FuzzVerifyCommand run 30 s each.
3. TestMandateAsCommand: mandate JWS into VerifyCommand -> TYP_REJECTED (and reverse).
4. TestFloatRejected: `100` decodes, `100.0`, `1e2`, `1E2` fail with the object's PARSE code.
5. TestChainDeterministic: two chains, same commands -> equal heads; drop or swap -> different.
6. TestThumbprintRFC7638: RFC 7638 section 3.1 RSA key -> NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs;
   plus EC thumbprint equals ans-sdk-go pop.Signer.JKT() for the same key (DPoP jkt must match).

## Decisions and risks
- Added typ `overpass-receipt+jws` for BookingReceipt (rule 3: every JWS has its own typ).
- Quote, Ack, CommandRecord are unsigned (master plan gives them no typ). Ack unsigned means a
  station could forge acks; flag for Phase 5.
- x402 `accepts[].amount` is an integer (atomic units), per master plan and rule 3; real x402
  uses strings. Flag in status.
- Integers limited to ±(2^53-1): JCS serializes numbers as IEEE doubles.
- SatRegistry payload is {iss, iat, satellites[]}; AuditReport carries both chain heads.
- Section 8's step 1 (parse) precedes step 2 (signature), so payload decode runs before the
  signature check; the strict decoder is total and size-capped.

## Review
Separate review subagent. Fixed: (H) ECDSA high-S malleability let a second token verify:
Sign emits low-S, Verify rejects high-S (TestHighSRejected, fuzz seed). (M) jku/extra header
fields were parse errors before typ: typ now read first (TestTypBeforeHeaderRules, card-as-X).
(M) Audit arrays could be null; Ack could carry both fields. (L) Sign had no size cap and the
token cap was below a max payload: 48 KiB payload / 68 KiB token. (L) Chain docs, tamper
test also flips bit 7. Noted: gowebpki/jcs accepts some lone surrogates (safe: byte-equality).
