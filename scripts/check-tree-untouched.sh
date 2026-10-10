#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-tree-untouched.sh — un gate que MODIFICA el arbol al comprobarlo no se nota, y en un
# clon compartido lo paga otro.
#
# POR QUE EXISTE, medido el 2026-08-19. `scripts/test-public-counts-verdicts.sh` cambia
# `README.md` EN EL ARBOL a una cifra equivocada —hace falta: es el control negativo, y sin el
# las otras cinco casillas pasarian con un gate que devolviera 2 siempre— y lo devuelve dos
# lineas despues. Entre esas dos lineas hay una ventana, y el `trap` de esa bateria solo
# borraba su directorio de trabajo. Ahora restaura, pero eso arregla UNA bateria.
#
# Lo que no habia era quien lo notase: ni `.githooks/pre-push` ni `mainline-ci` comprobaban el
# arbol DESPUES de correr los gates. Un fichero que un lint deja tocado no lo descubre quien lo
# dejo — lo descubre el `git add -A` de otro carril, ya commiteado, y en `README.md` eso es una
# cifra publica falsa. La REGLA CERO de CLAUDE.md documenta dos casos medidos de exactamente
# esa mecanica, con ficheros de otra sesion barridos por un commit ajeno.
#
# QUE MIDE: que `git status --porcelain` sea IGUAL antes y despues. Ni mas limpio ni mas sucio:
# igual. Un gate que BORRA algo sin trackear tambien esta tocando trabajo que no es suyo.
#
# TRES RESPUESTAS: 0 igual · 1 el arbol cambio (los nombra) · 2 NO HE PODIDO MIRAR.
#
# LIMITES, escritos para que su verde no se lea de mas:
#   · Compara la SALIDA de git status, no el contenido. Dos cambios que se cancelen entre la
#     foto y la comparacion son invisibles — y eso es justo lo que hace bien una bateria que
#     restaura lo que toca, asi que es la lectura correcta y no un hueco.
#   · No ve modificaciones de un fichero YA sucio antes de la foto. El sujeto es el cambio que
#     introduce la corrida, no el estado previo del arbol.
#   · Un `chmod` sobre un fichero trackeado SI se ve (git status reporta el modo).
set -uo pipefail

# ⛔ AISLAMIENTO DEL ENTORNO DE GIT, exigido por `lint:git-env` a cualquier guion que combine
# `mktemp -d` con git — y esta bateria hace exactamente eso: levanta un repo de arena y le
# pregunta a git por su estado. Sin esto, un `GIT_DIR` envenenado en el entorno del llamante
# haria que `git status` respondiera sobre OTRO repositorio, y este gate compararia la foto de
# un arbol con la de otro sin enterarse. Lo caze corriendo los lints rapidos sobre un arbol
# anclado: el guion lo escribi yo anoche y fallaba su propia convencion.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

foto() {
	git -C "$ROOT" status --porcelain 2>/dev/null
}

uso() {
	echo "usage: check-tree-untouched.sh --snapshot <file> | --compare <file> | --selftest" >&2
	exit 2
}

if [ "${1:-}" = "--selftest" ]; then
	fail=0
	caso="$(mktemp -d "${TMPDIR:-/tmp}/tree-untouched.XXXXXX")"
	trap 'rm -rf "$caso"' EXIT
	git init -q "$caso/r" 2>/dev/null
	printf 'uno\n' >"$caso/r/a.txt"
	git -C "$caso/r" add -A 2>/dev/null
	git -C "$caso/r" -c user.name=t -c user.email=t@t commit -qm s 2>/dev/null
	# El script se ancla en dirname/..; se invoca desde un subdirectorio para que ROOT sea el repo.
	# Y lib/git-env.sh viaja CON el: el guion lo carga relativo a su propia carpeta, asi que una
	# copia sin la libreria sale por el FATAL de arranque —2, pero con otro mensaje— y la pata de
	# «NO HE PODIDO MIRAR» pasaba a comprobar el error equivocado. Medido al añadir el aislamiento.
	mkdir -p "$caso/r/scripts/lib" && cp "$0" "$caso/r/scripts/check.sh"
	cp "$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh" "$caso/r/scripts/lib/git-env.sh"

	bash "$caso/r/scripts/check.sh" --snapshot "$caso/f1" >/dev/null 2>&1
	bash "$caso/r/scripts/check.sh" --compare "$caso/f1" >/dev/null 2>&1 && rc=0 || rc=$?
	if [ "$rc" = "0" ]; then echo "  ok    an unchanged tree passes (negative control)"
	else echo "  FAIL  an unchanged tree did not pass (rc=$rc)"; fail=1; fi

	printf 'dos\n' >>"$caso/r/a.txt"
	out="$(bash "$caso/r/scripts/check.sh" --compare "$caso/f1" 2>&1)" && rc=0 || rc=$?
	if [ "$rc" = "1" ] && grep -q 'a.txt' <<<"$out"; then
		echo "  ok    a modified file is detected and named"
	else echo "  FAIL  modification was not detected (rc=$rc)"; fail=1; fi
	git -C "$caso/r" checkout -- a.txt 2>/dev/null

	printf 'x\n' >"$caso/r/suelto.txt"
	out="$(bash "$caso/r/scripts/check.sh" --compare "$caso/f1" 2>&1)" && rc=0 || rc=$?
	if [ "$rc" = "1" ] && grep -q 'suelto.txt' <<<"$out"; then
		echo "  ok    a new untracked file is also detected"
	else echo "  FAIL  new file was not detected (rc=$rc)"; fail=1; fi
	rm -f "$caso/r/suelto.txt"

	# Y la direccion contraria: un gate que BORRA algo que estaba sucio tampoco es inocuo.
	printf 'y\n' >"$caso/r/previo.txt"
	bash "$caso/r/scripts/check.sh" --snapshot "$caso/f2" >/dev/null 2>&1
	rm -f "$caso/r/previo.txt"
	out="$(bash "$caso/r/scripts/check.sh" --compare "$caso/f2" 2>&1)" && rc=0 || rc=$?
	if [ "$rc" = "1" ]; then echo "  ok    deleting an untracked file is detected"
	else echo "  FAIL  deletion went undetected (rc=$rc)"; fail=1; fi

	out="$(bash "$caso/r/scripts/check.sh" --compare "$caso/no-existe" 2>&1)" && rc=0 || rc=$?
	if [ "$rc" = "2" ] && grep -q 'COULD NOT CHECK' <<<"$out"; then
		echo "  ok    an unreadable snapshot returns COULD NOT CHECK, not a pass"
	else echo "  FAIL  missing snapshot did not return the third result (rc=$rc)"; fail=1; fi

	[ "$fail" = "0" ] && { echo "check-tree-untouched selftest: 5 passed, 0 failed"; exit 0; }
	echo "check-tree-untouched selftest: FAILED"; exit 1
fi

modo="${1:-}"; ref="${2:-}"
[ -n "$modo" ] && [ -n "$ref" ] || uso

case "$modo" in
--snapshot)
	if ! foto >"$ref" 2>/dev/null; then
		echo "check-tree-untouched: COULD NOT CHECK — could not write snapshot to '$ref'." >&2
		exit 2
	fi
	exit 0
	;;
--compare)
	if [ ! -r "$ref" ]; then
		echo "check-tree-untouched: COULD NOT CHECK — snapshot is missing or unreadable:" >&2
		echo "  '$ref'. Nothing was compared, so the tree cannot be reported as unchanged." >&2
		exit 2
	fi
	ahora="$(foto)"
	antes="$(cat "$ref" 2>/dev/null)"
	if [ "$ahora" = "$antes" ]; then
		echo "check-tree-untouched: CLEAN — the tree is unchanged after the checks."
		exit 0
	fi
	echo "check-tree-untouched: ⛔ the tree changed during the checks."
	diff <(printf '%s\n' "$antes") <(printf '%s\n' "$ahora") 2>/dev/null | sed 's/^/    /'
	echo
	echo "  A check that modifies the tree can leave changes for another contributor's"
	echo "  'git add -A' to commit. If a suite must mutate a file for its negative control,"
	echo "  restoring it with 'cp' is insufficient; restoration must run in a trap,"
	echo "  as in scripts/test-public-counts-verdicts.sh since 2026-08-19."
	exit 1
	;;
*) uso ;;
esac
