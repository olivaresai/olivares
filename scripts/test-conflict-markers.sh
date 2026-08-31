#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-conflict-markers.sh — battery for scripts/check-conflict-markers.sh.
#
# The case that carries this file is CASE 2: a conflict in a MARKDOWN file. The incident that
# produced the gate was survivable only because the markers landed in a shell script, where
# `<<<<<<<` is a syntax error and the next lint choked on it. Prose has no such accident. If
# only the shell case were covered, the battery would be re-proving the luck instead of the
# control.
#
# Hermetic: every fixture is a throwaway repository. That matters more than usual here — the
# fixtures contain REAL conflict markers, so building them inside the repository under test
# would make this file's own test data trip the gate it is testing.
set -uo pipefail
SUT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-conflict-markers.sh"
PASS=0; FAIL=0
# ⛔ AISLAMIENTO DE ENTORNO GIT — OBLIGATORIO, y aquí no es higiene: SIN ESTO ESTA BATERÍA SE
# AUTOENVENENA EL GATE. Medido el 2026-08-15 desde un worktree ENLAZADO, que es como corren todas
# las sesiones paralelas: git exporta `GIT_DIR` a sus hooks, `repo()` deja de operar sobre el
# repositorio de pruebas, `git commit` no tiene nada que hacer, su prosa se antepone a la ruta que
# la función IMPRIME, y el `mkdir` siguiente crea un directorio dentro del árbol REAL con el
# fixture (marcadores incluidos) dentro. `check-conflict-markers.sh`, que corre justo después en
# el mismo `lint:conflict-markers`, lo encuentra y da DIRTY. Reproducido en los dos sentidos:
# con la librería, cero residuo; sin ella, residuo y rc=1.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t

repo() { # repo <name> -> prints dir, a git repo with one innocuous tracked file
	# ⛔ EVERY git call below is silenced on stdout, and that is not tidiness: THIS FUNCTION'S
	# STDOUT IS A PATH. Measured 2026-08-15 — `git commit` found nothing to commit, printed
	# "On branch main / nothing to commit, working tree clean" to STDOUT (which -q does NOT
	# suppress), and the caller's $( ) captured the message TOGETHER with the directory. The
	# fixture step then mkdir'd the whole string: a directory named after git's own output, with
	# real newlines in the name, created in the REPOSITORY instead of the temp tree. It survived
	# the run, the push gate found it, and this battery still reported 12/12 -- the leak was
	# silent because nothing checked that a path looked like one.
	#
	# In a worktree that is only a nuisance. In the shared clone it is everyone's, which is the
	# hazard CLAUDE.md documents: what is not committed there belongs to whoever runs `add -A`.
	local d="$WORK/$1"; mkdir -p "$d"
	git init -q -b main "$d" >/dev/null 2>&1 || { printf 'repo(): git init failed for %s\n' "$1" >&2; return 1; }
	printf 'hello\n' > "$d/README.md"
	git -C "$d" add -A >/dev/null 2>&1 || { printf 'repo(): git add failed for %s\n' "$1" >&2; return 1; }
	git -C "$d" commit -qm base >/dev/null 2>&1 \
		|| { printf 'repo(): commit staged nothing for %s -- the fixture is not what it claims\n' "$1" >&2; return 1; }
	# Fail closed on the shape too, so a future stray write cannot travel as a path again.
	case "$d" in "$WORK"/*) ;; *) printf 'repo(): computed path escaped WORK: %s\n' "$d" >&2; return 1 ;; esac
	printf '%s' "$d"
}
conflict() { # conflict <file> — write a complete, ordered marker triple
	printf '<<<<<<< HEAD\nmine\n=======\ntheirs\n>>>>>>> origin/main\n' >> "$1"
}
run() { # run <dir> <want-rc> <label> [want-substring]
	local d="$1" want="$2" label="$3" txt="${4:-}" out rc
	out=$(OLIVARES_CONFLICT_ROOT="$d" bash "$SUT" 2>&1); rc=$?
	if [ "$rc" -ne "$want" ]; then
		FAIL=$((FAIL+1)); printf 'FAIL %-58s got rc=%d want %d\n     %s\n' "$label" "$rc" "$want" "$(printf '%s' "$out"|tr '\n' ' '|cut -c1-130)"; return
	fi
	if [ -n "$txt" ]; then
		case "$out" in *"$txt"*) ;; *) FAIL=$((FAIL+1)); printf 'FAIL %-58s rc right, never said: %s\n' "$label" "$txt"; return ;; esac
	fi
	PASS=$((PASS+1)); printf 'ok   %-58s rc=%d\n' "$label" "$rc"
}

# --- CASE 0. CALIBRATION. Without a green baseline every red below could be red for free.
run "$(repo clean)" 0 "a clean tree is CLEAN" "no conflict markers"

# --- CASE 1. The shape that actually happened: committed markers in a shell script.
d="$(repo committed_sh)" || exit 2; printf '#!/bin/sh\n' > "$d/s.sh"; conflict "$d/s.sh"
git -C "$d" add -A; git -C "$d" commit -qm "bad resolution"
run "$d" 1 "committed markers in a shell script are caught" "s.sh:2"

# --- CASE 2. THE ONE THAT CARRIES THIS FILE. Markdown compiles no matter what is in it, so
# the syntax error that saved the real incident does not exist here. If this gate is ever
# narrowed to code, this case is what must go red.
d="$(repo committed_md)" || exit 2; conflict "$d/README.md"
git -C "$d" add -A; git -C "$d" commit -qm "bad resolution in prose"
run "$d" 1 "markers in MARKDOWN are caught — prose has no syntax error" "README.md:2"

# --- CASE 2b. ⛔ UN NOMBRE QUE EMPIEZA POR GUION SE LO COME `grep` COMO OPCIÓN. Lo encontró el
# contraste the model el 2026-08-15 y estaba VIVO en main: un fichero llamado `--help` con el
# triple dentro daba **CLEAN, rc=0** — `xargs` añade los operandos detrás del patrón, `grep` lee
# `--help` como opción, imprime su ayuda, sale 0, y el script descarta el diagnóstico.
#
# ⛔ EL FIXTURE TIENE UN SOLO CULPABLE, Y ES DELIBERADO: con un segundo fichero malo el caso da
# DIRTY por ESE otro y la casilla pasaría sin ejercitar nada. Me pasó al medirlo la primera vez —
# di el caso por bueno con un señuelo contaminado.
d="$(repo dash_option)"; conflict "$d/--help"
git -C "$d" add -A >/dev/null 2>&1; git -C "$d" commit -qm "dash option" >/dev/null 2>&1
run "$d" 1 "un fichero llamado --help NO es invisible" "--help:1"

# --- CASE 3. Written but not yet `git add`-ed. Reading only the index would move the blind
# spot one step earlier rather than close it, and the author staging a bad resolution is the
# moment this gate exists for.
d="$(repo untracked)" || exit 2; printf 'x\n' > "$d/new.md"; conflict "$d/new.md"
run "$d" 1 "an unadded file with markers is caught" "new.md"

# --- CASE 4. And its counterweight: a gitignored scratch tree belongs to somebody else.
d="$(repo ignored)" || exit 2; printf '.scratch/\n' > "$d/.gitignore"
mkdir -p "$d/.scratch"; printf 'x\n' > "$d/.scratch/other.md"; conflict "$d/.scratch/other.md"
git -C "$d" add .gitignore >/dev/null; git -C "$d" commit -qm ignore >/dev/null
run "$d" 0 "a gitignored scratch tree is not this tree" "CLEAN"

# --- CASES 5-7. THE TRIPLE MUST BE IN ORDER, or ordinary documents become false positives.
# A repository that cries wolf over its own prose is a repository that removes the gate.
d="$(repo lone_eq)" || exit 2; printf 'title\n=======\nbody\n' >> "$d/README.md"
git -C "$d" add -A; git -C "$d" commit -qm underline
run "$d" 0 "a setext underline (=======) alone is not a conflict" "CLEAN"

d="$(repo lone_lt)" || exit 2; printf 'quoting a diff:\n<<<<<<< HEAD\n' >> "$d/README.md"
git -C "$d" add -A; git -C "$d" commit -qm quote
run "$d" 0 "a lone <<<<<<< quoted in docs is not a conflict" "CLEAN"

d="$(repo out_of_order)" || exit 2; printf '>>>>>>> origin/main\n=======\n<<<<<<< HEAD\n' >> "$d/README.md"
git -C "$d" add -A; git -C "$d" commit -qm reversed
run "$d" 0 "the three markers in the WRONG order are not a conflict" "CLEAN"

# --- CASE 8. Two conflicts in one file are both named. A gate that reports the first and
# stops teaches the reader to fix one and push again.
d="$(repo two_hunks)" || exit 2; conflict "$d/README.md"; printf 'middle\n' >> "$d/README.md"; conflict "$d/README.md"
git -C "$d" add -A; git -C "$d" commit -qm two
out=$(OLIVARES_CONFLICT_ROOT="$d" bash "$SUT" 2>&1)
if [ "$(printf '%s' "$out" | grep -c 'MARKERS at')" -eq 2 ]; then
	PASS=$((PASS+1)); printf 'ok   %-58s rc=1\n' "both hunks in one file are named, not just the first"
else
	FAIL=$((FAIL+1)); printf 'FAIL %-58s named %s hunk(s), want 2\n' "both hunks in one file are named, not just the first" "$(printf '%s' "$out"|grep -c 'MARKERS at')"
fi

# --- CASE 8b. THE ONE THIS GATE FAILED IN PRODUCTION. A path with a real NEWLINE in it.
# On 2026-08-15 a directory with a newline in its name reached main carrying a file with the full
# marker triple, and this gate answered "CLEAN — 10987 file(s) examined". Without -z, git quotes
# and escapes such a path, the scan looked for a name that does not exist, the xargs error was
# swallowed and the [ -f ] guard skipped it in silence: two deny-closed nets in a row, both
# crossed. Found by another lane with file:line rather than an argument.
#
# It is checked as UNVERIFIED and not as DIRTY on purpose. What the gate must never do again is
# call this tree clean; whether it can also read the file is a smaller question than whether it
# knows it could not.
d="$(repo newline_path)" || exit 2
weird="$d/dir with"$'\n'"newline"
mkdir -p "$weird"; printf 'x\n' > "$weird/f.md"; conflict "$weird/f.md"
git -C "$d" add -A >/dev/null 2>&1
out=$(OLIVARES_CONFLICT_ROOT="$d" bash "$SUT" 2>&1); rc=$?
if [ "$rc" -ne 0 ] && ! printf '%s' "$out" | grep -q 'CLEAN'; then
	PASS=$((PASS+1)); printf 'ok   %-58s rc=%d\n' "a path with a NEWLINE never reads as clean" "$rc"
else
	FAIL=$((FAIL+1)); printf 'FAIL %-58s rc=%d — %s\n' "a path with a NEWLINE never reads as clean" "$rc" "$(printf '%s' "$out"|tail -1|cut -c1-70)"
fi

# --- CASES 9-10. THE THIRD ANSWER. Every one of these was a green in some earlier instrument.
mkdir -p "$WORK/not-a-repo"   # un directorio REAL que no es repositorio: sin esto el gate muere
                              # antes en el cd y el caso mide otra cosa
run "$WORK/not-a-repo" 2 "a real directory that is not a repository is COULD NOT LOOK" "not the top level"
d="$(repo subdir)" || exit 2; mkdir -p "$d/sub"
run "$d/sub" 2 "a SUBDIRECTORY of a repo is refused, not silently graded" "not the top level"

# --- UN CENSO VACÍO ES UNA SONDA ROTA, NO UN ÁRBOL LIMPIO.
#
# ⛔ ESTA CASILLA EXISTE PORQUE SU MUTANTE SOBREVIVIÓ. El contraste the model del 2026-08-15
#    (`an internal design note (not shipped)`) quitó el rechazo del censo
#    vacío y la batería siguió en **15/15**: el sujeto mutado devolvía `rc 0` y
#    «CLEAN — 0 file(s) examined». Ninguna casilla usaba un repositorio sin ficheros, así que
#    nada distinguía «no hay marcadores» de «no he mirado nada».
#
#    El fixture NO puede salir de repo(), que commitea un README a propósito: aquí hace falta un
#    repositorio de verdad y VACÍO, que es justo el caso que faltaba.
d="$WORK/censo-vacio"; mkdir -p "$d"
git init -q -b main "$d" >/dev/null 2>&1 || { printf 'FIXTURE: git init falló\n' >&2; exit 2; }
[ "$(git -C "$d" ls-files | wc -l)" -eq 0 ] || { printf 'FIXTURE: el repo vacío no está vacío\n' >&2; exit 2; }
run "$d" 2 "an EMPTY census is UNVERIFIED, not a clean tree" "census that stopped working"

printf '\nconflict-markers: %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
