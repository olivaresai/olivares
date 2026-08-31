#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: ALB xff_header_processing_mode named append.
# Unapplied. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-xff-mode: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-xff-mode: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04XFFM_JSON:-design/c04-xff-mode.json}"
DOC="${OLIVARES_C04XFFM_DOC:-design/C04-XFF-MODE-2026-08-20.md}"
TF="${OLIVARES_C04XFFM_TF:-deploy/aws/modules/ingress/main.tf}"

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
if data.get("schema") != "c04-xff-mode/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("xff_header_processing_mode") != "append":
    raise SystemExit("xff_header_processing_mode must stay append")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)

tf = open(sys.argv[2], encoding="utf-8").read()
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
if not re.search(r'xff_header_processing_mode\s*=\s*"append"', block):
    raise SystemExit("ALB xff_header_processing_mode lost append")
if re.search(r'xff_header_processing_mode\s*=\s*"(preserve|remove)"', block):
    raise SystemExit("ALB xff_header_processing_mode is not append")
PY

say "check-c04-xff-mode: CLEAN — XFF mode named append; estate unapplied."
exit 0
