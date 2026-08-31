#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: CloudWatch dashboard exists; estate unapplied.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-cw-dashboard: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-cw-dashboard: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04D_JSON:-design/c04-cw-dashboard.json}"
DOC="${OLIVARES_C04D_DOC:-design/C04-CW-DASHBOARD-2026-08-20.md}"
TF="${OLIVARES_C04D_TF:-deploy/aws/modules/observability/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$TF" ] || cannot "missing observability terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$TF" || fail "observability module lost NEVER APPLIED"
if grep -qiE 'estate applied|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-cw-dashboard/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("dashboard") is not True:
    raise SystemExit("dashboard must be true")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

grep -q 'resource "aws_cloudwatch_dashboard" "cp"' "$TF" \
	|| fail "CloudWatch dashboard resource missing"
grep -q 'dashboard_name' "$TF" || fail "dashboard_name missing"
grep -q 'CPUUtilization' "$TF" || fail "CPU widget/alarm metric missing"
grep -q 'MemoryUtilization' "$TF" || fail "memory widget/alarm metric missing"
grep -q 'resource "aws_cloudwatch_metric_alarm" "memory"' "$TF" \
	|| fail "memory alarm lost"

say "check-c04-cw-dashboard: CLEAN — dashboard present; estate unapplied."
exit 0
