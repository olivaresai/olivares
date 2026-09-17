#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C13-01 remasure: the HISTORICAL record of the 2026-08-20 re-measure, preserved.
# 0 CLEAN · 1 finding · 2 LOOK.
#
# ⛔ QUE CAMBIO AQUI, Y POR QUE (2026-09-05). Igual que su gemelo del catalogo: exigia que el
# catalogo VIVO del overlay siguiera SIN `iso42001` y quedo rojo cuando
# `daa083e56f331af6158475fc304fee633acbfc2b` (2026-09-02) lo catalogo. Se conserva el
# registro —incluidos `u_f`/`u_d` en UNKNOWN— y la comparacion viva pasa al contrato unico
# `scripts/check-overlay-live-facts.sh`. Ver an internal design note (not shipped)
#
# ⚠ LO QUE SIGUE LEYENDO, y por que no es una comparacion viva del overlay: el MAPA VENDIDO
# `commercial/module-slug-package.json` es un fichero de ESTE repositorio, no del overlay. La
# observacion del 20/08 fue precisamente que el mapa nombraba el slug y el catalogo no; que el
# mapa lo siga nombrando es coherencia del registro. La mitad que hablaba del overlay —y solo
# esa— es la que se ha retirado.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-01-iso42001-remeasure: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-01-iso42001-remeasure: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C1301R_JSON:-design/c13-01-iso42001-remeasure.json}"
DOC="${OLIVARES_C1301R_DOC:-design/C13-01-ISO42001-REMEASURE-2026-08-20.md}"
SOLD="${OLIVARES_C1301R_SOLD:-commercial/module-slug-package.json}"
ADJ="${OLIVARES_C1301R_ADJ:-design/OVERLAY-FACT-GATES-ADJUDICATION-2026-09-05.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$SOLD" ] || cannot "missing sold slug map"
[ -f "$ADJ" ] || cannot "missing $ADJ: the record is preserved, but what superseded it is not"

grep -q 'HOLD' "$DOC" || fail "$DOC lost HOLD"
grep -q 'Catalog not on overlay main' "$DOC" || fail "$DOC lost catalog-absent"
if grep -qiE 'iso42001 catalog landed|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi
grep -q 'C13-01' "$ADJ" || fail "$ADJ no longer names C13-01"
grep -q 'daa083e56f331af6158475fc304fee633acbfc2b' "$ADJ" \
	|| fail "$ADJ no longer names the commit that changed the fact"

python3 - "$JSON" "$SOLD" <<'PY' || fail "the historical record drifted"
import json, sys

HUB = "2f7d5cc4e3f48b7931bb52f4b1fe1fe607a37770"
OVERLAY = "bada7f7f9339a98131f7f9a0f536a3e9c474626c"

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c13-01-iso42001-remeasure/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("lote") != "C13-01":
    raise SystemExit("the acta no longer says which lote it belongs to")
if data.get("iso42001_in_catalog") is not False:
    raise SystemExit("iso42001_in_catalog must stay false: it is what was OBSERVED on bada7f7")
if data.get("iso42001_in_sold_map") is not True:
    raise SystemExit("iso42001_in_sold_map must stay true")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
if data.get("hub") != HUB:
    raise SystemExit("hub pin is %r; the 2026-08-20 re-measure was taken on %s"
                     % (data.get("hub"), HUB))
if data.get("overlay") != OVERLAY:
    raise SystemExit("overlay pin is %r; the 2026-08-20 re-measure was taken on %s"
                     % (data.get("overlay"), OVERLAY))
sold = {e["slug"] for e in json.load(open(sys.argv[2], encoding="utf-8"))["entries"]}
if "iso42001" not in sold:
    raise SystemExit("this repository's sold map lost iso42001, so the record is incoherent")
PY
grep -q 'bada7f7' "$DOC" || fail "$DOC no longer names the overlay the re-measure was taken on"

say "check-c13-01-iso42001-remeasure: CLEAN — HISTORICAL RECORD PRESERVED (overlay bada7f7f,"
say "  hub 2f7d5cc4e, u_f/u_d UNKNOWN) and this repository still sells the slug."
say "  This is NOT evidence about current main: iso42001 was catalogued by daa083e5, and"
say "  today's facts are measured by scripts/check-overlay-live-facts.sh against the SEALED"
say "  overlay main."
exit 0
