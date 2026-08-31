#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C13-01 remasure: sold map names iso42001; overlay catalog still omits it.
# Overlay via OLIVARES_ENT_DIR. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-01-iso42001-remeasure: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-01-iso42001-remeasure: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C1301R_JSON:-design/c13-01-iso42001-remeasure.json}"
DOC="${OLIVARES_C1301R_DOC:-design/C13-01-ISO42001-REMEASURE-2026-08-20.md}"
SOLD="${OLIVARES_C1301R_SOLD:-commercial/module-slug-package.json}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$SOLD" ] || cannot "missing sold slug map"

grep -q 'HOLD' "$DOC" || fail "$DOC lost HOLD"
grep -q 'Catalog not on overlay main' "$DOC" || fail "$DOC lost catalog-absent"
if grep -qiE 'iso42001 catalog landed|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi

python3 - "$JSON" "$SOLD" <<'PY' || fail "JSON/sold-map drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c13-01-iso42001-remeasure/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("iso42001_in_catalog") is not False:
    raise SystemExit("iso42001_in_catalog must stay false")
if data.get("iso42001_in_sold_map") is not True:
    raise SystemExit("iso42001_in_sold_map must stay true")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
sold = {e["slug"] for e in json.load(open(sys.argv[2], encoding="utf-8"))["entries"]}
if "iso42001" not in sold:
    raise SystemExit("sold map lost iso42001")
PY

ENT="${OLIVARES_ENT_DIR:-}"
[ -n "$ENT" ] || cannot "OLIVARES_ENT_DIR unset"
[ -d "$ENT" ] || cannot "OLIVARES_ENT_DIR is not a directory"
CAT="$ENT/enterprise/activation/catalog.go"
[ -f "$CAT" ] || cannot "missing activation catalog"
if grep -E 'Key:[[:space:]]*"iso42001"' "$CAT" >/dev/null; then
	fail "activation catalog already lists iso42001 while remasure says absent"
fi

say "check-c13-01-iso42001-remeasure: CLEAN — sold map names iso42001; overlay catalog still omits it."
exit 0
