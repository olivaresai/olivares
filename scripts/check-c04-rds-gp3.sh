#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: RDS gp3 IOPS 3000 and throughput 125. Unapplied.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-rds-gp3: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-rds-gp3: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04GP3_JSON:-design/c04-rds-gp3.json}"
DOC="${OLIVARES_C04GP3_DOC:-design/C04-RDS-GP3-2026-08-20.md}"
TF="${OLIVARES_C04GP3_TF:-deploy/aws/modules/data/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$TF" ] || cannot "missing data terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$TF" || fail "data module lost NEVER APPLIED"
grep -q 'baseline of 3000' "$DOC" || fail "$DOC lost gp3-baseline"
if grep -qiE 'estate applied|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

python3 - "$JSON" "$TF" <<'PY' || fail "JSON flags or terraform drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-rds-gp3/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("storage_type") != "gp3":
    raise SystemExit("storage_type must stay gp3")
if data.get("iops") != 3000:
    raise SystemExit("iops must stay 3000")
if data.get("storage_throughput") != 125:
    raise SystemExit("storage_throughput must stay 125")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)

tf = open(sys.argv[2], encoding="utf-8").read()
if not re.search(r'storage_type\s*=\s*"gp3"', tf):
    raise SystemExit("storage_type lost gp3")
if not re.search(r"(?m)^[ \t]*iops\s*=\s*3000\b", tf):
    raise SystemExit("iops lost 3000")
if not re.search(r"storage_throughput\s*=\s*125\b", tf):
    raise SystemExit("storage_throughput lost 125")
if re.search(r"(?m)^[ \t]*iops\s*=\s*0\b", tf):
    raise SystemExit("iops 0 is invalid for gp3")
PY

say "check-c04-rds-gp3: CLEAN — gp3 IOPS 3000 / throughput 125; estate unapplied."
exit 0
