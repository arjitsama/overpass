# Plan: go-live DNS via the Porkbun API (2026-09-20)

Goal: publish the ACME and ANS records for ops, gs-blacksburg, authority through
the Porkbun API instead of the UI, then run the gated ans-cli verifications.

## Files
- `scripts/porkbun-dns.sh` (new): bash + python3 (stdlib json/urllib; no new
  dependency). Reads `PORKBUN_API_KEY` and `PORKBUN_SECRET_KEY` (fallback
  `PORKBUN_SECRET_API_KEY`, the name `.env` uses). Rows on stdin as
  `host|type|ttl|answer` (host = label only, blank = apex). `--dry-run` default
  off; `--domain` required.
- `scripts/dns-records.sh`: `_ans` TXT emits `version=v<ver>` (the RA's exact
  value); print HTTPS `1 . alpn=h2` alongside SVCB.
- `docs/status/go-live.md`: timestamps for every action.

## Interface (Porkbun API v3, from the OpenAPI spec at /api/json/v3/spec)
- `POST https://api.porkbun.com/api/json/v3/dns/retrieve/{domain}` -> records[]
  {id,name,type,content,ttl,prio,notes}; `name` may be FQDN or label, normalize.
- `POST .../dns/create/{domain}` body {apikey,secretapikey,name,type,content,ttl}.
  type enum includes TXT, TLSA, HTTPS, SVCB. Errors: status ERROR + code
  (`INVALID_API_KEYS_001`, `DOMAIN_NOT_ALLOWED` = API access toggle).
- `POST .../ping` validates credentials.

## Behaviour
- Idempotent: retrieve once; skip a row when a record with the same label,
  type and normalized content exists; otherwise create. Never updates, never
  deletes. Every API response is printed with both keys replaced by `<KEY>` /
  `<SECRET>`; the script never echoes its inputs' env.
- Exit non-zero on the first ERROR so the human can flip the Porkbun toggle.

## Tests / acceptance
1. `bash -n`, `scripts/secret-scan.sh` (script holds no keys).
2. `--dry-run` against the live zone prints one line per row (create/skip).
3. Real run creates 7 records; re-run reports 7 skips (idempotent).
4. `dig @1.1.1.1` / `@8.8.8.8` PASS table for each record (<= 5 min retry).

## Risks / assumptions
- Porkbun may reject SVCB content; the row is optional and reported.
- TXT content is stored unquoted; compare after stripping quotes/whitespace.
- Minimum TTL is account-defined (typically 600): we use 600.

## Review
- Bug: the python program arrived on stdin, so `sys.stdin.read()` saw no rows
  (dry-run reported "rows 0"). Fixed by reading rows in bash into `ROWS` first.
- macOS bash 3.2 has no `declare -A`; the cert-fetch helper was moved to a
  python file. Not part of the script itself.
- Checked: keys only reach the API body; every printed response goes through
  `redact()`; the script creates only (no update/delete); exits 2 on ERROR.
- Porkbun accepted TLSA, HTTPS and SVCB content verbatim.

## Test
- `bash -n`, `scripts/secret-scan.sh` clean.
- Dry-run listed 6 WOULD rows; real run created 6 + SVCB; re-run skipped 6.
- dig PASS at 1.1.1.1 and 8.8.8.8 for all 17 records; AD flag set.
- verify-dns: ops, gs-blacksburg, authority all ACTIVE.
