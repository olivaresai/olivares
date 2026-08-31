#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# test-inbox-headings.sh — la bateria de `check-inbox-headings.sh`.
#
# ⛔ POR QUE EXISTE, y no es completitud: el gate acaba de crecer un control del SELLO, y un
#    control que nadie ve cortar es una intencion, no una guarda. El mutante se corrio a mano una
#    vez; esta bateria lo deja corriendo siempre.
#
# ⛔ Y EL CASO QUE MAS IMPORTA ES EL (f): una entrada VIEJA sin sello **no** puede enrojecer. Las
#    21 cabeceras con `__SELLO__` literal que hay hoy en el tronco son asientos ajenos ya
#    publicados; un gate que enrojeciera por ellas bloquearia a los cinco carriles por deuda que
#    su autor no puede reparar desde su rama. Ese es el fallo exacto por el que
#    `lint:unpublished-work` se retiro de este hook DOS veces. Si (f) se pone rojo, el gate ha
#    dejado de mirar el diff y ha pasado a mirar el fichero entero.
set -uo pipefail

# ⛔ AISLAMIENTO DEL ENTORNO GIT, y no es ceremonia: esta bateria empareja `mktemp -d` con `git
#    init`, y git EXPORTA `GIT_DIR` a sus hooks desde cualquier worktree ENLAZADO — es decir,
#    desde toda sesion en paralelo de este repo. Sin sanear, los repos senuelo se conducirian
#    contra el repo VIVO. Ya paso el 2026-08-06 y dejo la rama de un PR apuntando a un commit de
#    fixture. Lo exige `lint:git-env`, que ademas lo verifica POR MUTACION: rompe el saneo a
#    proposito y comprueba que el dano se ve.
#
#    Fail-closed a proposito: un saneador que no se puede cargar es «no he podido aislar», nunca
#    «no hacia falta aislar».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

GATE="${GATE_SRC:-scripts/check-inbox-headings.sh}"
[ -r "$GATE" ] || { echo "test-inbox-headings: 2 NO HE PODIDO MIRAR — no leo $GATE" >&2; exit 2; }
GATE_ABS="$(CDPATH= cd -- "$(dirname -- "$GATE")" && pwd)/$(basename -- "$GATE")"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/inbox-headings.XXXXXX")" ||
	{ echo "test-inbox-headings: 2 NO HE PODIDO MIRAR — sin temporal" >&2; exit 2; }
# shellcheck disable=SC2064
trap "rm -rf '$WORK'" EXIT

pass=0; fail=0
ok()  { pass=$((pass+1)); printf '  ok    %-56s %s\n' "$1" "${2:-}"; }
bad() { fail=$((fail+1)); printf '  FAIL  %-56s %s\n' "$1" "${2:-}"; }

# Un buzon minimo pero VALIDO: las cinco secciones que el protocolo fija.
cuerpo_valido() {
	printf '# Buzon\n\n## Cómo escribir aquí\n\n## Qué SÍ mandar aquí\n\n## Qué NO\n\n## Pendientes\n\n## Atendidos\n'
}

# Cada caso corre en su PROPIO repo: el gate compara contra `merge-base`, asi que necesita
# historia de verdad. Un fixture sin tronco probaria otra cosa.
montar() { # montar <dir> <contenido-inicial-del-buzon>
	local d="$1" inicial="$2"
	mkdir -p "$d/sessions/status/inbox" "$d/scripts"
	git -c init.defaultBranch=main init -q "$d" >/dev/null 2>&1 || return 1
	git -C "$d" config user.email t@example.invalid
	git -C "$d" config user.name t
	git -C "$d" config commit.gpgsign false
	cp "$GATE_ABS" "$d/scripts/check-inbox-headings.sh"
	printf '%s' "$inicial" > "$d/sessions/status/inbox/PRUEBA.md"
	git -C "$d" add -A >/dev/null 2>&1
	git -C "$d" commit -q -m tronco --no-verify >/dev/null 2>&1 || return 1
	git -C "$d" branch -q -f tronco-de-prueba HEAD
	return 0
}

correr() { # correr <dir> -> imprime rc
	( cd "$1" && OLIVARES_INBOX_TRUNK=tronco-de-prueba bash scripts/check-inbox-headings.sh >"$WORK/salida" 2>&1 )
	printf '%s' "$?"
}

# (a) CONTROL POSITIVO — un buzon bien formado pasa. Va primero: si esto no sale verde, ningun
#     rojo de abajo significa nada, porque no sabriamos si el gate esta juzgando.
d="$WORK/a"; montar "$d" "$(cuerpo_valido)" || { echo "test-inbox-headings: 2 NO HE PODIDO MIRAR — fixture" >&2; exit 2; }
[ "$(correr "$d")" = "0" ] && ok "(a) CONTROL POSITIVO: un buzon valido pasa" ||
	bad "(a) CONTROL POSITIVO: un buzon valido NO pasa — el resto no vale" "$(head -2 "$WORK/salida")"

# (b) una `##` que no es de las cinco.
d="$WORK/b"; montar "$d" "$(cuerpo_valido)"
printf '\n## Inventada\n' >> "$d/sessions/status/inbox/PRUEBA.md"
[ "$(correr "$d")" = "1" ] && ok "(b) una '##' fuera de las cinco enrojece" ||
	bad "(b) una '##' inventada NO enrojece"

# (c) falta una de las cinco.
d="$WORK/c"; montar "$d" "$(cuerpo_valido | grep -v '^## Atendidos$')"
[ "$(correr "$d")" = "1" ] && ok "(c) falta una seccion estructural y enrojece" ||
	bad "(c) un buzon sin sus cinco secciones NO enrojece"

# (d) EL SELLO — una entrada NUEVA con el placeholder literal.
d="$WORK/d"; montar "$d" "$(cuerpo_valido)"
printf '\n### __SELLO__ · X → Y · sin sustituir\n\ncuerpo\n' >> "$d/sessions/status/inbox/PRUEBA.md"
git -C "$d" commit -q --no-verify -am 'entrada sin sello' >/dev/null 2>&1
rc="$(correr "$d")"
if [ "$rc" = "1" ] && grep -q 'sin sello ordenable' "$WORK/salida"; then
	ok "(d) entrada NUEVA con '__SELLO__' enrojece Y la nombra"
else
	bad "(d) una entrada nueva sin sello no enrojece o no la nombra" "rc=$rc"
fi

# (e) y una entrada NUEVA con sello valido pasa: la guarda no puede prohibir escribir.
d="$WORK/e"; montar "$d" "$(cuerpo_valido)"
printf '\n### 2026-08-28T14:30Z · X → Y · con sello\n\ncuerpo\n' >> "$d/sessions/status/inbox/PRUEBA.md"
git -C "$d" commit -q --no-verify -am 'entrada con sello' >/dev/null 2>&1
[ "$(correr "$d")" = "0" ] && ok "(e) entrada NUEVA con sello valido pasa" ||
	bad "(e) una entrada bien sellada enrojece — la guarda prohibe escribir"

# (f) ⛔ EL QUE PROTEGE A LOS CINCO CARRILES: una entrada VIEJA sin sello, ya en el tronco, NO
#     enrojece. Si este caso cae, el gate ha dejado de mirar el DIFF y mira el fichero entero, y
#     bloquearia a todo el mundo por 21 asientos ajenos que nadie puede reparar desde su rama.
d="$WORK/f"; montar "$d" "$(cuerpo_valido)$(printf '\n### __SELLO__ · viejo → nadie · ya publicado\n\ncuerpo\n')"
printf '\n### 2026-08-28T15:00Z · X → Y · entrada nueva y correcta\n\ncuerpo\n' >> "$d/sessions/status/inbox/PRUEBA.md"
git -C "$d" commit -q --no-verify -am 'entrada nueva sobre un tronco con deuda' >/dev/null 2>&1
[ "$(correr "$d")" = "0" ] && ok "(f) una entrada VIEJA sin sello NO enrojece (deuda ajena)" ||
	bad "(f) el gate enrojece por deuda del TRONCO — bloquearia a los cinco carriles"

# (g) sin base contra el tronco: lo dice y no inventa un veredicto de sellos.
d="$WORK/g"; montar "$d" "$(cuerpo_valido)"
printf '\n### __SELLO__ · X → Y · sin sustituir\n' >> "$d/sessions/status/inbox/PRUEBA.md"
git -C "$d" commit -q --no-verify -am 'sin base' >/dev/null 2>&1
( cd "$d" && OLIVARES_INBOX_TRUNK=no-existe-este-ref bash scripts/check-inbox-headings.sh >"$WORK/salida" 2>&1 )
rc=$?
if [ "$rc" = "0" ] && grep -q 'could not look' "$WORK/salida"; then
	ok "(g) sin base lo DICE y no dictamina sobre sellos"
else
	bad "(g) sin base dictamina igual o falla mal" "rc=$rc"
fi

printf '\ntest-inbox-headings: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" = "0" ]
