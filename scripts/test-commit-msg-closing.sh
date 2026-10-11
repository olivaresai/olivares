#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-commit-msg-closing.sh — la guarda del hook `commit-msg` contra los cierres accidentales.
#
# QUÉ PROTEGE. Un commit que cambiaba UN fichero de documentación y ningún código llevaba en su
# cuerpo la frase «... and is closed: #484 ...». GitHub leyó `closed: #484` y cerró un pull
# request vivo con 25 ficheros de trabajo. El cierre se deshizo en un minuto; el daño real fue el
# SILENCIO — durante una hora se dio por entregado un trabajo que seguía sin integrar, porque
# nada avisó de que aquel pull request ya no estaba abierto.
#
# LO QUE ESTA BATERÍA EXIGE, y es lo que hace que sirva: cada aserción va acompañada de su
# MUTANTE. Una batería que solo comprueba que el caso bueno pasa y el malo falla no prueba que la
# guarda discrimine: prueba que el fichero existe. Aquí, por cada rama load-bearing, se rompe esa
# rama en una COPIA desechable del hook y se comprueba que el veredicto cambia.
set -euo pipefail

# The ambient git environment OUTRANKS `-C`: with GIT_DIR exported — which git does
# from every LINKED worktree, i.e. from every parallel session — this script's throwaway
# repositories would be driven into the LIVE repository instead. Measured 2026-08-06;
# it left the branch of PR #526 pointing at a fixture commit. Fail closed: a missing
# sanitiser is "I could not isolate", never "isolation was not needed".
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env
export LC_ALL=C

HOOK_SRC="${HOOK_SRC:-.githooks/commit-msg}"
[ -f "$HOOK_SRC" ] || { printf 'cannot find %s\n' "$HOOK_SRC" >&2; exit 2; }
HOOK_ABS="$(cd "$(dirname "$HOOK_SRC")" && pwd)/$(basename "$HOOK_SRC")"

pass=0; fail=0
check() { # check <descripción> <detalle> <rc-esperado> <rc-obtenido>
	if [ "$3" = "$4" ]; then
		printf '  ok    %-62s %s\n' "$1" "$2"; pass=$((pass + 1))
	else
		printf '  FAIL  %-62s %s (expected %s, got %s)\n' "$1" "$2" "$3" "$4"; fail=$((fail + 1))
	fi
}

WORK="$(mktemp -d "${TMPDIR:-/tmp}/commitmsg-closing.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/.githooks" "$WORK/scripts"
cp "$(dirname "$HOOK_ABS")/../scripts/check-commit-trailers.sh" "$WORK/scripts/"

# Use a disposable real Git repository: the guard queries the index.
# Fixture paths must not be real internal paths. This function creates files in a
# temporary git init, but check-export-closure.sh reads this script statically and
# cannot distinguish created fixtures from live dependencies. an internal design note (not shipped)
# caused five of main's six false closure failures for days. The commit-msg guard
# classifies only by extension (.githooks/commit-msg:49, grep -qvE '(\.md|\.txt)$');
# use nonexistent paths, as sibling fixtures modules/x/main.go and docs/guide.txt do.
repo() { # repo <ficheros-a-estacionar...>
	rm -rf "$WORK/r"; mkdir -p "$WORK/r"; cd "$WORK/r"
	git init -q .
	git config user.email "t@example.invalid"; git config user.name "T"
	for f in "$@"; do mkdir -p "$(dirname "$f")"; printf 'x\n' > "$f"; git add "$f"; done
	cd - >/dev/null
}

run() { # run <hook> <mensaje> -> imprime rc
	printf '%s\n\nSigned-off-by: Test <test@example.invalid>\n' "$2" > "$WORK/msg"
	( cd "$WORK/r" && sh "$1" "$WORK/msg" >/dev/null 2>&1 ) && printf 0 || printf $?
}

# La copia mutada permite comprobar que la aserción cae POR SU RAZÓN y no por otra.
mutant() { # mutant <sed-expr> -> ruta del hook mutado
	sed "$1" "$HOOK_ABS" > "$WORK/.githooks/mutant"; printf '%s' "$WORK/.githooks/mutant"
}

printf '\n== the guard rejects what it should ==\n'
repo "docs/board.md"
for kw in "closed: #484" "closes #7" "close #7" "fixes #123" "fixed #123" "fix #123" \
	"resolves #9" "resolved #9" "resolve #9" "Closes #7" "CLOSED: #484"; do
	rc="$(run "$HOOK_ABS" "docs(status): board update

the message says $kw and that is the problem")"
	check "rejects «$kw» in a docs-only commit" "rc=$rc" 1 "$rc"
done

printf '\n== the guard does NOT reject valid input ==\n'
repo "docs/board.md"
rc="$(run "$HOOK_ABS" "docs(status): board update

the pull request #484 is closed, and its closure is recorded here")"
check "accepts the SAME idea without the trigger word" "rc=$rc" 0 "$rc"

rc="$(run "$HOOK_ABS" "docs(status): board update

see #484 and #7 for context")"
check "accepts bare issue number references" "rc=$rc" 0 "$rc"

# El caso que separa esta guarda de una prohibición: un commit CON código sí puede cerrar.
repo "modules/x/main.go" "docs/board.md"
rc="$(run "$HOOK_ABS" "fix(x): the thing

closes #7")"
check "accepts closure in a commit that DOES contain code" "rc=$rc" 0 "$rc"

repo "docs/guide.txt"
rc="$(run "$HOOK_ABS" "docs(guide): text

closes #7")"
check "also covers .txt, not just .md" "rc=$rc" 1 "$rc"

printf '\n== MUTATION VERIFICATION — each branch fails for ITS reason ==\n'
repo "docs/board.md"

m="$(mutant 's/clos(e|es|ed)|fix(|es|ed)|resolv(e|es|ed)/__nunca__/')"
rc="$(run "$m" "docs(status): x

closes #7")"
check "MUTANT: no keyword set -> accepts" "rc=$rc" 0 "$rc"

m="$(mutant 's/grep -inE/grep -nE/')"
rc="$(run "$m" "docs(status): x

CLOSES #7")"
check "MUTANT: no -i -> uppercase escapes" "rc=$rc" 0 "$rc"

# OJO al sentido del mutante: sustituir la CONDICIÓN por `true` haría que la función
# retornase 0 antes de mirar nada, que es dejar pasar más, no menos. Para comprobar que la
# excepción de código es load-bearing hay que hacerla FALSA, y entonces un commit con código
# debe empezar a ser rechazado. Un mutante mal orientado no prueba nada: parece verde por la
# razón contraria.
m="$(mutant 's/^  if printf .*grep -qvE.*then$/  if false; then/')"
repo "modules/x/main.go"
rc="$(run "$m" "fix(x): y

closes #7")"
check "MUTANT: no code exemption -> rejects a valid commit" "rc=$rc" 1 "$rc"

repo "docs/board.md"
m="$(mutant 's/^closing_keyword_guard || exit 1$/:/')"
rc="$(run "$m" "docs(status): x

closes #7")"
check "MUTANT: guard not invoked -> accepts" "rc=$rc" 0 "$rc"

printf '\ncommit-msg-closing: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
