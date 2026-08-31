#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: ALB HSTS max-age named. Unapplied.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-alb-hsts: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-alb-hsts: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04HSTS_JSON:-design/c04-alb-hsts.json}"
DOC="${OLIVARES_C04HSTS_DOC:-design/C04-ALB-HSTS-2026-08-20.md}"
TF="${OLIVARES_C04HSTS_TF:-deploy/aws/modules/ingress/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$TF" ] || cannot "missing ingress terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$TF" || fail "ingress module lost NEVER APPLIED"
if grep -qiE 'estate applied|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

python3 - "$JSON" "$TF" <<'PY' || fail "JSON flags or terraform drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-alb-hsts/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("hsts") != "max-age=31536000":
    raise SystemExit("hsts drifted")
if data.get("include_subdomains") is not False:
    raise SystemExit("include_subdomains must stay false")
if data.get("preload") is not False:
    raise SystemExit("preload must stay false")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)

def resource_block(tf, typ, name):
    m = re.search(r'resource\s+"%s"\s+"%s"\s*\{' % (re.escape(typ), re.escape(name)), tf)
    if not m:
        return None
    i = m.end()
    depth = 1
    while i < len(tf) and depth:
        if tf[i] == "{":
            depth += 1
        elif tf[i] == "}":
            depth -= 1
        i += 1
    return tf[m.start():i]

tf = open(sys.argv[2], encoding="utf-8").read()
alb = resource_block(tf, "aws_lb", "alb")
if alb is None:
    raise SystemExit("aws_lb.alb missing")
attr = "routing_http_response_strict_transport_security_header_value"
if attr in alb:
    raise SystemExit("HSTS header sits on aws_lb.alb; it belongs on the listener")
lis = resource_block(tf, "aws_lb_listener", "https")
if lis is None:
    raise SystemExit("aws_lb_listener.https missing")
if attr not in lis:
    raise SystemExit("HSTS header attribute missing on the listener")
mval = re.search(attr + r'\s*=\s*"([^"]*)"', lis)
if not mval:
    raise SystemExit("HSTS header value missing")
val = mval.group(1)
if val != "max-age=31536000":
    raise SystemExit("HSTS value drifted to %r" % val)
PY

say "check-c04-alb-hsts: CLEAN — HSTS max-age named; estate unapplied."
exit 0
