#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C13-01 remainder: overlay main catalog still omits iso42001.
# Overlay via OLIVARES_ENT_DIR. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-01-iso42001-catalog: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-01-iso42001-catalog: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C1301_JSON:-design/c13-01-iso42001-catalog.json}"
DOC="${OLIVARES_C1301_DOC:-design/C13-01-ISO42001-CATALOG-HOLD-2026-08-20.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"

grep -q 'HOLD' "$DOC" || fail "$DOC lost HOLD"
grep -q 'Catalog not on overlay main' "$DOC" || fail "$DOC lost catalog-absent"
if grep -qiE 'iso42001 catalog landed|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("iso42001_in_catalog") is not False:
    raise SystemExit("iso42001_in_catalog must stay false")
if data.get("panel_executed") is not False:
    raise SystemExit("panel_executed must stay false")
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

ENT="${OLIVARES_ENT_DIR:-}"
[ -n "$ENT" ] || cannot "OLIVARES_ENT_DIR unset"
[ -d "$ENT" ] || cannot "OLIVARES_ENT_DIR is not a directory"
CAT="$ENT/enterprise/activation/catalog.go"
[ -f "$CAT" ] || cannot "missing activation catalog"

if grep -E 'Key:[[:space:]]*"iso42001"' "$CAT" >/dev/null; then
	fail "activation catalog already lists iso42001 while HOLD says absent"
fi

say "check-c13-01-iso42001-catalog: CLEAN — iso42001 still off the overlay-main catalog."
exit 0
