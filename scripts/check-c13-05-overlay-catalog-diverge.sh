#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C13-05 remainder: overlay-main catalog still diverges from the sold map.
# Overlay via OLIVARES_ENT_DIR. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-05-overlay-catalog-diverge: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-05-overlay-catalog-diverge: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C1305_JSON:-design/c13-05-overlay-catalog-diverge.json}"
DOC="${OLIVARES_C1305_DOC:-design/C13-05-OVERLAY-CATALOG-DIVERGE-2026-08-20.md}"
SOLD="${OLIVARES_C1305_SOLD:-commercial/module-slug-package.json}"
HOLD="${OLIVARES_C1305_HOLD:-design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$SOLD" ] || cannot "missing sold slug map"
[ -f "$HOLD" ] || cannot "missing C13-07 HOLD file"

grep -q 'HOLD' "$DOC" || fail "$DOC lost HOLD"
grep -q 'Overlay main does not match the sold cards' "$DOC" \
	|| fail "$DOC lost overlay-main diverge"
if grep -qiE 'C13-05 closed|catalog matches sold|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c13-05-overlay-catalog-diverge/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("overlay_matches_sold") is not False:
    raise SystemExit("overlay_matches_sold must stay false")
if data.get("hub_tier_card_is_not_overlay") is not True:
    raise SystemExit("hub_tier_card_is_not_overlay must stay true")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
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

python3 - "$SOLD" "$HOLD" "$CAT" <<'PY' || fail "overlay catalog matched the sold map"
import json, re, sys

sold = {e["slug"] for e in json.load(open(sys.argv[1], encoding="utf-8"))["entries"]}
hold_doc = open(sys.argv[2], encoding="utf-8").read()
hold = set(re.findall(r"^hold-slug:\s*([a-z0-9-]+)\s*$", hold_doc, flags=re.M))
if hold != {"caeptransmit", "circuit-breaker"}:
    raise SystemExit("HOLD doc drifted from named airs slugs")
catalog = set(re.findall(r'Key:\s*"([a-z0-9-]+)"', open(sys.argv[3], encoding="utf-8").read()))
if not catalog:
    raise SystemExit("catalog yielded no keys")
missing = sorted(s for s in sold if s not in catalog and s not in hold)
surplus = sorted(s for s in catalog if s not in sold and s not in hold)
if not missing and not surplus:
    raise SystemExit("overlay catalog equals sold map; HOLD is stale")
PY

say "check-c13-05-overlay-catalog-diverge: CLEAN — overlay catalog still diverges from the sold map."
exit 0
