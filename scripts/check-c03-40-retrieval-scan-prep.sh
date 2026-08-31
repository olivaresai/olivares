#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C03-40 unique leftover unique vs #1074: two options, none chosen.
# Hub-safe: no overlay remasure so lint:addon-sets does not LOOK 2.
# Three answers: 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-40-retrieval-scan-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-40-retrieval-scan-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0340_JSON:-design/c03-40-retrieval-scan-2026-08-20.json}"
DOC="${OLIVARES_C0340_DOC:-design/C03-40-RETRIEVAL-SCAN-2026-08-20.md}"
DEC="${OLIVARES_C0340_DEC:-design/DECISIONES-RESUELTAS-CRITERIO-2026-08-08.md}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$DEC" ] || cannot "missing $DEC"
command -v python3 >/dev/null || cannot "no python3"

grep -q 'Unique leftover unique vs `#1074`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1074"
grep -q 'NO ELEGIDO. NO APLICADO.' "$DOC" \
  || fail "prepare doc lost NO ELEGIDO. NO APLICADO."
grep -q 'addongate-runtime' "$DOC" || fail "prepare doc lost option A"
grep -q 'declare-included-in-airs-artifact' "$DOC" || fail "prepare doc lost option B"
grep -q 'retrieval-scan' "$DEC" || fail "$DEC lost the owner question this lote presents"
if grep -qiE 'elegimos A|applied option|gateamos retrieval' "$DOC"; then
  fail "prepare doc claims a choice this lote does not have"
fi

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c03-40-retrieval-scan-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c03-40-retrieval-scan-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"JSON is not readable: {e}")

want = ["addongate-runtime", "declare-included-in-airs-artifact"]
if data.get("options") != want:
    fail("options %r, want %r" % (data.get("options"), want))
if data.get("chosen") is not None:
    fail("chosen %r, want null" % (data.get("chosen"),))
if data.get("applied") is not False:
    fail("applied must be false")
if data.get("fran_decides") is not True:
    fail("fran_decides must be true")
if data.get("always_on_enterprise_binary") is not False:
    fail("always_on_enterprise_binary must be false (premise retracted)")
if data.get("wired_only_under_airs_tag") is not True:
    fail("wired_only_under_airs_tag must be true")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s is %r, want UNKNOWN" % (k, data.get(k)))
print("json-ok")
PY

say "check-c03-40-retrieval-scan-prep: CLEAN — two options presented; none chosen; hub-safe."
exit 0
