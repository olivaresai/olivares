#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C13-05 remainder: the overlay-main catalog and the sold map name the SAME modules.
# Overlay via OLIVARES_ENT_DIR. 0 CLEAN · 1 finding · 2 LOOK.
#
# ⛔ HASTA EL 2026-09-03 ESTE GATE EXIGÍA QUE LA DIVERGENCIA SIGUIERA EXISTIENDO, y ése es el
# defecto que enseña: un gate que fija el ESTADO en que encontró el árbol llama regresión a la cura.
# Medido ese día con el overlay real y el mapa derivado del canon: salió ROJO con
# «overlay catalog equals sold map; HOLD is stale». Ahora fija la PROPIEDAD — igualdad, con los
# HOLDs declarados como único hueco permitido y DERIVADOS, nunca fijados a dos nombres.

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

grep -q 'HOLD' "$DOC" || fail "$DOC lost the HOLD history"
# The doc must keep the SENTENCE that explains which side was behind. Losing it turns a cured
# divergence back into folklore about "the overlay being wrong", which is what it was not.
grep -q 'El lado que iba por detrás era el hub' "$DOC" \
	|| fail "$DOC lost the sentence naming which side was behind"
if grep -qiE 'FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a signature this lote does not have"
fi

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c13-05-overlay-catalog-diverge/v2":
    raise SystemExit("unknown schema %r (v1 pinned the divergence and is retired)" % data.get("schema"))
if data.get("overlay_matches_sold") is not True:
    raise SystemExit("overlay_matches_sold must be true: this lote cured the divergence, and a "
                     "record that still says false describes a tree that no longer exists")
if data.get("hub_tier_card_is_not_overlay") is not True:
    raise SystemExit("hub_tier_card_is_not_overlay must stay true")
ev = data.get("evidence")
if not isinstance(ev, dict):
    raise SystemExit("the evidence block is missing")
for k in ("u_f", "u_d"):
    if ev.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN until somebody measures it" % k)
for key in ("hub", "overlay"):
    val = ev.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("evidence.%s is not a 40-hex object id" % key)
PY

ENT="${OLIVARES_ENT_DIR:-}"
[ -n "$ENT" ] || cannot "OLIVARES_ENT_DIR unset"
[ -d "$ENT" ] || cannot "OLIVARES_ENT_DIR is not a directory"
CAT="$ENT/enterprise/activation/catalog.go"
[ -f "$CAT" ] || cannot "missing activation catalog"

python3 - "$SOLD" "$HOLD" "$CAT" <<'PY' || fail "the overlay catalog and the sold map do not describe the same product"
import json, re, sys

# ⛔ THIS GATE USED TO REQUIRE THE DIVERGENCE TO EXIST, AND THAT IS BACKWARDS. Its last line was
# `if not missing and not surplus: raise SystemExit("overlay catalog equals sold map; HOLD is
# stale")` — so the day somebody CURED the divergence, the gate went red and called the cure a
# regression. Measured 2026-09-03 with the real overlay and the canon-derived sold map: exactly
# that happened, and the message was the gate telling the truth about itself.
#
# The divergence was never the overlay's fault. The overlay catalog already carried the 30 modules
# the canon sells; `commercial/module-slug-package.json` carried 27 derived from a document that is
# not the canon. So the property to hold from here on is EQUALITY, and a declared packaging HOLD is
# the only permitted hole — derived (a canon slug the map does not carry), never pinned to two
# names, which is the same cure applied to the 27/25 literals.
sold = {e["slug"] for e in json.load(open(sys.argv[1], encoding="utf-8"))["entries"]}
hold_doc = open(sys.argv[2], encoding="utf-8").read()
hold = set(re.findall(r"^hold-slug:\s*([a-z0-9-]+)\s*$", hold_doc, flags=re.M))
catalog = set(re.findall(r'Key:\s*"([a-z0-9-]+)"', open(sys.argv[3], encoding="utf-8").read()))
if not catalog:
    raise SystemExit("catalog yielded no keys")
if not sold:
    raise SystemExit("the sold map yielded no slugs")

stale = sorted(s for s in hold if s in sold)
if stale:
    raise SystemExit("the HOLD doc names %s as a packaging hole while the sold map carries it; a "
                     "HOLD whose condition is cured is theatre" % ", ".join(stale))

missing = sorted(s for s in sold if s not in catalog and s not in hold)
surplus = sorted(s for s in catalog if s not in sold and s not in hold)
if missing or surplus:
    raise SystemExit("overlay catalog and sold map disagree — sold but not in the overlay catalog: "
                     "%r; in the overlay catalog but not sold: %r. Either is a module whose bytes "
                     "and whose entitlement do not describe the same product" % (missing, surplus))
PY

say "check-c13-05-overlay-catalog-diverge: CLEAN — the overlay catalog and the sold map name the same modules."
exit 0
