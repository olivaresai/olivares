#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ALC-04 unique leftover unique vs overlay #73: XFF trusted-proxy feat
# is NOT LANDED. Hub-safe: no overlay remasure.
# Three answers: 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-alc-04-xff-hold: FAIL — $*" >&2; exit 1; }
cannot() { say "check-alc-04-xff-hold: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ALC04_JSON:-design/alc-04-xff-hold-2026-08-20.json}"
DOC="${OLIVARES_ALC04_DOC:-design/ALC-04-XFF-HOLD-2026-08-20.md}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
command -v python3 >/dev/null || cannot "no python3"

grep -q 'Unique leftover unique vs overlay `#73`' "$DOC" \
  || fail "prepare doc lost uniqueness vs overlay #73"
grep -q 'NOT LANDED' "$DOC" \
  || fail "prepare doc lost NOT LANDED"
if grep -qiE 'overlay #73 merged|xff landed on overlay main' "$DOC"; then
  fail "prepare doc claims a land this lote does not have"
fi

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-alc-04-xff-hold: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-alc-04-xff-hold: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"JSON is not readable: {e}")

if data.get("schema") != "alc-04-xff-hold-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("alc_04_landed") is not False:
    fail("alc_04_landed must stay false")
if data.get("overlay_73_landed") is not False:
    fail("overlay #73 must stay not landed")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
if data.get("hub_writes_enterprise_ssoenforce") is not False:
    fail("hub must not write enterprise/ssoenforce")
if data.get("overlay_pr") != 73:
    fail("overlay_pr must stay 73")
print("json-ok")
PY

say "check-alc-04-xff-hold: CLEAN — overlay #73 not landed; hub-safe HOLD."
exit 0
