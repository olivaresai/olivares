#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C03-06: the HISTORICAL record of the 2026-08-20 observation, preserved.
# 0 CLEAN · 1 finding · 2 LOOK.
#
# ⛔ QUE CAMBIO AQUI, Y POR QUE (2026-09-05). Hasta hoy este guion comparaba la observacion
# del 20/08 —«EvaluateOverride NO-GATE; durableLicensed sin composicion de compra»— con
# FICHEROS VIVOS del checkout bajo `OLIVARES_ENT_DIR`, y por eso estaba ROJO: la mitad de
# `durableLicensed` DEJO DE SER CIERTA cuando `daa083e56f331af6158475fc304fee633acbfc2b`
# (2026-09-02) integro la composicion de compra. El rojo no era una regresion: era un HECHO
# QUE CAMBIO, y el remediador lo dijo correctamente al negarse a curarlo con un numero.
#
# La adjudicacion (an internal design note (not shipped)) separa las dos cosas:
#
#   · ESTE guion conserva el REGISTRO: comprueba que el acta y su documento siguen diciendo
#     lo que se observo, con su SHA y sus banderas intactos. NO abre el clon del overlay, NO
#     lee ningun fichero vivo y NO acredita el `main` de hoy. Su nombre y su tarea se
#     conservan por compatibilidad.
#   · el HECHO PRESENTE lo mide `scripts/check-overlay-live-facts.sh`, contra el `origin/main`
#     SELLADO y sus blobs, que es la unica fuente que puede acreditar un aterrizaje.
#
# ⚠ Consecuencia deliberada: este guion da el MISMO veredicto con un checkout Enterprise
# nuevo, con uno viejo o sin ninguno. Un registro historico cuyo resultado dependa del arbol
# de al lado no es un registro.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-06-needs-decision: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-06-needs-decision: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0306_JSON:-design/c03-06-needs-decision.json}"
DOC="${OLIVARES_C0306_DOC:-design/C03-06-NEEDS-DECISION-2026-08-20.md}"
ADJ="${OLIVARES_C0306_ADJ:-design/OVERLAY-FACT-GATES-ADJUDICATION-2026-09-05.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$ADJ" ] || cannot "missing $ADJ: the record is preserved, but what superseded it is not"

grep -q 'EvaluateOverride is NO-GATE' "$DOC" || fail "$DOC lost NO-GATE"
grep -q 'HOLD on narrowing' "$DOC" || fail "$DOC lost HOLD on narrowing"
grep -q 'not to' "$DOC" || fail "$DOC lost the wrong-pack refusal"
if grep -qiE 'durableLicensed now scoped|EvaluateOverride gated|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a motor this lote does not have"
fi

# La adjudicacion tiene que seguir NOMBRANDO este lote y el commit que cambio el hecho: sin
# eso, el registro queda huerfano y un lector futuro vuelve a leer la ausencia como vigente.
grep -q 'C03-06' "$ADJ" || fail "$ADJ no longer names C03-06"
grep -q 'daa083e56f331af6158475fc304fee633acbfc2b' "$ADJ" \
	|| fail "$ADJ no longer names the commit that changed the fact"

# ⛔ EL PIN SE COMPRUEBA POR SU VALOR, NO POR SU FORMA. Antes solo se exigia «40 hex», asi que
# cambiar el SHA de la observacion por otro cualquiera pasaba el gate: un registro historico
# cuyo sujeto se puede sustituir en silencio no registra nada. Los dos valores son los que el
# acta trae desde el 2026-08-20 y no se re-miden nunca — moverlos reescribiria un hecho pasado.
python3 - "$JSON" <<'PY' || fail "the historical record drifted"
import json, sys

HUB = "22d4c16deb31f41b5e7c6c19c8ac1cfa2dd29fde"
OVERLAY = "bada7f7f9339a98131f7f9a0f536a3e9c474626c"

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("lote") != "C03-06":
    raise SystemExit("the acta no longer says which lote it belongs to")
if data.get("evaluate_override_gated") is not False:
    raise SystemExit("evaluate_override_gated must stay false")
if data.get("durable_addon_scoped") is not False:
    raise SystemExit("durable_addon_scoped must stay false")
if data.get("narrow_to_identity_scale") is not False:
    raise SystemExit("narrow_to_identity_scale must stay false")
if data.get("hub") != HUB:
    raise SystemExit("hub pin is %r; the 2026-08-20 observation was taken on %s"
                     % (data.get("hub"), HUB))
if data.get("overlay") != OVERLAY:
    raise SystemExit("overlay pin is %r; the 2026-08-20 observation was taken on %s"
                     % (data.get("overlay"), OVERLAY))
PY

say "check-c03-06-needs-decision: CLEAN — HISTORICAL RECORD PRESERVED (overlay bada7f7f, hub"
say "  22d4c16de, flags intact). This is NOT evidence about current main: the durableLicensed"
say "  half was superseded by daa083e5 and today's facts are measured by"
say "  scripts/check-overlay-live-facts.sh against the SEALED overlay main."
exit 0
