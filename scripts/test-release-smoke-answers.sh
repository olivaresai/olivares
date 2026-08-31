#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# La bateria de las TRES RESPUESTAS de release-smoke.sh.
#
# Existe porque el guion solo sabia salir con 1: «no hay artefacto que mirar» y «el
# artefacto esta mal» daban el MISMO codigo. No poder mirar no es un hallazgo, y
# reportarlo como tal tumba un release por una razon que no es del release — una caja sin
# toolchain, un montaje noexec, una ruta equivocada.
#
# El caso que mas importa es el ultimo: si la REFERENCIA con la que se compara el arbol de
# mandatos no se puede construir, antes `set -e` mataba con el rc de `go build` y la
# corrida se leia como «el arbol diverge». Un hallazgo inventado.
#
# 0 limpio · 1 hallazgo · 2 no he podido mirar.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SUT="${OLIVARES_SMOKE_UNDER_TEST:-$ROOT/scripts/release-smoke.sh}"
pass=0; fail=0
ok()  { printf 'ok    %-52s %s\n' "$1" "${2:-}"; pass=$((pass+1)); }
bad() { printf 'FAIL  %-52s %s\n' "$1" "${2:-}"; fail=$((fail+1)); }

W="$(mktemp -d "${TMPDIR:-/var/tmp}/rsa.XXXXXX")" || { echo "sin scratch"; exit 2; }
trap 'rm -rf "$W"' EXIT
# El scratch tiene que poder EJECUTAR: en esta caja /tmp esta montado noexec y el bit de
# ejecucion miente. Si no puede, se rehusa con 2 en vez de dar rojos falsos.
printf '#!/bin/sh\nexit 0\n' > "$W/probe"; chmod +x "$W/probe"
"$W/probe" 2>/dev/null || { echo "test-release-smoke-answers: NO HE PODIDO MIRAR — $W no ejecuta binarios (noexec)" >&2; exit 2; }

# Artefacto que PASA las dos primeras patas, para que los casos lleguen a la tercera.
cat > "$W/olivares" <<'ART'
#!/bin/sh
case "$1" in
  version)   echo "olivares 26.8.0 (commit 1b10cff1095b, built 2026-08-31T06:26:58Z)" ;;
  --version) echo "olivares version 26.8.0" ;;
  firstparty-bins) exit 0 ;;
  commands)  printf 'olivares\nolivares version\nolivares quickstart\n' ;;
esac
ART
cp "$W/olivares" "$W/ref-igual"
sed 's|olivares quickstart|olivares quickstart\\nolivares mandato-de-mas|' "$W/olivares" > "$W/ref-distinta"
chmod +x "$W"/olivares "$W"/ref-igual "$W"/ref-distinta

# `go` emulado: copia la referencia que diga REF_SRC. Asi la pata 3 se ejerce SIN depender
# de que esta caja pueda construir el arbol real (que hoy no puede: -tags release exige los
# binarios de conectores embebidos, y sin ellos el build falla por una razon ajena).
GOOK="$W/gook"; mkdir -p "$GOOK"; cat > "$GOOK/go" <<'G'
#!/bin/sh
dest=""; while [ $# -gt 0 ]; do case "$1" in -o) dest="$2"; shift 2 ;; *) shift ;; esac; done
[ -n "$dest" ] || exit 9
[ -n "${REF_SRC:-}" ] || exit 9
cp "$REF_SRC" "$dest" || exit 9
exit 0
G
GOBAD="$W/gobad"; mkdir -p "$GOBAD"; printf '#!/bin/sh\nexit 3\n' > "$GOBAD/go"
chmod +x "$GOOK/go" "$GOBAD/go"

corre() { ( cd "$ROOT" && "$@" bash "$SUT" ${RUTA:+"$RUTA"} >"$W/out" 2>&1 ); echo $?; }

# 1 · NO-FIRE de la respuesta 0: referencia IDENTICA -> limpio. Sin este caso, un guion que
#     rehusara SIEMPRE pasaria todos los rojos de abajo.
RUTA="$W/olivares"; rc=$(REF_SRC="$W/ref-igual" PATH="$GOOK:$PATH" corre env)
[ "$rc" = 0 ] && ok "no-fire: referencia identica -> limpio" "rc=$rc" \
              || { bad "no-fire: esperaba 0" "rc=$rc"; tail -3 "$W/out" | sed 's/^/       /'; }

# 2 · HALLAZGO: la referencia difiere -> 1, y lo dice
rc=$(REF_SRC="$W/ref-distinta" PATH="$GOOK:$PATH" corre env)
if [ "$rc" = 1 ] && grep -q "command tree diverges" "$W/out"; then
  ok "arbol divergente -> hallazgo (1)" "rc=$rc"
else bad "arbol divergente: esperaba 1 nombrandolo" "rc=$rc"; tail -3 "$W/out" | sed 's/^/       /'; fi

# 3 · EL CASO QUE ESTA BATERIA EXISTE PARA FIJAR: la referencia no se puede CONSTRUIR.
#     Misma entrada que el caso 2; solo cambia si hay toolchain. Antes daba 1.
rc=$(PATH="$GOBAD:$PATH" corre env)
if [ "$rc" = 2 ] && grep -q "COULD NOT LOOK" "$W/out"; then
  ok "referencia no construible -> 2, no un falso 'diverge'" "rc=$rc"
else bad "referencia no construible: esperaba 2" "rc=$rc"; tail -3 "$W/out" | sed 's/^/       /'; fi

# 4..6 · las otras tres puertas de la respuesta 2
RUTA="/no/existe/olivares"; rc=$(corre env)
[ "$rc" = 2 ] && ok "ruta inexistente -> 2" "rc=$rc" || bad "ruta inexistente: esperaba 2" "rc=$rc"
RUTA="/etc"; rc=$(corre env)
[ "$rc" = 2 ] && ok "no es un fichero regular -> 2" "rc=$rc" || bad "directorio: esperaba 2" "rc=$rc"
printf 'no soy un binario\n' > "$W/texto"; chmod +x "$W/texto"
RUTA="$W/texto"; rc=$(corre env)
if [ "$rc" = 2 ] && grep -q "will not execute" "$W/out"; then
  ok "no ejecutable en esta caja (126/127) -> 2" "rc=$rc"
else bad "fichero no ejecutable: esperaba 2" "rc=$rc"; tail -2 "$W/out" | sed 's/^/       /'; fi

# 7 · CONTROL NEGATIVO del propio SUT: un artefacto que corre pero no lleva sello es
#     HALLAZGO (1), no «no he podido mirar». La respuesta 2 no puede tragarse los rojos.
RUTA="/bin/true"; rc=$(corre env)
if [ "$rc" = 1 ] && grep -q "commit not stamped" "$W/out"; then
  ok "binario sin sello sigue siendo hallazgo (1), no 2" "rc=$rc"
else bad "binario sin sello: esperaba 1" "rc=$rc"; tail -2 "$W/out" | sed 's/^/       /'; fi

printf '\ntest-release-smoke-answers: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
