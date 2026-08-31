#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# a repository gate / openapi:check — testigo del aprovisionamiento y de la guarda con nombre.
#
# QUE MIDE, y por que existe. El 2026-08-30, corrida 33284926144 (job control-plane
# 99186359739, SHA 417564d0d), el paso «OpenAPI snapshot + web client codegen drift» salio
# ROJO con `sh: line 1: openapi-typescript: command not found`. No habia deriva ninguna:
# sobre ese mismo arbol, con las dependencias instaladas, `task openapi:check` da rc 0 y el
# diff de los tres ficheros que juzga es de CERO bytes. Lo que habia era (1) un rojo previo
# —SPDX, paso 17— que salto los pasos 18-49, incluido el 31 que INSTALA las dependencias, y
# (2) una guarda `if:` en el consumidor copiada de `vuln-gate` con el predicado de `vuln-gate`
# (`steps.tools`, go-task) en vez del suyo. El consumidor se des-salto; su proveedor no.
#
# Esta bateria ata las dos mitades de la cura y NO puede pasar por casualidad:
#   · la guarda del Taskfile se EJECUTA de verdad, en un arbol de mentira, con la herramienta
#     y sin ella, y se le exige que NOMBRE lo que falta (no basta con que falle);
#   · las dos `if:` del workflow se leen del YAML, no por grep de prosa;
#   · cada afirmacion tiene su MUTANTE: se rompe la cura en el fichero copiado y se exige que
#     esta bateria lo vea. Un mutante que sobrevive es un control que no controla.
#
# Contrato de salida: 0 limpio · 1 hallazgo · 2 NO HE PODIDO MIRAR.
set -u

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TF="$ROOT/Taskfile.yml"
WF="$ROOT/.github/workflows/mainline-ci.yml"
WORK=$(mktemp -d "${TMPDIR:-/tmp}/openapi-prov.XXXXXX") || { echo "no pude crear el area de trabajo"; exit 2; }
trap 'rm -rf "$WORK"' EXIT

pass=0; fail=0
ok(){ printf 'ok    %s\n' "$1"; pass=$((pass+1)); }
no(){ printf 'FAIL  %s\n' "$1"; fail=$((fail+1)); }
cannot(){ printf 'NO HE PODIDO MIRAR: %s\n' "$1"; exit 2; }

command -v python3 >/dev/null 2>&1 || cannot "sin python3 no puedo leer el YAML"
PEEK="$ROOT/scripts/lib/ci-yaml-peek.py"
[ -r "$PEEK" ] || cannot "falta scripts/lib/ci-yaml-peek.py, que es como leo estos YAML"
# ⛔ SIN PyYAML A PROPOSITO. Este guion corre como paso de mainline-ci, y ese job vive en un
# runner AUTOALOJADO (hetzner/srv17), no en una imagen de GitHub. Medido el 2026-08-30 sobre el
# arbol entero: de todos los `run:` de todos los workflows, UNO usa python y solo importa
# stdlib. Nada demuestra que PyYAML se alcance alli, y un gate nuevo que lo diera por hecho
# seria la misma clase de dependencia de entorno sin medir que este claim cura un piso arriba.

# ---------------------------------------------------------------- extractores (del YAML)

# Imprime el bloque shell de la guarda de web:codegen. rc 3 = no esta (lo usan los mutantes).
guard_block() {
  python3 "$PEEK" task-cmd "$1" web:codegen 'node_modules/.bin'
}

# Imprime la expresion `if:` del paso con ese id dentro del job control-plane.
step_if() {
  python3 "$PEEK" step-field "$1" control-plane "$2" if
}

# Arbol de mentira con los binarios que se le pidan en web/node_modules/.bin.
fake_tree() {
  local d; d=$(mktemp -d "$WORK/tree.XXXXXX") || return 1
  mkdir -p "$d/web/node_modules/.bin" || return 1
  local t
  for t in "$@"; do
    printf '#!/bin/sh\nexit 0\n' > "$d/web/node_modules/.bin/$t" || return 1
    chmod +x "$d/web/node_modules/.bin/$t" || return 1
  done
  printf '%s' "$d"
}

# ---------------------------------------------------------------- 1. la guarda, EJECUTADA

G="$WORK/guard.sh"
if ! guard_block "$TF" > "$G" 2>"$WORK/g.err"; then
  cannot "no encuentro la guarda de web:codegen en Taskfile.yml ($(head -1 "$WORK/g.err"))"
fi
[ -s "$G" ] || cannot "la guarda de web:codegen salio vacia"

if bash -n "$G" 2>"$WORK/n.err"; then
  ok "el bloque de la guarda es shell valido (bash -n)"
else
  no "la guarda no pasa bash -n: $(head -1 "$WORK/n.err")"
fi

run_guard() { ( cd "$1" && bash "$G" ) >"$WORK/out.txt" 2>&1; printf '%s' "$?"; }

T_OK=$(fake_tree openapi-typescript prettier) || cannot "no pude montar el arbol completo"
rc=$(run_guard "$T_OK")
if [ "$rc" = 0 ]; then ok "con las dos herramientas presentes la guarda deja pasar (rc 0)"
else no "con las herramientas presentes la guarda corta igual (rc $rc): $(head -1 "$WORK/out.txt")"; fi

T_NONE=$(fake_tree) || cannot "no pude montar el arbol vacio"
rc=$(run_guard "$T_NONE")
if [ "$rc" = 1 ]; then ok "sin ninguna herramienta la guarda corta fail-closed (rc 1)"
else no "sin herramientas la guarda no corta con rc 1 (rc $rc)"; fi
if command grep -q 'openapi-typescript' "$WORK/out.txt"; then
  ok "y NOMBRA openapi-typescript (no un 'command not found' del siguiente)"
else no "corta sin nombrar openapi-typescript: $(head -2 "$WORK/out.txt")"; fi
if command grep -q 'pnpm --dir web install --frozen-lockfile' "$WORK/out.txt"; then
  ok "y dice COMO conseguirlas (el comando exacto de instalacion)"
else no "no dice como conseguir las herramientas"; fi
if command grep -qi 'no es una deriva' "$WORK/out.txt"; then
  ok "y desmiente explicitamente la lectura falsa ('no es una deriva del snapshot')"
else no "no desmiente la lectura de deriva, que es el coste medido del defecto"; fi

T_HALF=$(fake_tree openapi-typescript) || cannot "no pude montar el arbol a medias"
rc=$(run_guard "$T_HALF")
if [ "$rc" = 1 ] && command grep -q 'prettier' "$WORK/out.txt" \
   && ! command grep -q ' openapi-typescript' "$WORK/out.txt"; then
  ok "con openapi-typescript presente y prettier ausente nombra SOLO prettier"
else no "no distingue cual de las dos falta (rc $rc): $(head -1 "$WORK/out.txt")"; fi

# ---------------------------------------------------------------- 2. las dos `if:` del workflow

IF_PROV=$(step_if "$WF" openapi-web-deps) || cannot "no encuentro el paso openapi-web-deps"
IF_CONS=$(step_if "$WF" openapi-check)   || cannot "no encuentro el paso openapi-check"

case "$IF_PROV" in
  *steps.node.outcome*steps.pnpm.outcome*)
    ok "el proveedor (openapi-web-deps) nombra SU predicado: node y pnpm" ;;
  '') no "el proveedor sigue sin guarda: un rojo ajeno lo vuelve a saltar" ;;
  *)  no "el proveedor lleva una guarda que no nombra node+pnpm: $IF_PROV" ;;
esac
case "$IF_PROV" in
  *'!cancelled()'*) ok "y usa !cancelled(), no always() (una cancelacion no es un veredicto)" ;;
  *) no "el proveedor no usa !cancelled(): $IF_PROV" ;;
esac
case "$IF_CONS" in
  *steps.openapi-web-deps.outcome*)
    ok "el consumidor (openapi-check) nombra a su proveedor, no solo a steps.tools" ;;
  *) no "el consumidor NO nombra openapi-web-deps: puede volver a correr sin herramienta ($IF_CONS)" ;;
esac
case "$IF_CONS" in
  *steps.tools.outcome*) ok "y conserva steps.tools, que tambien le hace falta (go-task)" ;;
  *) no "el consumidor perdio steps.tools al corregirlo: $IF_CONS" ;;
esac

# ---------------------------------------------------------------- 3. mutantes

mut_check() { # <etiqueta> <fichero-mutado> <TF|WF>
  local label=$1 f=$2 kind=$3 out
  if [ "$kind" = TF ]; then
    out=$(guard_block "$f" 2>/dev/null); local grc=$?
    if [ $grc -ne 0 ]; then printf 'MUERTO %s (la bateria ya no encuentra la guarda)\n' "$label"; return 0; fi
    printf '%s' "$out" > "$WORK/mut-guard.sh"
    local rc2; rc2=$( ( cd "$T_NONE" && bash "$WORK/mut-guard.sh" ) >/dev/null 2>&1; printf '%s' "$?" )
    if [ "$rc2" != 1 ]; then printf 'MUERTO %s (la guarda mutada deja de cortar)\n' "$label"; return 0; fi
    printf 'SOBREVIVE %s\n' "$label"; return 1
  else
    local p c; p=$(step_if "$f" openapi-web-deps 2>/dev/null); c=$(step_if "$f" openapi-check 2>/dev/null)
    case "$p" in *steps.node.outcome*steps.pnpm.outcome*) ;; *) printf 'MUERTO %s (proveedor)\n' "$label"; return 0;; esac
    case "$c" in *steps.openapi-web-deps.outcome*) ;; *) printf 'MUERTO %s (consumidor)\n' "$label"; return 0;; esac
    printf 'SOBREVIVE %s\n' "$label"; return 1
  fi
}

M_TF="$WORK/mut-Taskfile.yml"; M_WF="$WORK/mut-mainline.yml"

# M1 — se borra la guarda del Taskfile.
python3 - "$TF" "$M_TF" <<'PY'
import sys
s = open(sys.argv[1], encoding='utf-8').read()
i = s.index('      - cmd: |\n          missing=\"\"')
j = s.index('      - pnpm --dir web run codegen', i)
open(sys.argv[2], 'w', encoding='utf-8').write(s[:i] + s[j:])
PY
if mut_check "M1 guarda del Taskfile borrada" "$M_TF" TF; then ok "M1: el mutante muere"; else no "M1 SOBREVIVE"; fi

# M2 — la guarda existe pero no nombra nada (falla muda).
python3 - "$TF" "$M_TF" <<'PY'
import sys, re
s = open(sys.argv[1], encoding='utf-8').read()
s = re.sub(r'\n *echo "::error::web:codegen[^\n]*\n *echo "Instalalas[^\n]*\n *echo "\(esto NO[^\n]*', '', s, count=1)
open(sys.argv[2], 'w', encoding='utf-8').write(s)
PY
G_KEEP=$G; G="$WORK/mut-guard-mudo.sh"
guard_block "$M_TF" > "$G" 2>/dev/null
rc=$(run_guard "$T_NONE")
if [ "$rc" = 1 ] && ! command grep -q 'openapi-typescript' "$WORK/out.txt"; then
  ok "M2: una guarda que corta MUDA es distinguible (falla sin nombrar) — la bateria lo exige arriba"
else no "M2: no distingo una guarda muda de una que nombra (rc $rc)"; fi
G=$G_KEEP

# M3 — el proveedor se queda sin `if:` (el estado de ayer).
python3 - "$WF" "$M_WF" <<'PY'
import sys, re
s = open(sys.argv[1], encoding='utf-8').read()
s = s.replace("""        if: ${{ !cancelled() && steps.node.outcome == 'success' && steps.pnpm.outcome == 'success' }}
        timeout-minutes: 10""", "        timeout-minutes: 10", 1)
open(sys.argv[2], 'w', encoding='utf-8').write(s)
PY
if mut_check "M3 proveedor sin guarda" "$M_WF" WF; then ok "M3: el mutante muere"; else no "M3 SOBREVIVE"; fi

# M4 — el consumidor vuelve al predicado de vuln-gate (el defecto exacto que se cura).
python3 - "$WF" "$M_WF" <<'PY'
import sys
s = open(sys.argv[1], encoding='utf-8').read()
s = s.replace("!cancelled() && steps.tools.outcome == 'success' && steps.openapi-web-deps.outcome == 'success'",
              "!cancelled() && steps.tools.outcome == 'success'", 1)
open(sys.argv[2], 'w', encoding='utf-8').write(s)
PY
if mut_check "M4 consumidor con el predicado de vuln-gate" "$M_WF" WF; then ok "M4: el mutante muere"; else no "M4 SOBREVIVE"; fi

# CONTROL — se muta EL GUION, no el sujeto: si la bateria mira un fichero que no tiene el paso,
# tiene que decir NO HE PODIDO MIRAR (rc 3 del extractor), no dar por buena la ausencia.
printf 'jobs:\n  control-plane:\n    steps:\n      - name: nada\n' > "$WORK/vacio.yml"
step_if "$WORK/vacio.yml" openapi-check >/dev/null 2>&1
if [ $? -eq 3 ]; then ok "CONTROL: sobre un workflow sin el paso, el extractor dice que no puede mirar"
else no "CONTROL: la ausencia del paso se lee como algo distinto de 'no puedo mirar'"; fi

printf '\ntest-openapi-check-provisioning: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
