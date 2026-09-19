# Phase 1 status: Wire formats and crypto core

## What works
- **`internal/jose`**
  - `StrictJSON`: integers only, within ±2^53−1; no duplicate keys; valid UTF-8; depth and size caps.
  - JCS via gowebpki/jcs, with panics recovered.
  - ES256 compact and detached JWS through go-jose v4, with low-S normalization.
  - RFC 7638 thumbprints.
  - `Verify` / `VerifyDetached` check in this order: strict structure, then **typ first**
    (`TYP_REJECTED`), then exact header, then payload decode, then key by `kid`, then signature.
    They never panic.
- **`internal/schema`**
  - Types: Quote, Mandate, SatRegistry, Command, CommandRecord, Ack, AuditReport, BookingReceipt.
  - Strict decoding: no unknown, case-variant or missing fields; floats rejected; 48 KiB cap.
    Signed payloads must be JCS canonical.
  - `Sign*` / `Verify*` for the five signed types. On error they return a zero value, never an
    unverified object.
- **`internal/chain`**: `hash = SHA-256(prev_hash bytes || JCS(record without hash))`.
  `Append`, `Head` and `Walk` return `CHAIN_MISMATCH` for broken links.
- **`internal/errs`**: every code named in master plan sections 8–10, plus per-object parse and
  signature codes.
- **`docs/schemas.md`**: one section per object, each with a field table, example, typ, signer
  and verifier. A test decodes every example, so the doc can't drift from the code.
  **It is now frozen**, and changing it needs a human yes.

## How it was tested
- `gofmt` is clean, `go vet` passes, and `go test -race ./...` passes.
- `scripts/accept/phase-1.sh` passes. It covers:
  1. RFC 8785 sample vectors, including the section 3.2.2 example.
  2. Round trip for all 5 signed types. Flipping bits 0 and 7 of every payload and signature byte
     gives a named code. Each verifier (mandate, command) is fuzzed for 30 s, about 4M executions
     each, with no panic, no unnamed error, and no modified token verifying.
  3. Every signed type presented as every other gives `TYP_REJECTED`. So does a real agent-card
     header (with `jku`).
  4. `100` is accepted, while `100.0`, `1.2e3`, `12e2` and `1E2` are rejected at decode.
     This holds on a signed mandate too.
  5. Equal command sequences give equal chain heads, and dropping or reordering a command changes
     the head. The hash definition is pinned by a test.
  6. The RFC 7638 section 3.1 vector matches, and our EC thumbprint equals ans-sdk-go
     `pop.Signer.JKT()` for the same key.
- A separate review subagent reviewed the diff. The findings are in `docs/plans/phase-1.md`
  under "Review", and all are fixed. The high-severity one was ECDSA high-S malleability.

## Decisions to confirm (need a human yes to change later)
- **New typ:** `overpass-receipt+jws` for BookingReceipt. The prompt's list didn't have it,
  but rule 3 requires every JWS to have its own typ.
- **Unsigned objects:** Quote, Ack and CommandRecord, since the master plan gives them no typ.
  **Risk:** an unsigned Ack means a station could forge Acks to hide dropped commands.
  Consider a spacecraft-signed Ack in Phase 5.
- **Quote `accepts[].amount`:** an integer in atomic units. Real x402 uses a string.
- **SatRegistry payload:** `{iss, iat, satellites[]}`.
- **AuditReport:** carries both `chain_head` (Ops) and `station_chain_head`.
- **Extra codes:** `CHAIN_MISMATCH`, `ACK_MISSING`, `COMMAND_REJECTED:{signature,norad_id,counter}`,
  and per-object `*_PARSE_ERROR` / `*_REJECTED:signature`. The master plan describes these checks
  but doesn't name the codes.

## Stubbed or deferred
- Agent card body (Phase 2). Only the detached JWS form and its codes are fixed here.
- `book_pass` steps 3–12, DPoP (via ans-sdk-go `pop`), and storage are Phase 4. HTTP is out of scope.
- Known library deviation: gowebpki/jcs accepts some lone UTF-16 surrogate escapes that
  RFC 8785 says to reject. This is not exploitable here, because signed payloads must be
  byte-equal to their JCS form and valid UTF-8. Don't use `Transform` output as an identity
  without that byte check.

## Needs a human
- Nothing blocks Phase 2. The human gates H1–H6 are unchanged.
