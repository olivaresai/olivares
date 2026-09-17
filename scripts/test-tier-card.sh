#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-tier-card.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/tier-card.XXXXXX")"

# ⛔ ${TMPDIR:-/tmp} PUEDE ESTAR MONTADO noexec —el /tmp de este contenedor lo está— y el sujeto de
# esta batería comprueba `[ -x scripts/addon-sets.sh ]`. En un montaje noexec ese test consulta
# access(X_OK), que devuelve EACCES AUNQUE EL BIT ESTÉ PUESTO: el `chmod +x` de más abajo funciona,
# `ls -l` enseña `-rwxr-xr-x`, y `[ -x ]` sale FALSO igual.
#
# El síntoma engaña más que el de sus hermanas: no falla al EJECUTAR, falla al PREGUNTAR, así que el
# gate informa «addon-sets.sh missing» sobre un fichero que existe, es ejecutable y da CLEAN si lo
# corres a mano. Medido el 2026-08-19: `lint:addon-sets-gate` ROJO en `origin/main` limpio, y por
# vivir en el carril rápido bloqueaba el push de toda máquina con /tmp noexec.
#
# Es la misma clase que ya documentaron test-alias-image-digest.sh (2026-08-01) y
# test-publish-enterprise-artifacts.sh (#1065). El nombre del respaldo usa el prefijo que
# `.gitignore` ya cubre (`/.tmpexec.*`), para que un residuo nunca salga untracked.
printf '#!/bin/sh\nexit 0\n' >"$TMP/.execprobe" && chmod +x "$TMP/.execprobe"
if ! [ -x "$TMP/.execprobe" ]; then
	rm -rf "$TMP"
	TMP="$(mktemp -d "$ROOT/.tmpexec.tier-card.XXXXXX")" || exit 2
fi
rm -f "$TMP/.execprobe"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/commercial" "$TMP/tree/scripts"
  cp "$ROOT/design/PRICING-CANON.md" "$TMP/tree/design/"
  cp "$ROOT/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" "$TMP/tree/design/"
  cp "$ROOT/commercial/module-slug-package.json" "$TMP/tree/commercial/"
  cp "$ROOT/scripts/addon-sets.sh" "$CHECK" "$TMP/tree/scripts/"
  # ⛔ Y EL DERIVADOR, porque este gate ya le pregunta. Sin él el árbol de prueba devolvía 127 y el
  # gate —correctamente— lo llamaba «no pude mirar»: los casos medían la ausencia del envoltorio, no
  # la guarda. Se construye UNA vez, fuera del bucle de montaje.
  cp "$ROOT/scripts/module-catalog-go.sh" "$TMP/tree/scripts/"
  mkdir -p "$TMP/tree/commercial/license-worker/src/catalog" "$TMP/tree/commercial/license-worker/contracts"
  cp "$ROOT/design/c13-02-package-view.json" "$TMP/tree/design/"
  cp "$ROOT/commercial/module-package-slugs.json" "$TMP/tree/commercial/"
  cp "$ROOT/commercial/license-worker/src/catalog/module-slug-package.json" \
    "$TMP/tree/commercial/license-worker/src/catalog/"
  cp -r "$ROOT/commercial/commerce-lint" "$TMP/tree/commercial/"
  chmod +x "$TMP/tree/scripts/"*.sh
}
export GOWORK=off
MCBIN="$(mktemp -u "${TMPDIR:-/workspace/.olivares-tmptest}/tc-bin.XXXXXX")"
( cd "$ROOT/commercial/commerce-lint" && go build -o "$MCBIN" . ) >/dev/null 2>&1 || {
  echo "test-tier-card: NO PUDE MIRAR — el derivador no construye" >&2; exit 2; }
export OLIVARES_MODULE_CATALOG_BIN="$MCBIN"
run() { OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-tier-card.sh" >/dev/null 2>"$TMP/err"; }

stage
if run; then ok "live map + named HOLDs is CLEAN"; else bad "live tree should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/commercial/module-slug-package.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p))
d["entries"]=[e for e in d["entries"] if e["slug"]!="content-firewall"]
json.dump(d, open(p,"w"))
PY
if run; then bad "dropping a shipping slug stayed CLEAN"; else ok "sold map missing a shipping slug is a finding"; fi

# ⛔ EL CASO QUE HABÍA AQUÍ VACIABA LA LISTA DE HOLD Y ESPERABA ROJO, Y ESO DEJÓ DE SER UN DEFECTO
# EL 2026-09-03. El gate ya no fija los dos nombres: DERIVA el conjunto esperado como «slugs que el
# canon asigna y el mapa vendido no lleva». Con el mapa derivado del canon ese conjunto está vacío,
# así que una lista de HOLD vacía es la respuesta CORRECTA y exigir rojo sobre ella era pedir que un
# HOLD curado siguiera escrito. Los dos casos que lo sustituyen prueban las DOS direcciones de la
# derivación, que es lo que el pin no podía probar.

stage
# Mutante A: un slug que el canon asigna desaparece del mapa vendido y NADIE lo declara como HOLD.
# Es el agujero sin explicar, y el gate tiene que nombrarlo.
python3 - "$TMP/tree/commercial/module-slug-package.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p))
before=len(d["entries"])
d["entries"]=[e for e in d["entries"] if e["slug"]!="caeptransmit"]
assert len(d["entries"])==before-1, "control de mutacion: caeptransmit no estaba en el mapa"
json.dump(d, open(p,"w"))
PY
# ⛔ Y AHORA LO MATA LA DERIVACIÓN, NO LA LÓGICA DEL HOLD — y ése es el orden correcto, no una
# pérdida. Desde que este gate pregunta primero al derivador, CUALQUIER mapa que no sea la
# derivación del canon muere ahí: la integridad del mapa es del derivador, y la honestidad del
# documento de HOLD es de este gate. Exigir aquí el testigo `caeptransmit` sería pedirle a la capa
# de arriba que repita el trabajo de la de abajo, y de paso ocultaría que el mapa está mal por una
# razón más grave que un HOLD sin declarar.
if run; then bad "an undeclared hole stayed CLEAN"; else
  grep -qE 'canon derivation|derivación del canon' "$TMP/err" \
    && ok "mutant (map that is no longer the canon derivation) is killed BY THE DERIVATION, before the HOLD logic" \
    || bad "killed, but not by the derivation ($(head -2 "$TMP/err"))"
fi

stage
# Mutante B: el documento declara como HOLD de empaquetado un slug que el mapa SÍ lleva. Un HOLD
# cuya condición está curada es teatro, y el gate tiene que decirlo.
python3 - "$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" <<'PY'
import sys
p=sys.argv[1]
text=open(p,encoding="utf-8").read()
assert "\nhold-slug:" not in text, "control de mutacion: el doc ya traia un hold-slug"
open(p,"w",encoding="utf-8").write(text+"\nhold-slug: content-firewall\n")
PY
if run; then bad "a cured HOLD stayed CLEAN"; else
  grep -q 'content-firewall' "$TMP/err" \
    && ok "mutant (HOLD naming a slug the map carries) is killed BY NAME" \
    || bad "killed, but the message does not name content-firewall ($(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/commercial/module-slug-package.json"
if run; then bad "missing JSON stayed CLEAN"; else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing JSON is COULD NOT LOOK"
  else bad "missing JSON should be exit 2 ($(cat "$TMP/err"))"; fi
fi

printf 'check-tier-card selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
