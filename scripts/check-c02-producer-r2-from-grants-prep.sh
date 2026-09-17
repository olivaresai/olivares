#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C02 unique leftover unique vs check-c02-producer-r2-from-grants.sh
# (on main, not in lint:addon-sets) and unique leftover unique vs #1381.
# 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-producer-r2-from-grants-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-producer-r2-from-grants-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C02PKP_JSON:-design/c02-producer-r2-from-grants-prep-2026-08-20.json}"
DOC="${OLIVARES_C02PKP_DOC:-design/C02-PRODUCER-R2-FROM-GRANTS-PREP-2026-08-20.md}"
ART="${OLIVARES_C02PKP_ART:-commercial/license-worker/src/download/artifacts.ts}"
GATE="${OLIVARES_C02PKP_GATE:-commercial/license-worker/src/download/gate.ts}"
TEST="${OLIVARES_C02PKP_TEST:-commercial/license-worker/test/download.test.ts}"
PUB="${OLIVARES_C02PKP_PUB:-scripts/publish-enterprise-artifacts.sh}"
SETS="${OLIVARES_C02PKP_SETS:-$(dirname "$ART")/sets.ts}"
PROBE="$ROOT/scripts/lib/c02-download-contract.mjs"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$ART" ] || cannot "missing $ART"
[ -r "$GATE" ] || cannot "missing $GATE"
[ -r "$TEST" ] || cannot "missing $TEST"
[ -r "$PUB" ] || cannot "missing $PUB"
[ -r "$SETS" ] || cannot "missing $SETS"
[ -r "$PROBE" ] || cannot "missing $PROBE"
command -v python3 >/dev/null || cannot "no python3"
command -v node >/dev/null || cannot "no node"

grep -F -q 'Unique leftover unique vs `check-c02-producer-r2-from-grants.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original producer-R2 check"
grep -q 'delivery NOT CLOSED' "$DOC" || fail "prepare doc lost delivery NOT CLOSED"
if grep -qiE 'bytes are real|FIRMA A claimed|stub gone' "$DOC"; then
  fail "prepare doc claims a close this lote does not have"
fi
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

contract_rc=0
node "$PROBE" "$ART" "$SETS" "$GATE" || contract_rc=$?
case "$contract_rc" in
  0) ;;
  2) cannot "download executable contract has missing runtime inputs" ;;
  *) fail "artifact/download executable contract failed" ;;
esac

python3 - "$JSON" "$TEST" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c02-producer-r2-from-grants-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c02-producer-r2-from-grants-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
    test = open(sys.argv[2], encoding="utf-8").read()
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c02-producer-r2-from-grants-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("binary_key_includes_set") is not True:
    fail("binary_key_includes_set must stay true")
if data.get("set_source") != "live_grants":
    fail("set_source must stay live_grants")
if data.get("set_on_binary_query") != "refused":
    fail("set_on_binary_query must stay refused")
if data.get("monolith_fallback") is not False:
    fail("monolith_fallback must stay false")
if data.get("delivery_404_closed") is not False:
    fail("delivery_404_closed must stay false")
if data.get("r2_objects_verified") is not False:
    fail("r2_objects_verified must stay false")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
if data.get("overlay_producer_pr") != 75:
    fail("overlay_producer_pr must stay 75")
hub = data.get("hub") or ""
if len(hub) != 40 or any(c not in "0123456789abcdef" for c in hub):
    fail("hub is not 40-hex")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)

if "no live grant for set" not in test:
    fail("tests lost the empty-grants mutant")
print("json-ok")
PY

say "check-c02-producer-r2-from-grants-prep: CLEAN — set-keyed R2 from grants; delivery NOT CLOSED."
exit 0
