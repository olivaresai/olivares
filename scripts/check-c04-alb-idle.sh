#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: ALB idle_timeout 60 and desync defensive.
# Estate unapplied. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-alb-idle: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-alb-idle: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04IDLE_JSON:-design/c04-alb-idle.json}"
DOC="${OLIVARES_C04IDLE_DOC:-design/C04-ALB-IDLE-2026-08-20.md}"
TF="${OLIVARES_C04IDLE_TF:-deploy/aws/modules/ingress/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$TF" ] || cannot "missing ingress terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$TF" || fail "ingress module lost NEVER APPLIED"
grep -q 'HTTP idle' "$DOC" || fail "$DOC lost HTTP-idle"
if grep -qiE 'estate applied|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

python3 - "$JSON" "$TF" <<'PY' || fail "JSON flags or terraform drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-alb-idle/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("idle_timeout_seconds") != 60:
    raise SystemExit("idle_timeout_seconds must stay 60")
if data.get("desync_mitigation_mode") != "defensive":
    raise SystemExit("desync_mitigation_mode must stay defensive")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)

tf = open(sys.argv[2], encoding="utf-8").read()
# isolate the ALB resource so NLB tcp_idle_timeout cannot satisfy this pin
m = re.search(r'resource\s+"aws_lb"\s+"alb"\s*\{', tf)
if not m:
    raise SystemExit("aws_lb.alb missing")
i = m.end()
depth = 1
while i < len(tf) and depth:
    if tf[i] == "{":
        depth += 1
    elif tf[i] == "}":
        depth -= 1
    i += 1
block = tf[m.start():i]
if not re.search(r"idle_timeout\s*=\s*60\b", block):
    raise SystemExit("ALB idle_timeout lost 60")
if not re.search(r'desync_mitigation_mode\s*=\s*"defensive"', block):
    raise SystemExit("ALB desync_mitigation_mode lost defensive")
if re.search(r"idle_timeout\s*=\s*6000\b", block):
    raise SystemExit("ALB copied the NLB 6000 budget")
PY

say "check-c04-alb-idle: CLEAN — ALB HTTP idle 60, desync defensive; unapplied."
exit 0
