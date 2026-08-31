#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C13-03 unique leftover unique vs overlay-gated
# check-c13-03-dependson.sh: hub-safe HOLD so lint:addon-sets
# does not LOOK 2 without OLIVARES_ENT_DIR.
# Three answers: 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-03-dependson-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-03-dependson-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C1303P_JSON:-design/c13-03-dependson-prep-2026-08-20.json}"
DOC="${OLIVARES_C1303P_DOC:-design/C13-03-DEPENDSON-PREP-2026-08-20.md}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-c13-03-dependson.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs overlay-gated DependsOn check"
grep -q 'HOLD' "$DOC" || fail "prepare doc lost HOLD"
grep -q 'DependsOn not honoured' "$DOC" \
  || fail "prepare doc lost the honour pin"
if grep -qiE 'DependsOn honoured on overlay main|FIRMA A claimed' "$DOC"; then
  fail "prepare doc claims a close this lote does not have"
fi

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c13-03-dependson-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c13-03-dependson-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"JSON is not readable: {e}")

if data.get("schema") != "c13-03-dependson-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("depends_on_honoured") is not False:
    fail("depends_on_honoured must stay false")
if data.get("declared_on_legalhold_reconcile") is not True:
    fail("declared_on_legalhold_reconcile must stay true")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
sha = data.get("overlay_main_sha") or ""
if len(sha) != 40 or any(c not in "0123456789abcdef" for c in sha):
    fail("overlay_main_sha is not 40-hex")
hub = data.get("hub") or ""
if len(hub) != 40 or any(c not in "0123456789abcdef" for c in hub):
    fail("hub is not 40-hex")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)
print("json-ok")
PY

say "check-c13-03-dependson-prep: CLEAN — DependsOn HOLD; hub-safe; overlay not remasured."
exit 0
