#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C05-14 unique leftover unique vs #1023 (original OPEN product PR;
# 013 is already plan_enforcement on origin/main). 0 CLEAN · 1 finding
# · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-14-effect-key-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-14-effect-key-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0514P_JSON:-design/c05-14-effect-key-prep-2026-08-20.json}"
DOC="${OLIVARES_C0514P_DOC:-design/C05-14-EFFECT-KEY-PREP-2026-08-20.md}"
M004="${OLIVARES_C0514P_004:-cloud/control-plane/migrations/004_suspension_log.up.sql}"
M013="${OLIVARES_C0514P_013:-cloud/control-plane/migrations/013_plan_enforcement.up.sql}"
COLLIDE="${OLIVARES_C0514P_COLLIDE:-cloud/control-plane/migrations/013_suspension_log_effect_key.up.sql}"
STMT="${OLIVARES_C0514P_STMT:-cloud/control-plane/internal/store/tenantstatements.go}"

for f in "$JSON" "$DOC" "$M004" "$M013" "$STMT"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `#1023`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1023"
grep -F -q 'Unique leftover unique vs `hub-comercio/c05-14-suspension-log`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original branch"
grep -F -q 'HOLD. NOT APPLIED.' "$DOC" \
  || fail "prepare doc lost HOLD"
grep -F -q 'Does not modify 004' "$DOC" \
  || fail "prepare doc lost 004 HOLD"
grep -F -q '013 is already plan_enforcement' "$DOC" \
  || fail "prepare doc lost 013 collision remasure"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|effect_key landed in 004' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

if grep -q 'effect_key' "$M004"; then
  fail "004 gained effect_key — this lote does not modify existing migrations"
fi
if [ -e "$COLLIDE" ]; then
  fail "013_suspension_log_effect_key landed — this HOLD lote does not apply #1023 as-is"
fi
grep -q 'max_seats' "$M013" \
  || fail "013 is no longer plan_enforcement (GRANT max_seats gone)"
if grep -q 'effect_key' "$M013"; then
  fail "013 plan_enforcement was overwritten with effect_key"
fi
if grep -q 'polar_event_id, effect_key' "$STMT"; then
  fail "LogSuspension INSERT gained effect_key — this HOLD lote does not apply C05-14"
fi
grep -q '(tenant_id, action, reason, polar_event_id)' "$STMT" \
  || fail "LogSuspension INSERT drifted"

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c05-14-effect-key-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c05-14-effect-key-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c05-14-effect-key-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("effect_key_present") is not False:
    fail("effect_key_present must stay false")
if data.get("migration_004_untouched") is not True:
    fail("migration_004_untouched must stay true")
if data.get("slot_013_is_plan_enforcement") is not True:
    fail("slot_013_is_plan_enforcement must stay true")
if data.get("remainder_applied") is not False:
    fail("remainder_applied must stay false")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
hub = data.get("hub") or ""
if len(hub) != 40 or any(c not in "0123456789abcdef" for c in hub):
    fail("hub is not 40-hex")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)
print("json-ok")
PY

say "check-c05-14-effect-key-prep: CLEAN — 004 untouched; 013 is plan_enforcement; effect_key HOLD; overlay remasure not in this gate."
exit 0
