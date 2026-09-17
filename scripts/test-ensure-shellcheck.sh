#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-ensure-shellcheck.sh — bateria hermetica del ELECTOR DE DESTINO de scripts/ensure-shellcheck.sh.
#
# Lo que mide: que el destino donde se instala el shellcheck anclado es un hecho del entorno de
# ejecucion elegido por orden declarado, y nunca una ruta fija de una maquina. Lo que NO mide: la
# descarga ni el anclaje por hash (eso queda como estaba y necesita red). Por eso usa el puerto
# `--destino`, que imprime la eleccion sin bajar nada.
#
# «No escribible» se simula con un FICHERO regular en la ruta candidata: `mkdir -p` falla ahi
# tambien para root, asi que la bateria mide lo mismo en un contenedor de desarrollo y en un runner.
#
# exit 0  todos los casos sostienen · exit 1  un caso fallo · exit 2  no he podido mirar.
set -euo pipefail
LC_ALL=C; export LC_ALL
me=test-ensure-shellcheck
root_dir="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"
helper="$root_dir/scripts/ensure-shellcheck.sh"
[ -f "$helper" ] || { printf '%s: NO HE PODIDO MIRAR — falta %s\n' "$me" "$helper" >&2; exit 2; }
pass=0; fail=0
ok() { pass=$((pass + 1)); printf 'ok - %s\n' "$*"; }
bad() { fail=$((fail + 1)); printf 'not ok - %s\n' "$*"; }
banco="$(mktemp -d "${TMPDIR:-/tmp}/ensure-sc-test.XXXXXX")" || { printf '%s: NO HE PODIDO MIRAR — sin temporal\n' "$me" >&2; exit 2; }
trap 'rm -rf "$banco"' EXIT
# Un entorno LIMPIO por caso: ninguna variable del contenedor que llama se cuela en la medida.
elige() { # [VAR=val ...] → stdout: destino; rc del helper en $rc
	rc=0
	out="$(env -i PATH="$PATH" HOME="$banco/home" TMPDIR="$banco/tmp" "$@" bash "$helper" --destino 2>"$banco/err")" || rc=$?
}
mkdir -p "$banco/home" "$banco/tmp"
bloqueado() { printf '' >"$1"; } # un fichero regular donde se esperaba un directorio

# 1. El explicito gana, tal cual.
elige OLIVARES_BIN_DIR="$banco/explicito" RUNNER_TEMP="$banco/rt"
[ "$rc" = 0 ] && [ "$out" = "$banco/explicito" ] && ok "explicito: OLIVARES_BIN_DIR gana" || bad "explicito: rc=$rc out=$out"

# 2. El explicito no escribible se REHUSA nombrandolo (no se cae a otro sitio).
bloqueado "$banco/explicito-bloq"
elige OLIVARES_BIN_DIR="$banco/explicito-bloq" RUNNER_TEMP="$banco/rt"
[ "$rc" = 2 ] && grep -q "OLIVARES_BIN_DIR=$banco/explicito-bloq" "$banco/err" && ok "explicito no escribible: rc 2 y lo nombra" || bad "explicito no escribible: rc=$rc err=$(cat "$banco/err")"

# 3. La condicion del runner (VM y hospedado): la ruta del contenedor no se puede crear y hay RUNNER_TEMP.
bloqueado "$banco/hub-bloq"
elige OLIVARES_HUB_BIN_DIR="$banco/hub-bloq" RUNNER_TEMP="$banco/rt"
[ "$rc" = 0 ] && [ "$out" = "$banco/rt/olivares-bin" ] && ok "runner: RUNNER_TEMP/olivares-bin cuando la ruta del contenedor no existe" || bad "runner: rc=$rc out=$out"

# 4. El contenedor: sin RUNNER_TEMP y con su ruta escribible, se conserva la convencion.
elige OLIVARES_HUB_BIN_DIR="$banco/hub-ok"
[ "$rc" = 0 ] && [ "$out" = "$banco/hub-ok" ] && ok "contenedor: la ruta convencional cuando es escribible" || bad "contenedor: rc=$rc out=$out"

# 5. RUNNER_TEMP tiene prioridad sobre la ruta del contenedor aunque esta exista (efimero y propio del job).
elige OLIVARES_HUB_BIN_DIR="$banco/hub-ok" RUNNER_TEMP="$banco/rt2"
[ "$rc" = 0 ] && [ "$out" = "$banco/rt2/olivares-bin" ] && ok "runner: RUNNER_TEMP antes que la ruta del contenedor" || bad "prioridad RUNNER_TEMP: rc=$rc out=$out"

# 6. Sin RUNNER_TEMP y sin ruta del contenedor: la cache del usuario (XDG primero).
elige OLIVARES_HUB_BIN_DIR="$banco/hub-bloq" XDG_CACHE_HOME="$banco/xdg"
[ "$rc" = 0 ] && [ "$out" = "$banco/xdg/olivares-bin" ] && ok "usuario: XDG_CACHE_HOME/olivares-bin" || bad "xdg: rc=$rc out=$out"

# 7. Todo bloqueado: rc 2 y se nombran TODOS los probados.
bloqueado "$banco/rt-bloq"; bloqueado "$banco/xdg-bloq"; bloqueado "$banco/tmp-bloq"
rc=0; out="$(env -i PATH="$PATH" HOME="$banco/home" TMPDIR="$banco/tmp-bloq" OLIVARES_HUB_BIN_DIR="$banco/hub-bloq" RUNNER_TEMP="$banco/rt-bloq" XDG_CACHE_HOME="$banco/xdg-bloq" bash "$helper" --destino 2>"$banco/err")" || rc=$?
if [ "$rc" = 2 ] && grep -q "$banco/rt-bloq/olivares-bin" "$banco/err" && grep -q "$banco/hub-bloq" "$banco/err" && grep -q "$banco/xdg-bloq/olivares-bin" "$banco/err" && grep -q "$banco/tmp-bloq/olivares-bin" "$banco/err"; then ok "todo bloqueado: rc 2 con los cuatro probados"; else bad "todo bloqueado: rc=$rc err=$(cat "$banco/err")"; fi

# 8. Mirar no es escribir: `--destino` con la ruta del contenedor bloqueada no crea nada en ella ni en el explicito ausente.
[ ! -d "$banco/hub-bloq" ] && ok "no crea el directorio bloqueado" || bad "creo algo donde no debia"

# 9. Mutante: el guion de produccion no vuelve a llevar la ruta fija como asignacion de destino.
if grep -Eq '^[[:space:]]*dest=/workspace' "$helper"; then bad "mutante: dest=/workspace fijo ha vuelto"; else ok "sin destino fijo en el guion"; fi

printf '%s: %d ok, %d fallo(s)\n' "$me" "$pass" "$fail"
[ "$fail" = 0 ] || exit 1
