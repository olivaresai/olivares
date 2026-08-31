#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-conflict-markers.sh — refuse a tree that still carries merge conflict markers.
#
# WHY, measured 2026-08-15 while composing a 44-hour-stale branch. `git merge` reported ONE
# conflicted file. Four had markers. The other three were reported clean because the merge
# driver produced them without flagging the paths, and then `git add -A` — the ordinary way
# to stage a resolution — MARKED THEM RESOLVED WITH THE MARKERS INSIDE and `git commit`
# accepted it. Nothing in the tree objected.
#
# What stopped that push was luck wearing the costume of a control: one of the three was a
# shell script, so `<<<<<<<` was a syntax error and the lint that ran it exited 2. Had the
# markers landed in a Markdown file, a YAML file, a JSON fixture or a Go comment, the push
# would have been GREEN and the tree would carry `<<<<<<< HEAD` into main.
#
# THE CENSUS IS `git ls-files --cached --others --exclude-standard`, and every word of that
# is load-bearing:
#   * --cached  — what is committed, because the case above was already committed;
#   * --others  — what is written but not yet added, because the author staging a bad
#                 resolution is exactly the moment this gate is for, and reading only the
#                 index would move the blind spot one step earlier instead of closing it;
#   * --exclude-standard — a scratch worktree of another lane is not this tree. Measured on
#                 the shared hub clone, a bare walk finds other lanes' checkouts and grades
#                 them, which is how a correct gate gets switched off for crying wolf.
#
# THE PATTERN IS THE TRIPLE, IN ORDER, EACH AT LINE START: `<<<<<<< `, then `=======`, then
# `>>>>>>> `. Not any one of them alone. A gate that fired on `<<<<<<<` anywhere would fire
# on every script that SEARCHES for markers — including this one — and a gate that fires on
# itself is a gate somebody deletes.
#
# NO EXEMPTION LIST, and that is a measurement rather than an omission: on 2026-08-15 exactly
# ZERO tracked files in this repository carried the triple. An allowlist written today would
# be an allowlist for nothing, and the first file that needs one should be a decision someone
# makes out loud, not an entry that was already sitting there.
#
# Exit 0 clean · 1 markers found, each named with its line · 2 could not look.
set -uo pipefail

RC_CLEAN=0; RC_DIRTY=1; RC_BLIND=2
blind() { printf 'check-conflict-markers: UNVERIFIED — %s\n' "$*" >&2; exit "$RC_BLIND"; }

ROOT="${OLIVARES_CONFLICT_ROOT:-}"
if [ -z "$ROOT" ]; then
	ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)" || blind "cannot resolve the repository root"
fi
cd "$ROOT" || blind "cannot enter $ROOT"
command -v git >/dev/null 2>&1 || blind "git is not on PATH, so the census cannot be taken"

top="$(git rev-parse --show-toplevel 2>/dev/null)" || top=""
[ -n "$top" ] && [ "$top" = "$PWD" ] || blind "$ROOT is not the top level of a git work tree.
  The census is the index plus unignored working files; without a repository there is no census,
  and walking the filesystem instead would grade whatever happens to be lying here."

# ⛔ `-z`, AND IT IS NOT PEDANTRY: this gate declared a tree CLEAN with a conflicted file in it.
# Found 2026-08-15 by another lane, with file:line rather than an argument. A path containing a
# real NEWLINE is emitted by git QUOTED AND ESCAPED when -z is absent, so the name that reached
# the scan did not exist; the xargs error was swallowed by `2>/dev/null || true`, and the
# `[ -f "$f" ] || continue` below skipped it without a word. TWO deny-closed nets in a row, and
# the path went through both -- while this file's own header promises that a file the scan cannot
# read is not a file with no hits.
#
# NUL-separated end to end, and a census entry that cannot be stat'd is now REPORTED rather than
# skipped, because "I could not look at this one" is the third answer and it was being spent as
# a silent pass.
CENSUS="$(mktemp)"; trap 'rm -f "$CENSUS" "$CANDS"' EXIT
git ls-files -z --cached --others --exclude-standard > "$CENSUS" \
	|| blind "git ls-files failed; the file set is UNKNOWN, not empty"
count="$(tr -cd '\0' < "$CENSUS" | wc -c)"
[ "${count:-0}" -gt 0 ] || blind "the census returned no files at all, which is not a clean tree —
  it is a census that stopped working. An empty subject reads as a perfect result."

# ⛔ CANDIDATES FIRST, AND THAT IS A MEASUREMENT NOT A MICRO-OPTIMISATION. The first version
# spawned two processes per file — a binary test and an awk — over ~11k files, and cost 26s on
# the FAST lane, which is ~10% of its whole budget imposed on four lanes for a check that finds
# nothing almost every time. One grep pass now narrows to the files that carry the opening
# marker at all, and awk runs only on those. Same answer, 26s -> under 2s.
#
# -I skips binaries here too, so a binary that happens to contain the byte sequence never
# reaches the ordered check. -l stops at the first hit per file; the ORDER is still decided by
# awk below, on the few survivors.
CANDS="$(mktemp)"
# ⛔ EL `--` NO ES ADORNO: sin él, `xargs` añade los operandos DETRÁS del patrón y `grep` se come
# un fichero llamado `--help` COMO OPCIÓN — imprime su ayuda, sale 0, y este script descarta el
# diagnóstico (`2>/dev/null || true`) y sigue. Medido contra un señuelo de dos ficheros, `ok.md`
# y `--help` con el triple dentro: **CLEAN, rc=0**.
#
# Sólo el nombre de la RAÍZ viaja desnudo: `sub/--help` lleva prefijo y `grep` ni lo mira como
# opción, así que el agujero es estrecho — y por eso el fixture de la casilla tiene UN solo
# culpable, en la raíz. Con un segundo fichero cualquiera el caso da DIRTY por otra razón y la
# casilla pasaría sin ejercitar nada (me pasó al medirlo la primera vez).
xargs -0 -r grep -lZIE '^<<<<<<< ' -- < "$CENSUS" > "$CANDS" 2>/dev/null || true

# The census entries the scan could not even stat. A path with a newline used to land here and
# vanish; now it is named, and it makes the run UNVERIFIED rather than clean.
unreadable=0
while IFS= read -r -d '' f; do
	[ -n "$f" ] || continue
	[ -e "$f" ] || { unreadable=$((unreadable + 1)); printf 'check-conflict-markers: CANNOT STAT %s\n' "$(printf '%q' "$f")" >&2; }
done < "$CENSUS"

found=0
while IFS= read -r -d '' f; do
	[ -n "$f" ] && [ -f "$f" ] || continue
	# The triple must appear IN ORDER. awk carries the state so `=======` in ordinary prose,
	# or a lone `>>>>>>>` in a diff quoted inside documentation, cannot raise this on its own.
	hit="$(awk '
		/^<<<<<<< / { start = FNR; seen_mid = 0; next }
		start && /^=======$/ { seen_mid = 1; next }
		start && seen_mid && /^>>>>>>> / { print start; start = 0; seen_mid = 0 }
	' "$f" 2>/dev/null)"
	[ -n "$hit" ] || continue
	found=$((found + 1))
	while IFS= read -r ln; do
		printf 'check-conflict-markers: MARKERS at %s:%s\n' "$f" "$ln" >&2
	done <<EOF
$hit
EOF
done < "$CANDS"

if [ "$unreadable" -ne 0 ]; then
	printf 'check-conflict-markers: UNVERIFIED — %d census entr(y/ies) could not be stat'"'"'ed, so this\n' "$unreadable" >&2
	printf '  tree was NOT fully scanned. A path the scan cannot reach is not a path with no hits.\n' >&2
	exit "$RC_BLIND"
fi
if [ "$found" -ne 0 ]; then
	printf 'check-conflict-markers: DIRTY — %d file(s) carry an unresolved conflict.\n' "$found" >&2
	printf '  `git add -A` marks a conflicted path RESOLVED whatever is inside it, so a bad resolution\n' >&2
	printf '  commits without a word. Resolve the hunks named above, then re-run.\n' >&2
	exit "$RC_DIRTY"
fi
printf 'check-conflict-markers: CLEAN — %d file(s) examined (index + unignored working tree), no conflict markers.\n' "$count"
exit "$RC_CLEAN"
