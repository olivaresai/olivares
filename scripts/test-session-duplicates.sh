#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Batería del censo de números duplicados. Corre contra repositorios SEÑUELO construidos
# aquí, nunca contra el árbol vivo: una batería que mida el repositorio real mide el
# repositorio, no la regla, y se pone roja el día que alguien añade un anexo.
set -uo pipefail

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || { echo "FATAL: cannot source $_olivares_git_env" >&2; exit 2; }
unset _olivares_git_env

AQUI=$(cd "$(dirname "$0")" && pwd)
SUT="$AQUI/check-session-duplicates.sh"
[ -r "$SUT" ] || { echo "FATAL: no leo $SUT" >&2; exit 2; }

# ⛔ Los señuelos NO van en /tmp: en este contenedor está montado `noexec`, y aunque este
#    guion sólo escriba ficheros, la lección de es que una batería que confía en /tmp
#    se descubre rota el día que alguien le añade algo ejecutable. Base propia y comprobada.
BASE_TMP="${TMPDIR:-/tmp}"
[ -w "$BASE_TMP" ] || BASE_TMP="${HOME:-/var/tmp}"
W="$(mktemp -d "${BASE_TMP}/olivares-dupcensus-bat.XXXXXX")" || exit 1
trap 'rm -rf "$W"' EXIT HUP INT TERM

PASS=0; FAIL=0
check() { # <titulo> <evidencia> <rc del predicado>
	if [ "$3" -eq 0 ]; then PASS=$((PASS+1)); printf '  ok    %-62s %s\n' "$1" "$2"
	else FAIL=$((FAIL+1)); printf '  FAIL  %-62s %s\n' "$1" "$2"; fi
}

# ⛔ Los numeros de los señuelos llevan CUATRO digitos a proposito. El escrubador del export
# reconoce una referencia interna como S seguida de DOS O TRES digitos con frontera de palabra,
# asi que un numero de tres digitos en un literal de test viaja entero al arbol publico —me paso
# en otra rama, con cuatro fugas— mientras que uno de cuatro no casa. Y el guion bajo prueba SI
# los ve, porque el suyo acepta de dos a cuatro. No los bajes «por realismo»: cambia una fuga por
# nada.
# Un árbol con `n` ficheros de sesión; los nombres extra se pasan como argumentos.
sembrar() { # <dir> <cuantos-unicos> [rutas extra...]
	local d="$1" n="$2"; shift 2
	rm -rf "$d"; mkdir -p "$d"
	git -C "$d" init -q
	git -C "$d" config user.email "b@example.invalid"
	git -C "$d" config user.name "Bateria"
	mkdir -p "$d/sessions"
	local i
	for i in $(seq 1 "$n"); do printf 'x\n' > "$d/sessions/S$((1000+i))-slug-$i.md"; done
	local extra
	for extra in "$@"; do mkdir -p "$d/sessions/$(dirname "$extra")"; printf 'x\n' > "$d/sessions/$extra"; done
	git -C "$d" add sessions >/dev/null 2>&1
	git -C "$d" commit -qm "seed" >/dev/null 2>&1
	git -C "$d" branch -f main HEAD >/dev/null 2>&1
}

correr() { # <dir> <fichero de base> -> imprime salida, deja rc en $rc
	out=$(cd "$1" && OLIVARES_DUP_BASE_REF=main OLIVARES_DUP_BASELINE="$2" bash "$SUT" 2>&1); rc=$?
}

echo "--- (1) un duplicado que NO esta en la base es ROJO y se nombra ---------------------"
sembrar "$W/r1" 150 "S2000-primero.md" "S2000-segundo.md"
: > "$W/base-vacia.txt"
correr "$W/r1" "$W/base-vacia.txt"
[ "$rc" -eq 1 ]
check "(1) un duplicado nuevo sale 1" "rc=$rc" $?
case "$out" in *S2000*) true ;; *) false ;; esac
check "(1) lo ACUSA por su numero" "nombra S2000" $?
case "$out" in *S2000-primero.md*) true ;; *) false ;; esac
check "(1) y enseña las DOS rutas, no solo el numero" "lista ficheros" $?

echo "--- (2) el mismo arbol con el numero en la base es VERDE ----------------------------"
printf 'S2000\n' > "$W/base-200.txt"
correr "$W/r1" "$W/base-200.txt"
[ "$rc" -eq 0 ]
check "(2) un duplicado congelado no bloquea" "rc=$rc" $?

echo "--- (3) un numero de la base que ya NO esta duplicado pide bajar la base ------------"
sembrar "$W/r3" 150
printf 'S2000\n' > "$W/base-200b.txt"
correr "$W/r3" "$W/base-200b.txt"
[ "$rc" -eq 0 ]
check "(3) resuelto no es un fallo" "rc=$rc" $?
case "$out" in *"baja la línea base"*) true ;; *) false ;; esac
check "(3) y PIDE bajar la base en el mismo commit" "lo dice" $?

echo "--- (4) CONTROL POSITIVO: un arbol sin sesiones no es 'cero duplicados' -------------"
sembrar "$W/r4" 3
correr "$W/r4" "$W/base-vacia.txt"
[ "$rc" -eq 2 ]
check "(4) pocos ficheros -> NO HE PODIDO MIRAR" "rc=$rc" $?

echo "--- (5) sin linea base tampoco se aprueba nada --------------------------------------"
correr "$W/r1" "$W/no-existe.txt"
[ "$rc" -eq 2 ]
check "(5) base ausente -> 2, no 0" "rc=$rc" $?

echo "--- (6) un ref que no resuelve no es un arbol limpio --------------------------------"
out=$(cd "$W/r1" && OLIVARES_DUP_BASE_REF=no-existe OLIVARES_DUP_BASELINE="$W/base-vacia.txt" bash "$SUT" 2>&1); rc=$?
[ "$rc" -eq 2 ]
check "(6) ref ausente -> 2" "rc=$rc" $?

echo "--- (7) un argumento desconocido no se ignora ---------------------------------------"
out=$(cd "$W/r1" && OLIVARES_DUP_BASE_REF=main OLIVARES_DUP_BASELINE="$W/base-vacia.txt" bash "$SUT" --loquesea 2>&1); rc=$?
[ "$rc" -eq 2 ]
check "(7) argumento desconocido -> 2" "rc=$rc" $?

echo "--- (8) un subdirectorio de encargos cuenta como fichero del numero -----------------"
sembrar "$W/r8" 150 "S3000-brief.md" "S3000-encargos/E1.md"
correr "$W/r8" "$W/base-vacia.txt"
[ "$rc" -eq 1 ]
check "(8) sessions/S3000-encargos/ cuenta para S3000" "rc=$rc" $?

echo
echo "session-duplicates: ${PASS} pasan, ${FAIL} fallan"
[ "$FAIL" -eq 0 ]
