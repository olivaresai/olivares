#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-private-leg.sh — both halves of scripts/private-leg.sh on throwaway trees:
# the guard must RUN (and propagate exit codes exactly) when the directory
# exists, say NOT APPLICABLE visibly when the tree is a stamped public export,
# and refuse to guess when it is neither. Every red row is exercised, not
# assumed: the battery builds each tree shape and asserts the exit code AND the
# words the operator would read. Hermetic: no network, no toolchain, bash +
# coreutils only; safe under a noexec TMPDIR (the guard is always read by bash,
# never execve'd from the fixture tree).
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
pass=0
fail=0
check() { # check <label> <expect> <rc>
	if [ "$3" -eq 0 ]; then
		pass=$((pass + 1))
		printf '  ok    %s\n' "$1"
	else
		fail=$((fail + 1))
		printf '  FAIL  %-58s %s\n' "$1" "$2"
	fi
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

mk_tree() { # mk_tree <name> [marker] -> a tree carrying the REAL guard script
	local t="$TMP/$1"
	mkdir -p "$t/scripts"
	cp "$ROOT/scripts/private-leg.sh" "$t/scripts/"
	[ "${2:-}" = marker ] && echo "# public-export marker (fixture)" >"$t/PUBLIC-EXPORT.md"
	echo "$t"
}

# 1) directory present: the command executes INSIDE the directory, not beside it
t="$(mk_tree hub)"
# CON CONTENIDO, y no un `mkdir -p` a secas: desde 2026-08-19 un directorio VACIO cuenta como
# ausente, porque go-task FABRICA el `dir:` de una tarea que falla y ese resto dejaba ciega la
# guarda para siempre (ver la cabecera de private-leg.sh). Una pierna presente de verdad tiene
# ficheros; un directorio vacio es justamente el estado envenenado que ahora se rechaza.
mkdir -p "$t/cloud/control-plane"
: >"$t/cloud/control-plane/go.mod"
out="$(bash "$t/scripts/private-leg.sh" probe cloud/control-plane pwd)"
[ "$out" = "$t/cloud/control-plane" ]
check "dir present: command executes inside the leg directory" "pwd inside the leg dir" $?

# 2) directory present: the command's exit code survives untouched (the go-run
#    lesson: a wrapper that flattens exit codes turns three answers into two)
bash "$t/scripts/private-leg.sh" probe cloud/control-plane sh -c 'exit 7'
[ $? -eq 7 ]
check "dir present: exit 7 propagates as 7, not as 1" "exact exit code" $?

# 2-bis) directorio VACIO en un arbol PUBLICO: cuenta como ausente. Es el resto que deja
#        go-task al fallar una pierna declarada con `dir:`, y sin este caso la conducta que
#        cierra ese bucle no la comprueba nadie.
t="$(mk_tree pub marker)"
mkdir -p "$t/cloud/control-plane"
out="$(bash "$t/scripts/private-leg.sh" probe cloud/control-plane false 2>&1)"; rc=$?
[ "$rc" -eq 0 ] && printf '%s' "$out" | grep -q 'NOT APPLICABLE'
check "empty dir in a public tree: NOT APPLICABLE, payload not executed" "rc=$rc" $?

# 3) public tree: NOT APPLICABLE, exit 0, and the command is NOT run (`false`
#    as the payload: if the guard wrongly executed it, this row goes red)
t="$(mk_tree pub marker)"
out="$(bash "$t/scripts/private-leg.sh" probe cloud/control-plane false)"
rc=$?
[ "$rc" -eq 0 ] && printf '%s' "$out" | grep -q "NOT APPLICABLE"
check "public tree: NOT APPLICABLE, exit 0, payload not executed" "marker honoured" $?

# 4) public tree: the note reaches the job summary when CI offers one — the
#    whole point is that 'not applicable' is read, not buried in a step log
sum="$TMP/summary"
: >"$sum"
GITHUB_STEP_SUMMARY="$sum" bash "$t/scripts/private-leg.sh" probe cloud/control-plane false >/dev/null
grep -q "NOT APPLICABLE" "$sum"
check "public tree: the note lands in GITHUB_STEP_SUMMARY" "summary written" $?

# 5) neither tree: no directory, no marker -> refuse loudly, exit 1
t="$(mk_tree bare)"
out="$(bash "$t/scripts/private-leg.sh" probe cloud/control-plane true 2>&1)"
rc=$?
[ "$rc" -eq 1 ] && printf '%s' "$out" | grep -q "Refusing to guess"
check "no dir, no marker: exit 1 with an explanation" "fail-closed refusal" $?

# 6) misuse is 'could not look' (exit 2), never a pass
bash "$ROOT/scripts/private-leg.sh" only-two-args 2>/dev/null
[ $? -eq 2 ]
check "fewer than three arguments: usage error, exit 2" "exit 2" $?

# 7) RED half of the marker design: deleting the marker in a public clone must
#    degrade to the loud refusal (answer 3), never to a silent green (answer 2)
t="$(mk_tree pub2 marker)"
rm "$t/PUBLIC-EXPORT.md"
bash "$t/scripts/private-leg.sh" probe cloud/control-plane true 2>/dev/null
[ $? -eq 1 ]
check "deleted marker degrades to red, not to green" "loud degradation" $?

# 8) LA CIEGA DICE DONDE ARREGLARLA, Y EL AMBITO IMPORTA. `node_modules` vive en el arbol de
#    trabajo y esta en .gitignore, asi que es POR WORKTREE; este clon tiene 561. El mensaje decia
#    «once per container» y costo una noche entera de ciegas a quien lo siguio al pie de la letra.
#    Aqui se fija la palabra que cambia la conducta, no la frase entera.
t="$(mk_tree deps)"
mkdir -p "$t/commercial/license-worker"
: >"$t/commercial/license-worker/package.json"
out="$(bash "$t/scripts/private-leg.sh" probe commercial/license-worker true 2>&1)"
rc=$?
[ "$rc" -eq 3 ] && printf '%s' "$out" | grep -q "CANNOT LOOK"
check "package.json sin node_modules: ciega con exit 3" "exit 3 + CANNOT LOOK" $?
printf '%s' "$out" | grep -q "per worktree, not per container"
check "la ciega nombra el AMBITO correcto (worktree, no contenedor)" "consejo accionable" $?

# 9) INERTE, y es el que da valor al 8: una pierna CON node_modules no debe imprimir nada de esto.
#    Sin este caso, un mensaje impreso siempre pasaria el 8 y seria ruido en cada corrida buena.
mkdir -p "$t/commercial/license-worker/node_modules"
: >"$t/commercial/license-worker/node_modules/.keep"
out="$(bash "$t/scripts/private-leg.sh" probe commercial/license-worker true 2>&1)"
printf '%s' "$out" | grep -q "per worktree" && rc=1 || rc=0
check "con node_modules: NO imprime el consejo" "silencio en el caso bueno" "$rc"

printf 'private-leg: %d ok, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
