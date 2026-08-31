#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-program-anchors.sh — prove `check-program-anchors.sh` by MUTATION, in BOTH directions.
#
# The subject shipped WITHOUT a battery. Its author mutated it by hand — control, deleted anchor,
# deleted document, outside the repo — and wrote the results in a relay. That is better than
# nothing and it is not the same thing: a hand run proves the gate worked ONCE, on a tree that no
# longer exists, and cannot fire again when someone edits the subject. Two of the four legs it
# claimed are the ones this file now runs on every push.
#
# The half that matters most here is the EMPTY-LIST leg. A gate whose corpus is a hand-written
# list has exactly one catastrophic failure mode: the list going empty, or losing an entry, while
# the gate keeps printing CLEAN. It would then certify nothing and look identical to a gate that
# certified everything — the "instrument that says clean when it means it did not look" this
# repository keeps finding. The subject already refuses an empty list; this proves the refusal.
set -u -o pipefail

# `git ls-files` is not used by the subject, but `git rev-parse --show-toplevel` is, and an
# inherited GIT_DIR outranks `cd`. From a LINKED worktree git exports it, so without this every
# scenario would grade the LIVE repository instead of the throwaway one.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env
export LC_ALL=C

HERE=$(cd -- "$(dirname -- "$0")" && pwd)
SUT="$HERE/check-program-anchors.sh"
[ -r "$SUT" ] || { echo "test-program-anchors: cannot read $SUT" >&2; exit 2; }

# Twenty-five scenarios used to leave twenty-five anonymous tmp.* repositories per run. Group
# them beneath one attributable root and remove it on every normal exit. The Taskfile wrapper is
# the abnormal-exit backstop and asserts that this cleanup remains effective.
RUN_TMP="$(mktemp -d "${TMPDIR:-/tmp}/program-anchors.XXXXXX")" || exit 2
cleanup() { chmod -R u+rwX "$RUN_TMP" 2>/dev/null; rm -rf -- "$RUN_TMP"; }
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

PASS=0; FAIL=0; START=$SECONDS

AMB_ROOT=$(git -C "$HERE" rev-parse --show-toplevel 2>/dev/null || true)
if [ -n "$AMB_ROOT" ]; then
	AMB_HEAD=$(git -C "$AMB_ROOT" rev-parse HEAD 2>/dev/null || echo NONE)
	# ⛔ AQUI SE FIRMABA `refs/heads/ refs/tags/` DEL HOST ENTERO, Y ESO NO PODIA MEDIR A ESTA
	# BATERIA: sus cajas se crean con `git init` en un temporal (mas abajo), asi que NUNCA crea
	# un ref en el repo anfitrion. Firmar la lista de ramas del clon compartido no vigila a la
	# bateria: vigila a los CINCO CARRILES que trabajan en el, y cualquiera de ellos creando una
	# rama de lote la encendia. Un control que no puede detectar un fallo de su sujeto y solo
	# puede dar falsos positivos no es un control: es un generador de rojos ajenos.
	#
	# Lo que la bateria SI puede hacer mal es escribir DENTRO del repo en vez de en su caja, y
	# eso lo ve el arbol de trabajo — que es del worktree de quien corre, no del clon compartido,
	# asi que es inmune a lo que hagan los demas.
	AMB_TREE=$(git -C "$AMB_ROOT" status --porcelain 2>/dev/null | md5sum)
fi

# A sandbox repository with the shape the subject expects: a board, and the programme documents
# the subject's own list names. The list is read OUT OF THE SUBJECT rather than duplicated here —
# duplicating it would let the two drift, and then this battery would grade a list nobody ships.
PROGRAMMES=$(sed -n '/^PROGRAMMES="/,/^"/p' "$SUT" | sed '1d;$d' | sed '/^[[:space:]]*$/d')
[ -n "$PROGRAMMES" ] || { echo "test-program-anchors: could not read PROGRAMMES out of the subject" >&2; exit 2; }

# ⛔ READING THE LIST OUT OF THE SUBJECT MAKES THIS BATTERY BLIND TO THE LIST. Deleting an entry
# from PROGRAMMES left every case green (13/0, measured): the scenarios simply generated one
# document fewer and all of them passed. So the PR's own headline -- "widened from one entry to
# three" -- was verified by nothing, and a future edit could quietly stop protecting a document
# with the battery still reporting success. Found by an adversarial contrast, 2026-08-08.
#
# The fix is the pattern that has caught this lane twice today, in test-prepush-refclass: DECLARE
# the expected set here and COMPARE. The duplication is deliberate and its whole purpose is to
# make divergence loud -- an entry added without updating this line, or removed, is a red with
# both sides printed. Reading it from the subject stays, for building the scenarios; declaring it
# is what makes the reading non-vacuous.
#
# Widened 3 -> 8 on 2026-08-16 (META-15). The five new entries are the ones
# `an internal design note (not shipped)` §6 left as a patch "prepared, proved red by
# counterfactual, and REQUESTED of the integrator": all five had ZERO anchors on the board, and
# the section warned that applying it demands the ESTADO-PROYECTO.md anchors in the SAME commit
# or the gate reddens on the spot — "which is its function". Both halves land together.
#
# The count is no longer written into these strings. It said "three" and "3 entries" while the
# list was about to become eight, which is the same failure this battery exists to catch, one
# level up: a number frozen in prose stops describing the thing it names.
EXPECTED_PROGRAMMES="design/PROGRAMA-LICENCIAS-ENTITLEMENT-Y-DISTRIBUCION.md
design/FORMA-DEL-PRODUCTO-DEFINITIVA-2026-08-08.md
design/PRICING-CANON.md
design/DECISION-COMERCIAL-2026-07-27.md
design/REGISTRO-DECISIONES-2026-08-01.md
design/DECISIONES-PERDIDAS-2026-08.md
design/audits/2026-08-06-codex-sol-ultra-suscripciones-sweep.md
design/audits/2026-08-06-codex-sol-ultra-licencias-sweep-2.md
design/PLAN-COMPLETITUD-RELEASE-2026-08-16.md
design/BACKLOG-COMPLETITUD-2026-08-16.md
design/CRITERIOS-RELEASE-2026-08-16.md
design/REPARTO-CARRILES-2026-08-16.md
design/DECISIONES-PENDIENTES-FRAN-2026-08-17.md"
n_expected=$(printf '%s\n' "$EXPECTED_PROGRAMMES" | grep -c .)
if [ "$(printf '%s\n' "$PROGRAMMES" | sort)" != "$(printf '%s\n' "$EXPECTED_PROGRAMMES" | sort)" ]; then
	FAIL=$((FAIL + 1))
	printf 'FAIL %-58s the subject list and this battery disagree\n' "the PROGRAMMES set is exactly the declared set"
	printf '     subject:\n%s\n' "$(printf '%s\n' "$PROGRAMMES" | sed 's/^/       /')"
	printf '     declared:\n%s\n' "$(printf '%s\n' "$EXPECTED_PROGRAMMES" | sed 's/^/       /')"
	printf '     If the change is intended, update EXPECTED_PROGRAMMES in the same commit — that is\n'
	printf '     the point: adding or dropping a protected document is never a silent edit.\n'
else
	PASS=$((PASS + 1))
	printf 'ok   %-58s %s\n' "the PROGRAMMES set is exactly the declared set" "$n_expected entries"
fi

mkrepo() { # mkrepo -> a tree where the subject is CLEAN
	local d; d=$(mktemp -d "$RUN_TMP/repo.XXXXXX") || return 1
	git -c init.defaultBranch=main init -q "$d" >/dev/null 2>&1 || return 1
	: > "$d/ESTADO-PROYECTO.md"
	while IFS= read -r p; do
		[ -n "$p" ] || continue
		mkdir -p "$d/$(dirname -- "$p")"
		printf 'programme body\n' > "$d/$p"
		printf 'the board links it: %s\n' "$p" >> "$d/ESTADO-PROYECTO.md"
	done <<EOF
$PROGRAMMES
EOF
	printf '%s' "$d"
}
run() { # run <repo> <want-rc> <label>
	local d="$1" want="$2" label="$3" out rc
	out=$(cd "$d" && bash "$SUT" 2>&1); rc=$?
	if [ "$rc" -eq "$want" ]; then
		PASS=$((PASS + 1)); printf 'ok   %-58s rc=%d\n' "$label" "$rc"
	else
		FAIL=$((FAIL + 1)); printf 'FAIL %-58s got rc=%d want rc=%d\n' "$label" "$rc" "$want"
		printf '     %s\n' "$out"
	fi
}
first_programme() { printf '%s\n' "$PROGRAMMES" | head -1; }

# ------------------------------------------------------------------------ control
d=$(mkrepo)
run "$d" 0 "every programme exists and is linked"

# -------------------------------------------------------------- catching direction
# The anchor is deleted from the board. The document still exists, so nothing looks broken to
# anyone reading the tree -- which is precisely how the programme was lost the first time.
d=$(mkrepo)
p=$(first_programme)
grep -v -F -- "$p" "$d/ESTADO-PROYECTO.md" > "$d/.tmp" && mv "$d/.tmp" "$d/ESTADO-PROYECTO.md"
run "$d" 1 "an ORPHANED programme (anchor deleted) is caught"

# The document is deleted and the anchor left behind: the board routes a session to nothing. The
# mirror of the case above, and a gate that only checks one direction is half a gate.
d=$(mkrepo)
rm -f "$d/$(first_programme)"
run "$d" 1 "a MISSING document (anchor points at nothing) is caught"

# Both at once must still be a single, honest verdict rather than a crash.
d=$(mkrepo)
p=$(first_programme)
rm -f "$d/$p"
grep -v -F -- "$p" "$d/ESTADO-PROYECTO.md" > "$d/.tmp" && mv "$d/.tmp" "$d/ESTADO-PROYECTO.md"
run "$d" 1 "document AND anchor gone is still a finding"

# EVERY entry is enforced, not just the first. A loop that returns early after one finding would
# pass the three cases above and silently stop protecting the rest of the list.
n=0
while IFS= read -r p; do
	[ -n "$p" ] || continue
	n=$((n + 1))
	d=$(mkrepo)
	grep -v -F -- "$p" "$d/ESTADO-PROYECTO.md" > "$d/.tmp" && mv "$d/.tmp" "$d/ESTADO-PROYECTO.md"
	run "$d" 1 "entry $n is enforced too: $(basename -- "$p")"
done <<EOF
$PROGRAMMES
EOF

# ---------------------------------------------------------- NOT-catching direction
# A SUBSTRING must not count as an anchor. Without fixed-string, whole-path matching, a board that
# mentions a neighbouring file would satisfy the check for a document it never links.
d=$(mkrepo)
p=$(first_programme)
grep -v -F -- "$p" "$d/ESTADO-PROYECTO.md" > "$d/.tmp" && mv "$d/.tmp" "$d/ESTADO-PROYECTO.md"
printf 'see %s.OLD for the archived version\n' "${p%.md}" >> "$d/ESTADO-PROYECTO.md"
run "$d" 1 "a NEAR-MISS path in the board is not an anchor"

# THE `-F` IS LOAD-BEARING, and the case above does NOT prove it. A path is not a pattern: without
# fixed-string matching every `.` in a filename becomes "any character", so a board mentioning
# `…-DISTRIBUCIONXmd` would satisfy the check for `…-DISTRIBUCION.md`. That is not hypothetical
# pedantry — it is the difference between an anchor and a coincidence, and the anchor is the whole
# product of this gate.
#
# Found by mutation: dropping `-F` from the subject left the battery fully green, because the
# near-miss fixture above differs in LETTERS (.OLD vs .md) where a wildcard cannot help. A
# surviving mutant means the case meant to prove this was measuring something else.
d=$(mkrepo)
p=$(first_programme)
grep -v -F -- "$p" "$d/ESTADO-PROYECTO.md" > "$d/.tmp" && mv "$d/.tmp" "$d/ESTADO-PROYECTO.md"
printf 'a coincidence, not an anchor: %sXmd\n' "${p%.md}" >> "$d/ESTADO-PROYECTO.md"
run "$d" 1 "a path is not a PATTERN: the dot must not match any character"

# An extra, unlisted document in design/ is not this gate's business. It answers one question and
# claiming more would make it the "gate that reads as coverage".
d=$(mkrepo)
printf 'body\n' > "$d/design/SOMETHING-ELSE.md"
run "$d" 0 "an unlisted document is not this gate's business"

# AN ANCHOR INSIDE AN HTML COMMENT ROUTES NOBODY. It satisfied the check while being invisible in
# the rendered board, so the document was "anchored" to a line no human reads. Same family as the
# two mistakes made in the other direction today -- counting the prose that DESCRIBES a defect as
# the defect -- and this is its mirror: prose that LOOKS like an anchor and is not one.
d=$(mkrepo)
p=$(first_programme)
grep -v -F -- "$p" "$d/ESTADO-PROYECTO.md" > "$d/.tmp" && mv "$d/.tmp" "$d/ESTADO-PROYECTO.md"
printf '<!-- %s -->\n' "$p" >> "$d/ESTADO-PROYECTO.md"
run "$d" 1 "an anchor inside an HTML COMMENT is not an anchor"

# ...and the comment filter must not eat the rest of the file, which the first cut did: a sed
# RANGE looks for its end starting at the NEXT line, so a one-line comment never found its close
# and deleted to EOF. An anchor written BELOW a comment vanished and its document read as orphaned
# -- the gate crying on a correct tree, which is the failure its own header warns about.
d=$(mkrepo)
p=$(first_programme)
printf '<!-- an ordinary one-line comment -->\n' >> "$d/ESTADO-PROYECTO.md"
printf 'and the anchor lives BELOW it: %s\n' "$p" >> "$d/ESTADO-PROYECTO.md"
run "$d" 0 "a one-line comment does not swallow the anchors below it"

# ---------------------------------------------------------------- could-not-look
# THE ONE THAT MATTERS. An empty list is the failure mode of any hand-written corpus: the gate
# would certify NOTHING while printing CLEAN, indistinguishable from certifying everything.
d=$(mkrepo)
sed '/^PROGRAMMES="/,/^"/c\PROGRAMMES="\n"' "$SUT" > "$d/sut-empty.sh"
out=$(cd "$d" && bash "$d/sut-empty.sh" 2>&1); rc=$?
if [ "$rc" -eq 2 ]; then
	PASS=$((PASS + 1)); printf 'ok   %-58s rc=2\n' "an EMPTY programme list is COULD NOT LOOK"
else
	FAIL=$((FAIL + 1)); printf 'FAIL %-58s got rc=%d want rc=2\n' "an EMPTY programme list is COULD NOT LOOK" "$rc"
	printf '     %s\n' "$out"
fi

# No board at all: the gate anchors TO it, so its absence is not a clean tree.
d=$(mkrepo)
rm -f "$d/ESTADO-PROYECTO.md"
run "$d" 2 "a MISSING board is COULD NOT LOOK, never clean"

out=$(cd /tmp && bash "$SUT" 2>&1); rc=$?
if [ "$rc" -eq 2 ]; then
	PASS=$((PASS + 1)); printf 'ok   %-58s rc=2\n' "outside a git repository is NOT clean"
else
	FAIL=$((FAIL + 1)); printf 'FAIL %-58s got rc=%d want rc=2\n' "outside a git repository is NOT clean" "$rc"
fi

# ---------------------------------------------------------------- post-condition
if [ -n "${AMB_ROOT:-}" ]; then
	now=$(git -C "$AMB_ROOT" rev-parse HEAD 2>/dev/null || echo NONE)
	[ "$now" = "$AMB_HEAD" ] || { FAIL=$((FAIL + 1)); printf 'FAIL %-58s HEAD moved\n' "host repo untouched"; }
	# ⛔ SOLO LOS REFS LOCALES. Esta post-condicion firmaba el espacio de refs ENTERO, incluidos
	# refs/remotes/*, sobre un clon que comparten tres contenedores y los procesos de fondo del
	# propio hub. Cualquier `git fetch` ajeno la encendia: el 2026-08-18 dio «FAIL host repo
	# untouched — refs changed» en una corrida del carril rapido y CERO fallos re-corrida sola,
	# 27/27, con la unica diferencia de que nadie estaba haciendo fetch a la vez.
	#
	# Un instrumento que no distingue «la bateria toco el repo» de «otro carril hizo fetch» no
	# mide su sujeto: mide la caja. La bateria solo puede crear/mover refs LOCALES, asi que eso
	# es lo que se firma. Si algun dia tocara un remote-tracking, esto dejaria de verlo — y esa
	# perdida es menor que un rojo que bloquea a cinco carriles por un fetch de otro.
	now=$(git -C "$AMB_ROOT" status --porcelain 2>/dev/null | md5sum)
	[ "$now" = "$AMB_TREE" ] || { FAIL=$((FAIL + 1)); printf 'FAIL %-58s working tree dirtied\n' "host repo untouched"; }
	[ "$FAIL" -eq 0 ] && { PASS=$((PASS + 1)); printf 'ok   %-58s 2 properties\n' "the host repository is untouched"; }
fi

printf '\ncheck-program-anchors: %d passed, %d failed, %ds wall\n' "$PASS" "$FAIL" "$((SECONDS - START))"
[ "$FAIL" -eq 0 ] || exit 1
