#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Unique leftover unique vs check-json-decoders.sh (named on main,
# CHECK not in lint:addon-sets) and unique leftover unique vs #1461.
# 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-json-decoders-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-json-decoders-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_JSONDECP_JSON:-design/json-decoders-prep-2026-08-20.json}"
DOC="${OLIVARES_JSONDECP_DOC:-design/JSON-DECODERS-PREP-2026-08-20.md}"
ORIG="${OLIVARES_JSONDECP_ORIG:-scripts/check-json-decoders.sh}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$ORIG" ] || cannot "missing $ORIG"
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-json-decoders.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original json-decoders check"
grep -F -q 'Unique leftover unique vs `#1461`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1461"
grep -F -q 'Does not write core/' "$DOC" \
  || fail "prepare doc lost core HOLD"
grep -F -q 'Does not collapse the copies' "$DOC" \
  || fail "prepare doc lost copies HOLD"
if grep -qiE 'core rewritten|copies collapsed|FIRMA A claimed' "$DOC"; then
  fail "prepare doc claims a close this lote does not have"
fi

grep -q 'json\.NewDecoder' "$ORIG" \
  || fail "original check-json-decoders.sh lost behavioural roster"
grep -q '\.More()' "$ORIG" \
  || fail "original check-json-decoders.sh lost More() rejection"
grep -q 'io\.EOF' "$ORIG" \
  || fail "original check-json-decoders.sh lost io.EOF rejection"

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-json-decoders-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-json-decoders-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "json-decoders-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("decoder_count") != 31:
    fail("decoder_count drifted from live 31")
if data.get("all_refuse_trailing") is not True:
    fail("all_refuse_trailing must stay true")
if data.get("copies_collapsed") is not False:
    fail("copies_collapsed must stay false")
if data.get("core_rewritten") is not False:
    fail("core_rewritten must stay false")
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

say "check-json-decoders-prep: CLEAN — 31 decoders pinned; copies not collapsed; core not rewritten."
exit 0
