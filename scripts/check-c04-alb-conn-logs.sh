#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: ALB connection logs to a sibling bucket. Unapplied.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-alb-conn-logs: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-alb-conn-logs: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04CL_JSON:-design/c04-alb-conn-logs.json}"
DOC="${OLIVARES_C04CL_DOC:-design/C04-ALB-CONN-LOGS-2026-08-20.md}"
ING="${OLIVARES_C04CL_ING:-deploy/aws/modules/ingress/main.tf}"
DATA="${OLIVARES_C04CL_DATA:-deploy/aws/modules/data/main.tf}"
ROOTTF="${OLIVARES_C04CL_ROOT:-deploy/aws/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$ING" ] || cannot "missing ingress terraform"
[ -f "$DATA" ] || cannot "missing data terraform"
[ -f "$ROOTTF" ] || cannot "missing root terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$ING" || fail "ingress module lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$DATA" || fail "data module lost NEVER APPLIED"
if grep -qiE 'estate applied|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

python3 - "$JSON" "$ING" "$DATA" "$ROOTTF" <<'PY' || fail "JSON flags or terraform drifted"
import json, re, sys

def block_after(src, pat):
    m = re.search(pat, src)
    if not m:
        return None
    i = m.end()
    depth = 1
    while i < len(src) and depth:
        if src[i] == "{":
            depth += 1
        elif src[i] == "}":
            depth -= 1
        i += 1
    return src[m.start():i]

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-alb-conn-logs/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("connection_logs") is not True:
    raise SystemExit("connection_logs must stay true")
if data.get("prefix") != "alb-conn":
    raise SystemExit("prefix drifted")
if data.get("destination") != "alb_conn":
    raise SystemExit("destination drifted")
if data.get("delivery_principal") != "logdelivery.elasticloadbalancing.amazonaws.com":
    raise SystemExit("delivery_principal drifted")
if data.get("sse") != "AES256":
    raise SystemExit("sse drifted")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)

ing = open(sys.argv[2], encoding="utf-8").read()
dat = open(sys.argv[3], encoding="utf-8").read()
root = open(sys.argv[4], encoding="utf-8").read()

alb = block_after(ing, r'resource\s+"aws_lb"\s+"alb"\s*\{')
if not alb:
    raise SystemExit("aws_lb.alb missing")
conn = block_after(alb, r'connection_logs\s*\{')
if not conn:
    raise SystemExit("ALB connection_logs block missing")
if not re.search(r'prefix\s*=\s*"alb-conn"', conn):
    raise SystemExit("connection_logs prefix lost alb-conn")
if re.search(r'prefix\s*=\s*"AWSLogs"', conn):
    raise SystemExit("user prefix must not be AWSLogs")
if "connection_logs_bucket" not in conn:
    raise SystemExit("connection_logs bucket is not the dedicated variable")

bkt = block_after(dat, r'resource\s+"aws_s3_bucket"\s+"alb_conn"\s*\{')
if not bkt:
    raise SystemExit("aws_s3_bucket.alb_conn missing")
pol = block_after(dat, r'resource\s+"aws_s3_bucket_policy"\s+"alb_conn"\s*\{')
if not pol:
    raise SystemExit("alb_conn bucket policy missing")
if "logdelivery.elasticloadbalancing.amazonaws.com" not in pol:
    raise SystemExit("log-delivery principal lost")
# ACOTADO a `pol` el 2026-08-20. Decia `... in pol or ... in dat`, es decir,
# prohibia `aws_elb_service_account` en TODO modules/data/main.tf. Su sujeto es
# la politica de connection logs, y el propio an internal design note (not shipped)
# dice que los ACCESS logs de #1225 usan la cuenta regional legitimamente y que
# este lote "does not restack #1225". Mientras #1225 no estaba, `dat` y `pol`
# daban lo mismo y la diferencia no se veia; al aterrizar #1225 el gate enrojecio
# por contenido correcto que no es suyo.
if "aws_elb_service_account" in pol:
    raise SystemExit("legacy regional ELB account leaked into connection logs")
if "s3:PutObject" not in pol:
    raise SystemExit("PutObject lost from the connection-log policy")
if "sse_algorithm" in dat and "AES256" not in dat:
    raise SystemExit("alb_conn lost AES256")

if "alb_conn_bucket_id" not in root:
    raise SystemExit("root does not pass alb_conn_bucket_id")
if "connection_logs_bucket" not in root:
    raise SystemExit("root does not wire connection_logs_bucket")
if re.search(
    r"connection_logs_bucket\s*=\s*module\.data\.plane_bucket_id", root
):
    raise SystemExit("connection logs restack the plane bucket")
PY

say "check-c04-alb-conn-logs: CLEAN — connection logs to sibling; estate unapplied."
exit 0
