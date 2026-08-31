#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c03-28-public-prefix.sh — C03-28. Public enterprise/ prefix probe.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-28-public-prefix: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-28-public-prefix: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0328_JSON:-design/c03-28-public-prefix.json}"
DOC="${OLIVARES_C0328_DOC:-design/C03-28-PUBLIC-PREFIX-2026-08-20.md}"
BACKLOG="${OLIVARES_C0328_BACKLOG:-design/BACKLOG-COMPLETITUD-2026-08-16.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$BACKLOG" ] || cannot "missing $BACKLOG"

grep -q 'NOT EXECUTED' "$DOC" || fail "$DOC lost NOT EXECUTED"
if grep -qiE 'private bucket listed|public listing found|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi
grep -q 'C03-28' "$BACKLOG" || fail "$BACKLOG lost the C03-28 row"
grep -q 'NXDOMAIN' "$DOC" || fail "$DOC lost the registry NXDOMAIN"

python3 - "$JSON" "$DOC" <<'PY' || fail "JSON/doc failed the C03-28 contract"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
doc = open(sys.argv[2], encoding="utf-8").read()
if data.get("schema") != "c03-28-public-prefix/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("executed") is not False:
    raise SystemExit("executed must stay false")
if data.get("listed_private_r2") is not False:
    raise SystemExit("listed_private_r2 must stay false")
if data.get("public_listing_found") is not False:
    raise SystemExit("public_listing_found must stay false")
if data.get("r2_registry_dns") != "NXDOMAIN":
    raise SystemExit("r2_registry_dns must stay NXDOMAIN")
if data.get("worker_host") != "licenses.olivares.ai":
    raise SystemExit("worker_host pin drifted")
if data.get("worker_health_status") != 200:
    raise SystemExit("health pin must stay 200")
if data.get("worker_enterprise_prefix_status") != 404:
    raise SystemExit("enterprise/ prefix pin must stay 404")
if data.get("worker_enterprise_prefix_body") != "Not Found":
    raise SystemExit("enterprise/ body pin drifted")
if data.get("worker_tarball_path_status") != 404:
    raise SystemExit("tarball path pin must stay 404")
if data.get("download_no_token_status") != 403:
    raise SystemExit("download without token must stay 403")
if data.get("download_no_token_body") != "Forbidden":
    raise SystemExit("download body pin drifted")
if data.get("crl_status") != 200:
    raise SystemExit("crl pin must stay 200")
if data.get("updates_enterprise_status") != 401:
    raise SystemExit("updates/enterprise pin must stay 401")
if data.get("updates_is_directory_listing") is not False:
    raise SystemExit("updates must not be a directory listing")
if data.get("ghcr_unauthenticated_status") != 401:
    raise SystemExit("ghcr pin must stay 401")
if "404" not in doc or "NXDOMAIN" not in doc or "403" not in doc:
    raise SystemExit("doc lost the measured codes")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
sha = data.get("hub_sha")
if not isinstance(sha, str) or not re.fullmatch(r"[0-9a-f]{40}", sha):
    raise SystemExit("hub_sha is not a 40-hex object id")
PY

if [ "${OLIVARES_C0328_LIVE:-}" != "1" ]; then
	say "check-c03-28-public-prefix: NOTICE — live HTTPS remasure skipped"
	say "check-c03-28-public-prefix: CLEAN — no public enterprise/ listing; private R2 unlisted."
	exit 0
fi

python3 - "$JSON" <<'PY' || fail "live HTTPS remasure diverged from the pin"
import json, socket, sys, urllib.error, urllib.request

data = json.load(open(sys.argv[1], encoding="utf-8"))

def status_and_body(url, timeout=15):
    req = urllib.request.Request(url, method="GET", headers={"User-Agent": "olivares-c03-28-probe"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read(64)
            return resp.status, body.decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        body = e.read(64)
        return e.code, body.decode("utf-8", "replace")
    except Exception as e:
        raise SystemExit("live GET %s failed: %s" % (url, e))

try:
    socket.getaddrinfo("r2-registry.olivares.ai", 443)
except socket.gaierror:
    pass
else:
    raise SystemExit("r2-registry.olivares.ai now resolves")

st, body = status_and_body("https://licenses.olivares.ai/health")
if st != 200:
    raise SystemExit("live health %s != 200" % st)
st, body = status_and_body("https://licenses.olivares.ai/enterprise/")
if st != 404:
    raise SystemExit("live enterprise/ %s != 404" % st)
if "Not Found" not in body:
    raise SystemExit("live enterprise/ body is not Not Found")
st, body = status_and_body("https://licenses.olivares.ai/download?os=linux&arch=amd64")
if st != 403:
    raise SystemExit("live download-without-token %s != 403" % st)
PY

say "check-c03-28-public-prefix: CLEAN — no public enterprise/ listing; private R2 unlisted."
exit 0
