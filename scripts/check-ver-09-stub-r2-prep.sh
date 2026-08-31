#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# VER-09 stub R2 unique leftover unique vs #943 (original OPEN product
# PR; two-dot would restack evolved check-commerce-preflight.sh).
# 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-ver-09-stub-r2-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-ver-09-stub-r2-prep: COULD NOT LOOK — $*" >&2; exit 2; }

if [ -n "${OLIVARES_ROOT:-}" ]; then
  ROOT="$OLIVARES_ROOT"
else
  ROOT="$(
    cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2
    pwd
  )" || cannot "cannot resolve repository root"
fi
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_VER09P_JSON:-design/ver-09-stub-r2-prep-2026-08-20.json}"
DOC="${OLIVARES_VER09P_DOC:-design/VER-09-STUB-R2-PREP-2026-08-20.md}"
PF="${OLIVARES_VER09P_PF:-scripts/check-commerce-preflight.sh}"

for f in "$JSON" "$DOC" "$PF"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `#943`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #943"
grep -F -q 'HOLD. NOT APPLIED.' "$DOC" \
  || fail "prepare doc lost HOLD"
grep -F -q 'Does not copy `#943`' "$DOC" \
  || fail "prepare doc lost stale-branch HOLD"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|#943 landed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

pf_selftest=""
pf_rc=0
pf_selftest="$(bash "$PF" --selftest 2>&1)" || pf_rc=$?
case "$pf_rc" in
  0) ;;
  1) fail "commerce preflight executable selftest failed: $pf_selftest" ;;
  2) cannot "commerce preflight executable selftest could not run: $pf_selftest" ;;
  *) cannot "commerce preflight executable selftest returned unexpected rc=$pf_rc: $pf_selftest" ;;
esac

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-ver-09-stub-r2-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-ver-09-stub-r2-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "ver-09-stub-r2-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
for k in (
    "classify_r2_on_origin_main",
    "live_stub_case_present",
    "set_key_refusal_present",
    "stub_all_objects_refusal_present",
):
    if data.get(k) is not True:
        fail("%s must stay true" % k)
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

say "check-ver-09-stub-r2-prep: CLEAN — classify_r2 already on main; #943 HOLD; overlay remasure not in this gate."
exit 0
