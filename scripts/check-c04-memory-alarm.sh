#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: ECS MemoryUtilization alarm exists; estate unapplied.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-memory-alarm: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-memory-alarm: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04M_JSON:-design/c04-memory-alarm.json}"
DOC="${OLIVARES_C04M_DOC:-design/C04-MEMORY-ALARM-2026-08-20.md}"
TF="${OLIVARES_C04M_TF:-deploy/aws/modules/observability/main.tf}"

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
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("memory_alarm") is not True:
    raise SystemExit("memory_alarm must be true")
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

grep -q 'resource "aws_cloudwatch_metric_alarm" "memory"' "$TF" \
	|| fail "memory alarm resource missing"
grep -q 'metric_name.*=.*"MemoryUtilization"' "$TF" \
	|| fail "MemoryUtilization missing"
grep -q 'metric_name.*=.*"CPUUtilization"' "$TF" \
	|| fail "CPU alarm lost"

say "check-c04-memory-alarm: CLEAN — memory alarm present; estate unapplied."
exit 0
