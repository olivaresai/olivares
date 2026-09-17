#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c02-producer-r2-from-grants.sh — C02. Set-keyed R2 from live grants.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-producer-r2-from-grants: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-producer-r2-from-grants: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C02PK_JSON:-design/c02-producer-r2-from-grants.json}"
DOC="${OLIVARES_C02PK_DOC:-design/C02-PRODUCER-R2-FROM-GRANTS-2026-08-19.md}"
ART="${OLIVARES_C02PK_ART:-commercial/license-worker/src/download/artifacts.ts}"
GATE="${OLIVARES_C02PK_GATE:-commercial/license-worker/src/download/gate.ts}"
TEST="${OLIVARES_C02PK_TEST:-commercial/license-worker/test/download.test.ts}"
PUB="${OLIVARES_C02PK_PUB:-scripts/publish-enterprise-artifacts.sh}"
SETS="${OLIVARES_C02PK_SETS:-$(dirname "$ART")/sets.ts}"
PROBE="$ROOT/scripts/lib/c02-download-contract.mjs"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$ART" ] || cannot "missing $ART"
[ -f "$GATE" ] || cannot "missing $GATE"
[ -f "$TEST" ] || cannot "missing $TEST"
[ -f "$PUB" ] || cannot "missing $PUB"
[ -r "$SETS" ] || cannot "missing $SETS"
[ -r "$PROBE" ] || cannot "missing $PROBE"
command -v python3 >/dev/null || cannot "no python3"
command -v node >/dev/null || cannot "no node"
# ⛔ LA PROPIEDAD ES «LA CLAVE LLEVA EL CONJUNTO», Y HOY SE PUEDE ESCRIBIR DE DOS FORMAS.
# Este check exigia el literal `enterprise/${VERSION}/${SET}/` DENTRO del publicador. `f42b3442b`
# movio la clave a una PLANTILLA del contrato generado (`keys.artifact`), leida con
# `leer_contrato`: el literal desaparecio y el check habria pasado CLEAN por AUSENCIA de la cadena
# que buscaba, que es la peor forma de pasar. Se aceptan las DOS expresiones —en linea o por
# contrato— y se sigue rechazando que no este ninguna, que es lo unico que la garantia prohibe.
# Aceptar las dos NO es relajar: los senuelos de la bateria usan la forma en linea y el arbol vivo
# usa la del contrato, y un mutante que quita el conjunto de cualquiera de las dos sigue muriendo.
_set_ok=0
_unscoped=0
if grep -q 'enterprise/${VERSION}/${SET}/' "$PUB"; then
  _set_ok=1
fi
if grep -qE 'enterprise/\$\{VERSION\}/\$\(basename' "$PUB"; then
  _unscoped=1
fi
CONTRATO="${OLIVARES_C02_CONTRATO:-commercial/license-worker/contracts/publisher.gen.json}"
if [ "$_set_ok" -eq 0 ] && grep -Fq 'leer_contrato keys.artifact' "$PUB"; then
  # El publicador toma la clave del contrato: sin el contrato NO SE PUEDE MIRAR, y eso no es
  # lo mismo que estar roto. Un senuelo que copia el publicador y olvida el contrato caia
  # aqui como FAIL y acusaba al arbol de un defecto del banco.
  [ -r "$CONTRATO" ] || cannot "el publicador lee keys.artifact y no encuentro $CONTRATO"
  _art="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["keys"]["artifact"])' "$CONTRATO" 2>/dev/null)" \
    || cannot "el publicador lee keys.artifact del contrato y el contrato no lo declara"
  case "$_art" in
    */'{set}'/*) _set_ok=1 ;;
    'enterprise/{version}/olivares'*) _unscoped=1 ;;
  esac
fi
[ "$_set_ok" -eq 1 ] || fail "$PUB lost the per-set binary key"
[ "$_unscoped" -eq 0 ] || fail "$PUB still writes the unscoped monolith binary key"

grep -q 'delivery NOT CLOSED' "$DOC" || fail "$DOC lost delivery NOT CLOSED"
if grep -qiE 'bytes are real|FIRMA A claimed|stub gone' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi

contract_rc=0
node "$PROBE" "$ART" "$SETS" "$GATE" || contract_rc=$?
case "$contract_rc" in
  0) ;;
  2) cannot "download executable contract has missing runtime inputs" ;;
  *) fail "artifact/download executable contract failed" ;;
esac

python3 - "$JSON" "$TEST" <<'PY' || fail "JSON/test contract failed the C02 producer-key contract"
import json, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
test = open(sys.argv[2], encoding="utf-8").read()

if data.get("schema") != "c02-producer-r2-from-grants/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("binary_key_includes_set") is not True:
    raise SystemExit("binary_key_includes_set must stay true")
if data.get("set_source") != "live_grants":
    raise SystemExit("set_source must stay live_grants")
if data.get("set_on_binary_query") != "refused":
    raise SystemExit("set_on_binary_query must stay refused")
if data.get("monolith_fallback") is not False:
    raise SystemExit("monolith_fallback must stay false")
if data.get("delivery_404_closed") is not False:
    raise SystemExit("delivery_404_closed must stay false")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)

if "no live grant for set" not in test:
    raise SystemExit("tests lost the empty-grants mutant")
if "engine-shaped query streams the set-keyed artifact" not in test:
    raise SystemExit("tests lost the engine-shaped no-fire")
PY

say "check-c02-producer-r2-from-grants: CLEAN — set-keyed R2 from grants; query set/variant refused."
exit 0
