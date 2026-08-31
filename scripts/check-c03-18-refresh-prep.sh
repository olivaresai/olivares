#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C03-18 unique leftover unique vs #1019 (original OPEN product PR;
# no original check on origin/main). 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-18-refresh-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-18-refresh-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0318P_JSON:-design/c03-18-refresh-prep-2026-08-20.json}"
DOC="${OLIVARES_C0318P_DOC:-design/C03-18-REFRESH-PREP-2026-08-20.md}"
IDX="${OLIVARES_C0318P_IDX:-commercial/license-worker/src/index.ts}"
REF="${OLIVARES_C0318P_REF:-commercial/license-worker/src/license/refresh.ts}"

for f in "$JSON" "$DOC" "$IDX"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `#1019`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1019"
grep -F -q 'Unique leftover unique vs `hub-comercio/c03-18-refresh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original branch"
grep -F -q 'HOLD. NOT APPLIED.' "$DOC" \
  || fail "prepare doc lost HOLD"
grep -F -q 'Refresh serial' "$DOC" \
  || fail "prepare doc lost refresh-serial HOLD"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|refresh serial landed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

if [ -e "$REF" ]; then
  fail "license/refresh.ts landed — this HOLD lote does not apply C03-18"
fi

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c03-18-refresh-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c03-18-refresh-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c03-18-refresh-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("refresh_serial_landed") is not False:
    fail("refresh_serial_landed must stay false")
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

say "check-c03-18-refresh-prep: CLEAN — refresh serial HOLD; overlay remasure not in this gate."
exit 0
