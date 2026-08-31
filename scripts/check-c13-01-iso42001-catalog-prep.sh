#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C13-01 unique leftover unique vs overlay-gated
# check-c13-01-iso42001-catalog.sh: hub-safe HOLD so lint:addon-sets
# does not LOOK 2 without OLIVARES_ENT_DIR.
# Three answers: 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-01-iso42001-catalog-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-01-iso42001-catalog-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C1301P_JSON:-design/c13-01-iso42001-catalog-prep-2026-08-20.json}"
DOC="${OLIVARES_C1301P_DOC:-design/C13-01-ISO42001-CATALOG-PREP-2026-08-20.md}"
SOLD="${OLIVARES_C1301P_SOLD:-commercial/module-slug-package.json}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$SOLD" ] || cannot "missing $SOLD"
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-c13-01-iso42001-catalog.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs overlay-gated catalog check"
grep -q 'HOLD' "$DOC" || fail "prepare doc lost HOLD"
grep -q 'Catalog not on overlay main' "$DOC" \
  || fail "prepare doc lost catalog-absent"
if grep -qiE 'iso42001 catalog landed|FIRMA A claimed' "$DOC"; then
  fail "prepare doc claims a close this lote does not have"
fi

python3 - "$JSON" "$SOLD" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c13-01-iso42001-catalog-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c13-01-iso42001-catalog-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
    sold = json.load(open(sys.argv[2], encoding="utf-8"))
except Exception as e:
    cannot(f"JSON is not readable: {e}")

if data.get("schema") != "c13-01-iso42001-catalog-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("iso42001_in_catalog") is not False:
    fail("iso42001_in_catalog must stay false")
if data.get("iso42001_in_sold_map") is not True:
    fail("iso42001_in_sold_map must stay true")
if data.get("panel_executed") is not False:
    fail("panel_executed must stay false")
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
try:
    slugs = {e["slug"] for e in sold["entries"]}
except Exception as e:
    cannot(f"sold map is not readable: {e}")
if "iso42001" not in slugs:
    fail("sold map lost iso42001")
print("json-ok")
PY

say "check-c13-01-iso42001-catalog-prep: CLEAN — catalog HOLD; hub-safe; overlay not remasured."
exit 0
