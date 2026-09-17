#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C13-01: the HISTORICAL record of the 2026-08-20 catalog HOLD, preserved.
# 0 CLEAN · 1 finding · 2 LOOK.
#
# ⛔ QUE CAMBIO AQUI, Y POR QUE (2026-09-05). Hasta hoy este guion exigia que el catalogo de
# activacion VIVO bajo `OLIVARES_ENT_DIR` NO tuviera la fila `iso42001`, y por eso estaba
# ROJO: `daa083e56f331af6158475fc304fee633acbfc2b` (2026-09-02) la anadio. La observacion del
# 20/08 era CIERTA sobre `bada7f7`; dejo de describir el main de hoy. La adjudicacion
# (an internal design note (not shipped)) decide conservar el registro y NO
# retirar la capacidad para que un gate vuelva a verde.
#
#   · ESTE guion conserva el REGISTRO y no abre el clon del overlay. No acredita main.
#   · el HECHO PRESENTE lo mide `scripts/check-overlay-live-facts.sh` sobre el `origin/main`
#     SELLADO, leyendo la fila del catalogo como DATO y su corte por etiquetas de build.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-01-iso42001-catalog: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-01-iso42001-catalog: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C1301_JSON:-design/c13-01-iso42001-catalog.json}"
DOC="${OLIVARES_C1301_DOC:-design/C13-01-ISO42001-CATALOG-HOLD-2026-08-20.md}"
ADJ="${OLIVARES_C1301_ADJ:-design/OVERLAY-FACT-GATES-ADJUDICATION-2026-09-05.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$ADJ" ] || cannot "missing $ADJ: the record is preserved, but what superseded it is not"

grep -q 'HOLD' "$DOC" || fail "$DOC lost HOLD"
grep -q 'Catalog not on overlay main' "$DOC" || fail "$DOC lost catalog-absent"
if grep -qiE 'iso42001 catalog landed|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi
grep -q 'C13-01' "$ADJ" || fail "$ADJ no longer names C13-01"
grep -q 'daa083e56f331af6158475fc304fee633acbfc2b' "$ADJ" \
	|| fail "$ADJ no longer names the commit that changed the fact"

# El pin se comprueba POR SU VALOR (ver la nota en check-c03-06-needs-decision.sh), y ademas
# se exige que el documento siga NOMBRANDO ese mismo overlay en corto: el acta y su prosa
# tienen que hablar del mismo sujeto o el registro no es legible.
python3 - "$JSON" <<'PY' || fail "the historical record drifted"
import json, sys

HUB = "a0360c1d2abff8c41ed67a3b8ac0d888d6916f16"
OVERLAY = "bada7f7f9339a98131f7f9a0f536a3e9c474626c"

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("lote") != "C13-01":
    raise SystemExit("the acta no longer says which lote it belongs to")
if data.get("iso42001_in_catalog") is not False:
    raise SystemExit("iso42001_in_catalog must stay false: it is what was OBSERVED on bada7f7")
if data.get("panel_executed") is not False:
    raise SystemExit("panel_executed must stay false")
if data.get("hub") != HUB:
    raise SystemExit("hub pin is %r; the 2026-08-20 observation was taken on %s"
                     % (data.get("hub"), HUB))
if data.get("overlay") != OVERLAY:
    raise SystemExit("overlay pin is %r; the 2026-08-20 observation was taken on %s"
                     % (data.get("overlay"), OVERLAY))
PY
grep -q 'bada7f7' "$DOC" || fail "$DOC no longer names the overlay the observation was taken on"

say "check-c13-01-iso42001-catalog: CLEAN — HISTORICAL RECORD PRESERVED (overlay bada7f7f, hub"
say "  a0360c1d2, panel_executed=false). This is NOT evidence about current main: iso42001 was"
say "  catalogued by daa083e5, and today's catalog is measured by"
say "  scripts/check-overlay-live-facts.sh against the SEALED overlay main."
exit 0
