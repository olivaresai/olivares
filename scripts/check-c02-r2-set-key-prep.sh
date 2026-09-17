#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C02 producer-by-set R2 unique leftover unique vs #944/#928/#889 and
# check-r2-set-key.sh (original OPEN CHECK would FAIL on origin/main).
# 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-r2-set-key-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-r2-set-key-prep: COULD NOT LOOK — $*" >&2; exit 2; }

if [ -n "${OLIVARES_ROOT:-}" ]; then
  ROOT="$OLIVARES_ROOT"
else
  ROOT="$(
    cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2
    pwd
  )" || cannot "cannot resolve repository root"
fi
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C02R2P_JSON:-design/c02-r2-set-key-prep-2026-08-20.json}"
DOC="${OLIVARES_C02R2P_DOC:-design/C02-R2-SET-KEY-PREP-2026-08-20.md}"
ART="${OLIVARES_C02R2P_ART:-commercial/license-worker/src/download/artifacts.ts}"
SETS="${OLIVARES_C02R2P_SETS:-commercial/license-worker/src/download/sets.ts}"
GATE="${OLIVARES_C02R2P_GATE:-commercial/license-worker/src/download/gate.ts}"
PUB="${OLIVARES_C02R2P_PUB:-scripts/publish-enterprise-artifacts.sh}"

for f in "$JSON" "$DOC" "$ART" "$SETS" "$GATE" "$PUB"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"
command -v node >/dev/null || cannot "no node"

grep -F -q 'Unique leftover unique vs `#944`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #944"
grep -F -q 'Unique leftover unique vs `#928`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #928"
grep -F -q 'Unique leftover unique vs `#889`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #889"
grep -F -q 'Unique leftover unique vs `check-r2-set-key.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original CHECK"
grep -F -q 'HOLD. NOT APPLIED.' "$DOC" \
  || fail "prepare doc lost HOLD"
grep -F -q 'Remainder is legacyMonolithKey/setOrErr/isFullCommercialSet — not applied.' "$DOC" \
  || fail "prepare doc lost remainder HOLD"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|legacyMonolithKey landed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

PROBE="$ROOT/scripts/lib/c02-download-contract.mjs"
[ -r "$PROBE" ] || cannot "missing $PROBE"
contract_rc=0
node "$PROBE" "$ART" "$SETS" "$GATE" || contract_rc=$?
case "$contract_rc" in
  0) ;;
  2) cannot "download executable contract has missing runtime inputs" ;;
  *) fail "artifact/download executable contract failed" ;;
esac
if grep -q 'function legacyMonolithKey' "$ART"; then
  fail "legacyMonolithKey landed — this HOLD lote does not apply #944"
fi
if grep -q 'setOrErr' "$GATE"; then
  fail "setOrErr landed — this HOLD lote does not apply #944"
fi
if grep -q 'isFullCommercialSet' "$GATE"; then
  fail "isFullCommercialSet landed — this HOLD lote does not apply #944"
fi
# ⛔ LA PROPIEDAD ES «LA CLAVE LLEVA EL CONJUNTO», Y HOY SE PUEDE ESCRIBIR DE DOS FORMAS.
# Este check exigia el literal `enterprise/${VERSION}/${SET}/$(basename` DENTRO del publicador.
# `f42b3442b` movio la clave a una PLANTILLA del contrato generado (`keys.artifact`), leida con
# `leer_contrato`: el literal desaparecio y el check habria pasado CLEAN por AUSENCIA de la cadena
# que buscaba. Se aceptan las DOS expresiones y se sigue rechazando que no este ninguna.
_set_ok=0
_unscoped=0
if grep -Fq 'enterprise/${VERSION}/${SET}/$(basename' "$PUB"; then
  _set_ok=1
fi
if grep -Fq 'enterprise/${VERSION}/$(basename' "$PUB"; then
  _unscoped=1
fi
CONTRATO="${OLIVARES_C02R2P_CONTRATO:-commercial/license-worker/contracts/publisher.gen.json}"
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
[ "$_set_ok" -eq 1 ] || fail "publisher tarball key lost /<set>/"
[ "$_unscoped" -eq 0 ] || fail "publisher still plans an unscoped enterprise/\${VERSION}/ tarball"

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c02-r2-set-key-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c02-r2-set-key-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c02-r2-set-key-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("artifact_key_set_keyed") is not True:
    fail("artifact_key_set_keyed must stay true")
if data.get("gate_passes_purchased_set") is not True:
    fail("gate_passes_purchased_set must stay true")
if data.get("publisher_set_path") is not True:
    fail("publisher_set_path must stay true")
if data.get("legacy_monolith_key_landed") is not False:
    fail("legacy_monolith_key_landed must stay false")
if data.get("set_or_err_landed") is not False:
    fail("set_or_err_landed must stay false")
if data.get("is_full_commercial_set_landed") is not False:
    fail("is_full_commercial_set_landed must stay false")
if data.get("remainder_applied") is not False:
    fail("remainder_applied must stay false")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
hub = data.get("hub") or ""
if len(hub) != 40 or any(c not in "0123456789abcdef" for c in hub):
    fail("hub is not 40-hex")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)
print("json-ok")
PY

say "check-c02-r2-set-key-prep: CLEAN — set-keyed key+gate+publisher already on main; legacyMonolithKey HOLD; overlay remasure not in this gate."
exit 0
