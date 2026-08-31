#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: ECS services have autoscaling; estate unapplied.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-ecs-autoscale: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-ecs-autoscale: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04AS_JSON:-design/c04-ecs-autoscale.json}"
DOC="${OLIVARES_C04AS_DOC:-design/C04-ECS-AUTOSCALE-2026-08-20.md}"
TF="${OLIVARES_C04AS_TF:-deploy/aws/modules/compute/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$TF" ] || cannot "missing compute terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$TF" || fail "compute module lost NEVER APPLIED"
if grep -qiE 'estate applied|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-ecs-autoscale/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("cp_autoscale") is not True:
    raise SystemExit("cp_autoscale must be true")
if data.get("engine_autoscale") is not True:
    raise SystemExit("engine_autoscale must be true")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

grep -q 'resource "aws_appautoscaling_target" "cp"' "$TF" \
	|| fail "control-plane autoscaling target missing"
grep -q 'resource "aws_appautoscaling_policy" "cp_cpu"' "$TF" \
	|| fail "control-plane CPU policy missing"
grep -q 'resource "aws_appautoscaling_target" "engine"' "$TF" \
	|| fail "engine autoscaling target missing"
grep -q 'resource "aws_appautoscaling_policy" "engine_cpu"' "$TF" \
	|| fail "engine CPU policy missing"
grep -q 'ECSServiceAverageCPUUtilization' "$TF" \
	|| fail "CPU target-tracking metric missing"
grep -qE 'min_capacity[[:space:]]*=[[:space:]]*var\.desired_count' "$TF" \
	|| fail "min_capacity is not the HA floor"

say "check-c04-ecs-autoscale: CLEAN — ECS autoscaling present; estate unapplied."
exit 0
