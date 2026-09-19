# Overpass wire formats

**Frozen after Phase 1.** Changing this file needs a human to say yes (CLAUDE.md).
Code: `internal/schema` (types), `internal/jose` (JCS, JWS, thumbprints), `internal/chain`.
`internal/schema` tests decode every example below, so the doc cannot drift from the code.

## Common rules

**Encoding.** JSON, UTF-8. Every number is an integer in ±(2^53−1): money in cents,
times in epoch seconds, angles in whole degrees. Floats are rejected in any spelling
(`100.0`, `1e2`), and so are `-0` and leading zeros. Integers are capped at 2^53−1
because JCS serializes numbers as IEEE-754 doubles.

**Strict decoding.** A decoder accepts exactly one JSON value of at most 48 KiB.
- Unknown keys, duplicate keys and case variants (`Quote_ID`) are rejected.
- Every field is required unless the table marks it optional.
- A field is present exactly when it is non-empty; an empty optional field is omitted, not sent as `""`.
- Failures return the object's `*_PARSE_ERROR` code.

**Canonical form.** JCS (RFC 8785): keys sorted by UTF-16 code units, no whitespace.
A signed payload must be byte-for-byte JCS canonical. A non-canonical payload is a
parse error, even if its signature is valid.

**JWS.** ES256 over EC P-256 keys only. Compact serialization, except agent cards,
which use a detached payload (`header..signature`). The protected header is exactly:

| Field | Value |
| --- | --- |
| `alg` | `ES256` |
| `kid` | RFC 7638 SHA-256 thumbprint of the signing key, base64url |
| `typ` | one of the values below |
| `jku` | agent cards only: URL of the signer's trust card |

No other header parameter is accepted (`crit`, `b64`, `jwk`, `x5u` and `x5c` are all rejected).
Base64url is strict: no padding, no whitespace, no non-zero spare bits.
Signatures must be low-S (`s` ≤ n/2). Signers normalize to low-S, and verifiers reject high-S
with the object's signature code. ECDSA would otherwise accept both (r, s) and (r, n−s), so a
relay could mint a second valid token for the same payload. Together these rules give each
signed token exactly one valid encoding.

**Verification order.** A verifier checks the token in this order and stops at the first failure:
1. Size (at most 68 KiB; payloads at most 48 KiB) and structure: three segments, strict base64url, header is strict JSON.
2. `typ`: a mismatch is `TYP_REJECTED`. This is read and compared before any other header rule, so a foreign JWS (such as an agent card with `jku`) is always `TYP_REJECTED`.
3. The header is exactly as above, `alg` is ES256, the signature is 64 bytes, and `s` is low.
4. Payload decode and validation: the object's parse code.
5. The `kid` names a trusted key, and the signature verifies: the object's signature code.

Payload decoding comes before the signature because `book_pass` step 1 (parse) precedes
step 2 (signature), per master plan 8.5. A verifier never returns an unverified object:
on any error the value is zero. Verifier entry points recover from panics.

| typ | Object | Signer | Parse code | Signature code |
| --- | --- | --- | --- | --- |
| `overpass-mandate+jws` | Mandate | Authority | `MANDATE_PARSE_ERROR` | `MANDATE_REJECTED:signature` |
| `overpass-satreg+jws` | SatRegistry | Registry signer | `SATREG_PARSE_ERROR` | `SATREG_REJECTED:signature` |
| `overpass-cmd+jws` | Command | Ops | `COMMAND_PARSE_ERROR` | `COMMAND_REJECTED:signature` |
| `overpass-audit+jws` | AuditReport | Auditor | `AUDIT_PARSE_ERROR` | `AUDIT_REJECTED:signature` |
| `overpass-receipt+jws` | BookingReceipt | Station | `RECEIPT_PARSE_ERROR` | `RECEIPT_REJECTED:signature` |
| `agent-card+jws` | Agent card (Phase 2) | Each agent | `CARD_PARSE_ERROR` | `CARD_REJECTED:signature` |

Common field shapes:
- **ID:** 1–128 characters of `[A-Za-z0-9._:-]`.
- **Name:** an ANS name or host, 1–253 printable ASCII characters with no spaces.
- **Hex64:** a lowercase hex SHA-256.
- **Class:** a command class, `[a-z0-9_-]{1,32}`.
- **NORAD ID:** 1–999999999.

## Quote

Unsigned. A station returns it from `get_pass_quote`; the mandate binds it by `quote_id`.
Ops decodes it strictly (`QUOTE_PARSE_ERROR`).

| Field | Type | Rule |
| --- | --- | --- |
| `quote_id` | ID | |
| `station` | Name | the quoting station's ANS name |
| `norad_id` | int | NORAD ID |
| `aos`, `los` | int | epoch s, `aos` > 0, `los` > `aos` |
| `max_elevation_deg` | int | 0–90 |
| `mode` | string | `uplink` or `downlink` |
| `amount_cents` | int | ≥ 0, price for the pass |
| `accepts` | array | 1–8 entries of `{scheme, network, payTo, asset, amount}`. The first four are Names; `amount` is an int ≥ 0 in the asset's smallest unit. This is x402-shaped, but `amount` is an integer rather than x402's string. The same `payTo` must appear in the signed agent card. |
| `exp` | int | epoch s, quote expiry |

```json quote
{
  "quote_id": "q-1",
  "station": "gs-blacksburg.overpass.example",
  "norad_id": 25544,
  "aos": 1760000000,
  "los": 1760000480,
  "max_elevation_deg": 62,
  "mode": "uplink",
  "amount_cents": 1200,
  "accepts": [
    {"scheme": "exact", "network": "base-sepolia", "payTo": "0xabc", "asset": "USDC", "amount": 12000000}
  ],
  "exp": 1759999940
}
```

## Mandate

- **typ:** `overpass-mandate+jws`
- **Signer:** Authority, with a key listed in its trust card.
- **Verifiers:** the station (`book_pass` steps 1–2 here, steps 3–12 in Phase 4) and the auditor.

A missing `scope` or `jkt` fails decoding, which is `MANDATE_PARSE_ERROR`.

| Field | Type | Rule |
| --- | --- | --- |
| `mandate_id` | ID | |
| `iss` | Name | the authority's ANS name |
| `sub` | Name | the Ops agent's ANS name |
| `aud` | Name | the station's ANS name |
| `quote_id` | ID | the quote this mandate pays for |
| `scope` | string | `pass:<mode>:<norad_id>`, for example `pass:uplink:25544` |
| `command_classes` | array of Class | 1–32 entries, no duplicates |
| `max_amount_cents` | int | ≥ 0 |
| `nbf`, `exp` | int | epoch s, equal to the quote's AOS and LOS; `exp` > `nbf` |
| `jkt` | string | 43 characters of base64url: the RFC 7638 thumbprint of Ops's DPoP key. It equals ans-sdk-go `pop.Signer.JKT()`. |
| `nonce` | string | 16–128 characters of base64url; single use |

```json mandate
{
  "mandate_id": "m-1",
  "iss": "authority.overpass.example",
  "sub": "ops.overpass.example",
  "aud": "gs-blacksburg.overpass.example",
  "quote_id": "q-1",
  "scope": "pass:uplink:25544",
  "command_classes": ["telemetry", "attitude"],
  "max_amount_cents": 1500,
  "nbf": 1760000000,
  "exp": 1760000480,
  "jkt": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
  "nonce": "bm9uY2Utbm9uY2Utbm9uY2U"
}
```

## SatRegistry

- **typ:** `overpass-satreg+jws`
- **Signer:** the registry signer (the mission authority's key in the demo).
- **Verifiers:** every station, which loads it at startup and pins its signer key.

It stands in for license filings. ANS proves who an agent is; this file says which satellites that agent may command.

| Field | Type | Rule |
| --- | --- | --- |
| `iss` | Name | the signer |
| `iat` | int | epoch s |
| `satellites` | array | 1–256 entries, each NORAD ID once |
| `satellites[].norad_id` | int | NORAD ID |
| `satellites[].authority_ans_name` | Name | the only authority allowed to issue mandates for it |
| `satellites[].ops_ans_names` | array of Name | 1–16 Ops agents allowed as mandate `sub` |

```json satreg
{
  "iss": "authority.overpass.example",
  "iat": 1759913600,
  "satellites": [
    {"norad_id": 25544, "authority_ans_name": "authority.overpass.example", "ops_ans_names": ["ops.overpass.example"]}
  ]
}
```

## Command

- **typ:** `overpass-cmd+jws`
- **Signer:** Ops.
- **Verifiers:**
  - The spacecraft accepts a command only if all of these hold:
    - The signature verifies under the Ops key.
    - `typ` is the command type.
    - `norad_id` is its own (else `COMMAND_REJECTED:norad_id`).
    - `counter` is greater than the last accepted counter (else `COMMAND_REJECTED:counter`).
  - The station checks `typ`, that `class` is in the mandate's `command_classes` (else `CLASS_REJECTED`), and that `mandate_id` matches the booking.
  - The station never reads or changes `body`.

| Field | Type | Rule |
| --- | --- | --- |
| `norad_id` | int | NORAD ID |
| `counter` | int | ≥ 1, strictly increasing per satellite |
| `mandate_id` | ID | |
| `class` | Class | |
| `body` | object | any JSON object up to 16 KiB; integers only, like everything else |
| `issued_at` | int | epoch s |

```json command
{
  "norad_id": 25544,
  "counter": 7,
  "mandate_id": "m-1",
  "class": "telemetry",
  "body": {"op": "dump", "since": 1760000000},
  "issued_at": 1760000010
}
```

## CommandRecord

Unsigned. Ops and the station each compute it for every relayed command, and the auditor compares the two chain heads.

```
hash = SHA-256( prev_hash as 32 raw bytes || JCS(record without "hash") )
```

- The genesis `prev_hash` is 64 zeros.
- `cmd_sha256` is the lowercase hex SHA-256 of the command's compact JWS string, as relayed.
- Records hold only shared fields and no timestamps, so equal heads prove neither side added, removed or reordered a command.
- A broken link, a wrong hash or a non-increasing counter is `CHAIN_MISMATCH`.
- A chained command with no Ack is `ACK_MISSING`.

| Field | Type | Rule |
| --- | --- | --- |
| `counter` | int | ≥ 1, strictly increasing along the chain |
| `mandate_id` | ID | |
| `class` | Class | |
| `cmd_sha256` | Hex64 | |
| `prev_hash` | Hex64 | the previous record's `hash`, or genesis |
| `hash` | Hex64 | as above |

```json record
{
  "counter": 7,
  "mandate_id": "m-1",
  "class": "telemetry",
  "cmd_sha256": "abababababababababababababababababababababababababababababababab",
  "prev_hash": "abababababababababababababababababababababababababababababababab",
  "hash": "abababababababababababababababababababababababababababababababab"
}
```

## Ack

Unsigned. The spacecraft returns it and the station relays it (`ACK_PARSE_ERROR`).
Because it is unsigned, a station could forge an Ack. Phase 5 must account for this
(see the Phase 1 status report).

| Field | Type | Rule |
| --- | --- | --- |
| `counter` | int | ≥ 1, the command's counter |
| `result` | string | `accepted` or `rejected` |
| `telemetry_sha256` | Hex64 | required when accepted, absent when rejected |
| `reason` | Name | rejection code, required when rejected, absent when accepted |

```json ack
{"counter": 7, "result": "accepted", "telemetry_sha256": "abababababababababababababababababababababababababababababababab"}
```

## AuditReport

- **typ:** `overpass-audit+jws`
- **Signer:** Auditor.
- **Verifiers:** anyone, including the dashboard and the trust index.

| Field | Type | Rule |
| --- | --- | --- |
| `pass_id` | ID | the booking ID |
| `iss` | Name | the auditor |
| `station` | Name | the audited station |
| `iat` | int | epoch s |
| `checks` | array | never `null`; ≤ 64 entries of `{name: ID, result: pass\|fail, code?: string}` |
| `canaries` | array | never `null`; ≤ 64 entries of `{probe: ID, result: rejected\|accepted, code?: string}`. An `accepted` canary is `CANARY_ACCEPTED`. |
| `chain_head` | Hex64 | Ops's chain head |
| `station_chain_head` | Hex64 | the station's chain head |
| `missing_acks` | int | ≥ 0 |
| `verdict` | string | `pass` or `fail` |

```json audit
{
  "pass_id": "b-1",
  "iss": "auditor.overpass.example",
  "station": "gs-rogue.overpass.example",
  "iat": 1760000540,
  "checks": [{"name": "identity", "result": "pass"}],
  "canaries": [{"probe": "flipped_signature", "result": "accepted", "code": "CANARY_ACCEPTED"}],
  "chain_head": "abababababababababababababababababababababababababababababababab",
  "station_chain_head": "abababababababababababababababababababababababababababababababab",
  "missing_acks": 0,
  "verdict": "fail"
}
```

## BookingReceipt

- **typ:** `overpass-receipt+jws`. This typ is not in the Phase 1 prompt's list. It was added because CLAUDE.md rule 3 gives every JWS its own typ.
- **Signer:** the station, which returns it from `book_pass`.
- **Verifier:** the auditor.

| Field | Type | Rule |
| --- | --- | --- |
| `booking_id` | ID | |
| `station` | Name | the signer |
| `mandate_id`, `quote_id` | ID | |
| `norad_id` | int | NORAD ID |
| `nbf`, `exp` | int | the booked window, epoch s; `exp` > `nbf` |
| `amount_cents` | int | ≥ 0 |
| `iat` | int | epoch s |

```json receipt
{
  "booking_id": "b-1",
  "station": "gs-blacksburg.overpass.example",
  "mandate_id": "m-1",
  "quote_id": "q-1",
  "norad_id": 25544,
  "nbf": 1760000000,
  "exp": 1760000480,
  "amount_cents": 1200,
  "iat": 1759999700
}
```

## Agent card signature

- **typ:** `agent-card+jws`
- **Form:** a detached JWS over the card bytes, whose protected header carries `jku` (the signer's trust card URL). This matches `docs/webmesh-spec.md` section 2.1, but uses ES256 instead of EdDSA (decision in the prompts doc, section 1).
- **Phase 2** defines the card body. Phase 1 fixes only the JWS form and codes.

## Rejection codes

Every code lives in `internal/errs`.

| Group | Codes |
| --- | --- |
| Any JWS | `TYP_REJECTED` |
| `book_pass` (in check order) | `MANDATE_PARSE_ERROR`, `MANDATE_REJECTED:signature`, `MANDATE_REJECTED:not_owner`, `MANDATE_REJECTED:audience`, `MANDATE_REJECTED:quote`, `MANDATE_REJECTED:scope`, `MANDATE_REJECTED:amount`, `MANDATE_REJECTED:window`, `DPOP_REJECTED:key`, `DPOP_REJECTED:replay`, `BOOKING_REJECTED:overlap`, `MANDATE_REJECTED:consumed` |
| Session and audit | `WINDOW_CLOSED`, `CLASS_REJECTED`, `CANARY_ACCEPTED`, `CHAIN_MISMATCH`, `ACK_MISSING` |
| Trust and cards | `POLICY_REFUSED:tier`, `CARD_REJECTED:jku`, `CARD_CLAIM_MISMATCH` |
| Spacecraft | `COMMAND_REJECTED:signature`, `COMMAND_REJECTED:norad_id`, `COMMAND_REJECTED:counter` |
| Per object | `QUOTE_PARSE_ERROR`, `COMMAND_PARSE_ERROR`, `SATREG_PARSE_ERROR`, `SATREG_REJECTED:signature`, `AUDIT_PARSE_ERROR`, `AUDIT_REJECTED:signature`, `RECEIPT_PARSE_ERROR`, `RECEIPT_REJECTED:signature`, `CARD_PARSE_ERROR`, `CARD_REJECTED:signature`, `ACK_PARSE_ERROR`, `RECORD_PARSE_ERROR` |
