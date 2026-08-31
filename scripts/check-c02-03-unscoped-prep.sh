#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C02-03 unique leftover unique vs overlay-gated
# check-c02-03-overlay-main-unscoped.sh: hub-safe HOLD so
# lint:addon-sets does not LOOK 2 without OLIVARES_ENT_DIR.
# Three answers: 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-03-unscoped-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-03-unscoped-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0203P_JSON:-design/c02-03-unscoped-prep-2026-08-20.json}"
DOC="${OLIVARES_C0203P_DOC:-design/C02-03-UNSCOPED-PREP-2026-08-20.md}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-c02-03-overlay-main-unscoped.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs overlay-gated unscoped check"
grep -q 'HOLD' "$DOC" || fail "prepare doc lost HOLD"
grep -q 'Producer not on main' "$DOC" || fail "prepare doc lost Producer not on main"
grep -q 'land_key_before_producer' "$DOC" || fail "prepare doc lost the order pin"
if grep -qiE 'producer landed on main|FIRMA A claimed' "$DOC"; then
  fail "prepare doc claims a close this lote does not have"
fi

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c02-03-unscoped-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c02-03-unscoped-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"JSON is not readable: {e}")

if data.get("schema") != "c02-03-unscoped-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("producer_on_main") is not False:
    fail("producer_on_main must stay false")
if data.get("land_key_before_producer") is not True:
    fail("land_key_before_producer must stay true")
if data.get("latest_present") is not True:
    fail("latest_present must stay true while main is unscoped")
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

say "check-c02-03-unscoped-prep: CLEAN — unscoped HOLD; hub-safe; overlay not remasured."
exit 0
