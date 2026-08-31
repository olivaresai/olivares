#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: RDS Performance Insights on the existing rds CMK.
# Estate unapplied. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-rds-pi: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-rds-pi: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04PI_JSON:-design/c04-rds-pi.json}"
DOC="${OLIVARES_C04PI_DOC:-design/C04-RDS-PI-2026-08-20.md}"
TF="${OLIVARES_C04PI_TF:-deploy/aws/modules/data/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$TF" ] || cannot "missing data terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$TF" || fail "data module lost NEVER APPLIED"
grep -q 'same CMK' "$DOC" || fail "$DOC lost same-CMK reuse"
if grep -qiE 'estate applied|FIRMA A claimed|F1 restore closed' "$DOC"; then
	fail "$DOC claims an apply or restore this lote does not have"
fi

python3 - "$JSON" "$TF" <<'PY' || fail "JSON flags or terraform drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-rds-pi/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("performance_insights") is not True:
    raise SystemExit("performance_insights must be true")
if data.get("retention_days") != 7:
    raise SystemExit("retention_days must stay 7")
if data.get("reuses_rds_cmk") is not True:
    raise SystemExit("reuses_rds_cmk must stay true")
if data.get("second_kms_key") is not False:
    raise SystemExit("second_kms_key must stay false")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)

tf = open(sys.argv[2], encoding="utf-8").read()
keys = re.findall(r'resource\s+"aws_kms_key"\s+"([^"]+)"', tf)
if keys != ["rds"]:
    raise SystemExit("expected exactly one aws_kms_key named rds, got %r" % keys)
if not re.search(
    r"performance_insights_enabled\s*=\s*true", tf
):
    raise SystemExit("performance_insights_enabled lost true")
if not re.search(
    r"performance_insights_retention_period\s*=\s*7", tf
):
    raise SystemExit("retention period lost 7")
if not re.search(
    r"performance_insights_kms_key_id\s*=\s*aws_kms_key\.rds\.arn", tf
):
    raise SystemExit("Insights is not bound to the rds CMK")
PY

say "check-c04-rds-pi: CLEAN — Insights on the rds CMK; estate unapplied."
exit 0
