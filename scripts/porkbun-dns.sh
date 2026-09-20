#!/usr/bin/env bash
# Create DNS records at Porkbun through its JSON API v3, idempotently.
#
#   scripts/porkbun-dns.sh [--dry-run] --domain blacksburgbytes.club < rows
#
# rows (stdin), one per line:  host|type|ttl|answer
#   host   = label only ("ops", "_ans.ops", "" for the apex) -- never the FQDN
#   type   = TXT | TLSA | HTTPS | SVCB | A | ...   (Porkbun's enum)
#   answer = record content exactly as it should be served (no outer quotes)
#
# Env: PORKBUN_API_KEY and PORKBUN_SECRET_KEY (or PORKBUN_SECRET_API_KEY).
# Retrieves the zone first; a row whose label+type+content already exists is
# skipped. Creates only. Never updates or deletes anything. All API output is
# printed with the keys replaced by <KEY>/<SECRET>; keys are never echoed.
# Exits 2 on the first API ERROR (e.g. DOMAIN_NOT_ALLOWED: enable "API access"
# for the domain in Porkbun) so a human can act.
set -euo pipefail
dry=0; domain=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) dry=1; shift ;;
    --domain) domain=$2; shift 2 ;;
    -h|--help) sed -n '2,17p' "$0"; exit 0 ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
  esac
done
[[ -n $domain && $domain =~ ^[A-Za-z0-9.-]+$ ]] || { echo "--domain <zone> is required" >&2; exit 2; }
: "${PORKBUN_API_KEY:?PORKBUN_API_KEY is not set}"
export PORKBUN_SECRET_KEY="${PORKBUN_SECRET_KEY:-${PORKBUN_SECRET_API_KEY:-}}"
[[ -n $PORKBUN_SECRET_KEY ]] || { echo "PORKBUN_SECRET_KEY (or PORKBUN_SECRET_API_KEY) is not set" >&2; exit 2; }
command -v python3 >/dev/null || { echo "python3 is required" >&2; exit 2; }

# Rows are read here, because python's own program text arrives on stdin.
ROWS=$(cat)
ROWS=$ROWS DRY=$dry DOMAIN=$domain python3 - <<'PY'
import json, os, sys, urllib.request, urllib.error

BASE = "https://api.porkbun.com/api/json/v3"
key, secret = os.environ["PORKBUN_API_KEY"], os.environ["PORKBUN_SECRET_KEY"]
domain, dry = os.environ["DOMAIN"], os.environ["DRY"] == "1"

def redact(s):
    return str(s).replace(key, "<KEY>").replace(secret, "<SECRET>")

def call(path, body):
    req = urllib.request.Request(BASE + path, data=json.dumps({"apikey": key, "secretapikey": secret, **body}).encode(),
                                 headers={"Content-Type": "application/json"}, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, json.loads(r.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        raw = e.read().decode(errors="replace")
        try:
            return e.code, json.loads(raw)
        except ValueError:
            return e.code, {"status": "ERROR", "message": redact(raw)[:300]}

def norm(v):
    v = v.strip()
    if len(v) >= 2 and v[0] == v[-1] == '"':
        v = v[1:-1]
    return " ".join(v.split())

def label(name):
    # Porkbun returns FQDNs in retrieve; accept either form.
    n = name.rstrip(".").lower()
    if n == domain:
        return ""
    if n.endswith("." + domain):
        return n[: -len(domain) - 1]
    return n

rows = []
for ln in os.environ.get("ROWS", "").splitlines():
    if not ln.strip() or ln.lstrip().startswith("#"):
        continue
    parts = ln.split("|", 3)
    if len(parts) != 4:
        sys.exit(f"bad row (want host|type|ttl|answer): {redact(ln)}")
    host, typ, ttl, ans = (p.strip() for p in parts)
    rows.append((host.lower(), typ.upper(), int(ttl or 600), ans))

st, res = call(f"/dns/retrieve/{domain}", {})
if res.get("status") != "SUCCESS":
    print(f"retrieve failed (HTTP {st}): {redact(json.dumps(res))}")
    sys.exit(2)
have = {}
for rec in res.get("records", []):
    have.setdefault((label(rec.get("name", "")), rec.get("type", "").upper(), norm(rec.get("content", ""))), rec.get("id"))
print(f"zone {domain}: {len(res.get('records', []))} existing records")

created = skipped = 0
for host, typ, ttl, ans in rows:
    k = (host, typ, norm(ans))
    fqdn = f"{host}.{domain}" if host else domain
    if k in have:
        print(f"skip   {typ:5} {fqdn}  (exists, id {have[k]})"); skipped += 1
        continue
    if dry:
        print(f"WOULD  {typ:5} {fqdn}  ttl={ttl}  {ans}"); continue
    st, r = call(f"/dns/create/{domain}", {"name": host, "type": typ, "content": ans, "ttl": str(ttl)})
    if r.get("status") == "SUCCESS":
        print(f"create {typ:5} {fqdn}  ttl={ttl}  id {r.get('id')}"); created += 1
        have[k] = r.get("id")
    else:
        print(f"ERROR  {typ:5} {fqdn}  HTTP {st}: {redact(json.dumps(r))}")
        sys.exit(2)
print(f"{'dry-run: ' if dry else ''}created {created}, skipped {skipped}, rows {len(rows)}")
PY
