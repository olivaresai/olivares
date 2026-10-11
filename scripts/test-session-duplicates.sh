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
[ -r "$SUT" ] || { echo "FATAL: cannot read $SUT" >&2; exit 2; }

# Keep fixtures outside noexec /tmp. This script currently only writes files, but
# showed a suite can become broken as soon as someone adds an executable.
# Use an owned, verified base directory.
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

echo "--- (1) a duplicate ABSENT from the baseline is RED and named ---"
sembrar "$W/r1" 150 "S2000-primero.md" "S2000-segundo.md"
: > "$W/base-vacia.txt"
correr "$W/r1" "$W/base-vacia.txt"
[ "$rc" -eq 1 ]
check "(1) a new duplicate returns 1" "rc=$rc" $?
case "$out" in *S2000*) true ;; *) false ;; esac
check "(1) FLAGS its number" "names S2000" $?
case "$out" in *S2000-primero.md*) true ;; *) false ;; esac
check "(1) shows BOTH paths, not just the number" "lists files" $?

echo "--- (2) the same tree with the number in the baseline is GREEN ---"
printf 'S2000\n' > "$W/base-200.txt"
correr "$W/r1" "$W/base-200.txt"
[ "$rc" -eq 0 ]
check "(2) a frozen duplicate does not block" "rc=$rc" $?

echo "--- (3) a baseline number that is no longer duplicated requires lowering the baseline ---"
sembrar "$W/r3" 150
printf 'S2000\n' > "$W/base-200b.txt"
correr "$W/r3" "$W/base-200b.txt"
[ "$rc" -eq 0 ]
check "(3) resolved is not a failure" "rc=$rc" $?
case "$out" in *"reduce the baseline"*) true ;; *) false ;; esac
check "(3) ASKS to lower the baseline in the same commit" "says so" $?

echo "--- (4) POSITIVE CONTROL: a tree without sessions is not zero duplicates ---"
sembrar "$W/r4" 3
correr "$W/r4" "$W/base-vacia.txt"
[ "$rc" -eq 2 ]
check "(4) too few files -> COULD NOT LOOK" "rc=$rc" $?

echo "--- (5) no baseline also cannot pass ---"
correr "$W/r1" "$W/no-existe.txt"
[ "$rc" -eq 2 ]
check "(5) missing baseline -> 2, not 0" "rc=$rc" $?

echo "--- (6) an unresolvable ref is not a clean tree ---"
out=$(cd "$W/r1" && OLIVARES_DUP_BASE_REF=no-existe OLIVARES_DUP_BASELINE="$W/base-vacia.txt" bash "$SUT" 2>&1); rc=$?
[ "$rc" -eq 2 ]
check "(6) missing ref -> 2" "rc=$rc" $?

echo "--- (7) unknown arguments are not ignored ---"
out=$(cd "$W/r1" && OLIVARES_DUP_BASE_REF=main OLIVARES_DUP_BASELINE="$W/base-vacia.txt" bash "$SUT" --loquesea 2>&1); rc=$?
[ "$rc" -eq 2 ]
check "(7) unknown argument -> 2" "rc=$rc" $?

echo "--- (8) a task subdirectory counts as a file for its number ---"
sembrar "$W/r8" 150 "S3000-brief.md" "S3000-encargos/E1.md"
correr "$W/r8" "$W/base-vacia.txt"
[ "$rc" -eq 1 ]
check "(8) sessions/S3000-encargos/ counts for S3000" "rc=$rc" $?

echo
echo "session-duplicates: ${PASS} passed, ${FAIL} failed"
[ "$FAIL" -eq 0 ]
