#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-eco-02-eleven-rows.sh — ECO-02. Eleven-row work list, not closed.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-eco-02-eleven-rows: FAIL — $*" >&2; exit 1; }
cannot() { say "check-eco-02-eleven-rows: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ECO02_JSON:-design/eco-02-eleven-rows.json}"
DOC="${OLIVARES_ECO02_DOC:-design/ECO-02-ELEVEN-ROWS-2026-08-19.md}"
DIFF="${OLIVARES_ECO02_DIFF:-design/DIFERENCIA-CANON-CATALOGO-2026-08-08.md}"
MAP="${OLIVARES_ECO02_MAP:-commercial/module-slug-package.json}"
CANON="${OLIVARES_ECO02_CANON:-design/PRICING-CANON.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$DIFF" ] || cannot "missing $DIFF"
[ -f "$MAP" ] || cannot "missing $MAP"
[ -f "$CANON" ] || cannot "missing $CANON"

grep -q 'NOT EXECUTED' "$DOC" || fail "$DOC lost NOT EXECUTED"
if grep -qiE 'catalog closed|eleven rows (are )?done|executed on overlay main' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi
grep -q 'incomplete_is_worklist: true' "$CANON" || fail "canon lost incomplete_is_worklist"
grep -q 'once filas' "$DIFF" || fail "$DIFF lost the eleven-row work list"

python3 - "$JSON" "$MAP" "$DIFF" <<'PY' || fail "JSON/map/difference failed the ECO-02 contract"
import json, sys

sold = [
    "retrieval-scan",
    "compliancedepth",
    "doraregister",
    "iso42001",
    "oscalingest",
    "federation-multi-idp",
    "group-mapping",
    "durablebus",
    "connectors-conjur",
]
unsold = ["credential-minter", "circuit-breaker"]
required = sold + unsold

data = json.load(open(sys.argv[1], encoding="utf-8"))
modmap = json.load(open(sys.argv[2], encoding="utf-8"))
diff = open(sys.argv[3], encoding="utf-8").read()

if data.get("schema") != "eco-02-eleven-rows/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("executed") is not False:
    raise SystemExit("executed must stay false")
if data.get("overlay_main_addon_count") != 20:
    raise SystemExit("overlay main addon count must stay 20 until catalog lands")
if data.get("overlay_main_presets_cumulative") is not True:
    raise SystemExit("presets on overlay main are still cumulative")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)

rows = data.get("rows")
if not isinstance(rows, list):
    raise SystemExit("rows missing")
slugs = []
for row in rows:
    slug = row.get("slug")
    if slug in slugs:
        raise SystemExit("duplicate slug %s" % slug)
    slugs.append(slug)
    if row.get("status") != "OPEN_ON_OVERLAY_MAIN":
        raise SystemExit("%s status must stay OPEN_ON_OVERLAY_MAIN" % slug)
if set(slugs) != set(required):
    raise SystemExit("rows must be the eleven-slug set, not a substitute")

entries = {e.get("slug"): e.get("package") for e in modmap.get("entries", [])}
for slug in sold:
    if slug not in entries or not entries[slug]:
        raise SystemExit("hub map lost package for sold slug %s" % slug)
    if slug not in diff:
        raise SystemExit("difference doc lost sold slug %s" % slug)
for slug in unsold:
    if slug not in diff:
        raise SystemExit("difference doc lost unsold slug %s" % slug)
PY

say "check-eco-02-eleven-rows: CLEAN — eleven-row list pinned; not executed."
exit 0
