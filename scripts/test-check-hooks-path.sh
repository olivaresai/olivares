#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-check-hooks-path.sh — the battery for scripts/check-hooks-path.sh.
#
# It drives the gate against REAL throwaway repositories — a config value alone proves
# nothing, because three of the four failures this gate exists to catch (missing directory,
# missing hook, unexecutable hook) are properties of the filesystem, not of the config.
#
# TWO THINGS IT ASSERTS THAT A NAIVE BATTERY WOULD NOT:
#
#   1. THE VERDICT, NOT JUST THE EXIT CODE. All four broken states exit 1. A gate that
#      exits 1 for the wrong reason has told you nothing you can act on, so every red case
#      also asserts the phrase that names WHICH failure it is.
#
#   2. THAT EACH GUARD IS LOAD-BEARING. Every red case has a MUTANT leg: the guard is
#      removed from a copy of the gate (which must still parse — a mutant that will not run
#      goes red for the wrong reason, so `bash -n` is asserted on every one), and the case
#      must flip to green. A guard whose removal changes nothing was never testing anything.
#
# Exit 0 = every case passed. Exit 1 = a case failed (named). Exit 2 = could not run the
# battery at all — which is NOT a pass.
set -u

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)"

# The ambient git environment OUTRANKS `-C`: with GIT_DIR exported — which git does from
# every LINKED worktree, i.e. from every parallel session — this script's throwaway
# repositories would be driven into the LIVE repository instead. See lib/git-env.sh.
# shellcheck source=/dev/null
. "$HERE/lib/git-env.sh" || {
	echo "FATAL: cannot source $HERE/lib/git-env.sh (git-env isolation)" >&2
	exit 2
}

# This battery asserts PERMISSION BITS, and on a noexec mount `test -x` answers false even
# when the bit is set (measured 2026-08-07). Scratch space that cannot prove its execute bit
# is real would turn the clean case red and the not-executable case green. See lib/exec-workdir.sh.
# shellcheck source=/dev/null
. "$HERE/lib/exec-workdir.sh" || {
	echo "FATAL: cannot source $HERE/lib/exec-workdir.sh" >&2
	exit 2
}

command -v git >/dev/null 2>&1 || {
	echo "test-check-hooks-path: no git on PATH — could not run the battery. NOT a pass." >&2
	exit 2
}

ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || {
	echo "test-check-hooks-path: not inside a git repository — could not run. NOT a pass." >&2
	exit 2
}
GATE="$ROOT/scripts/check-hooks-path.sh"
[ -f "$GATE" ] || {
	echo "test-check-hooks-path: $GATE not found — could not run. NOT a pass." >&2
	exit 2
}

WORK="$(olivares_pick_exec_workdir check-hooks-path-tests)" || {
	echo "test-check-hooks-path: no scratch directory has a REAL execute bit" >&2
	echo "test-check-hooks-path: (tried RUNNER_TEMP, TMPDIR, /tmp, HOME, /workspace/.olivares-tmptest)." >&2
	echo "test-check-hooks-path: could not run the battery. This is NOT a pass." >&2
	exit 2
}
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

fails=0
pass() { printf '  ok   %s\n' "$1"; }
fail() {
	printf '  FAIL %s\n' "$1" >&2
	printf '       %s\n' "$2" >&2
	fails=$((fails + 1))
}

# mkrepo <name> — a repository in the shape the gate calls clean: relative hooksPath, a
# .githooks directory, both hooks present and executable. Every case starts from here and
# breaks exactly one property, so a red is attributable to that property and nothing else.
mkrepo() {
	local d="$WORK/$1"
	rm -rf "$d"
	mkdir -p "$d/.githooks"
	git init -q "$d" 2>/dev/null || return 1
	printf '#!/usr/bin/env bash\nexit 0\n' >"$d/.githooks/pre-push"
	printf '#!/usr/bin/env bash\nexit 0\n' >"$d/.githooks/commit-msg"
	chmod +x "$d/.githooks/pre-push" "$d/.githooks/commit-msg"
	git -C "$d" config core.hooksPath .githooks
	printf '%s' "$d"
}

# run <repo-dir> <gate-path> -> sets RC and OUT
run() {
	OUT="$(cd "$1" && bash "$2" 2>&1)"
	RC=$?
}

# expect <label> <repo> <gate> <want-rc> <want-substring>
expect() {
	local label="$1" repo="$2" gate="$3" wrc="$4" wsub="$5"
	run "$repo" "$gate"
	if [ "$RC" != "$wrc" ]; then
		fail "$label" "exit $RC, wanted $wrc — output: $(printf '%s' "$OUT" | head -2 | tr '\n' ' ')"
		return
	fi
	case "$OUT" in
	*"$wsub"*) pass "$label" ;;
	*) fail "$label" "exit code right ($RC) but the verdict never says '$wsub' — a red nobody can act on. Got: $(printf '%s' "$OUT" | head -2 | tr '\n' ' ')" ;;
	esac
}

# expect_mutant <name> <sed-expr> <label> <repo> <want-rc> <want-substring>
#
# Builds a copy of the gate with one guard removed and asserts the case flips.
#
# IT IS ONE FUNCTION, not a `M="$(mutant …)" && expect …` pair, because the pair was WRONG
# and the way it was wrong is the point. In the first version of this battery `mutant()`
# died under `set -u` on a `local` that referenced a variable being declared in the SAME
# statement — the trap this repository already has written down. The `&&` then swallowed it:
# four mutant legs never ran, nothing was recorded, and the battery printed
# `test-check-hooks-path: OK` and exited 0. A battery that reports green while a third of it
# silently did not execute is worse than no battery, so a leg that CANNOT RUN is now a
# FAILURE, never a skip — the same three-answer rule the gates themselves obey.
#
# Two properties are asserted about every mutant before it is used to judge anything:
#   PARSES   — `bash -n`. A mutant that cannot run reddens every case for the wrong reason,
#              which reads exactly like a battery that works.
#   DIFFERS  — the sed expression actually changed the file. A no-op edit proves nothing,
#              and a guard renamed tomorrow would silently turn every leg into a no-op.
expect_mutant() {
	local mname="$1" mexpr="$2" mlabel="$3" mrepo="$4" mwrc="$5" mwsub="$6"
	local m="$WORK/mutant-$mname.sh"
	if ! sed "$mexpr" "$GATE" >"$m" 2>/dev/null; then
		fail "$mlabel" "could not build the mutant (sed failed) — the leg did not run, which is a FAILURE and not a skip"
		return
	fi
	if cmp -s "$m" "$GATE"; then
		fail "$mlabel" "the sed expression changed nothing: the guard it targets has been renamed or removed, so this leg proves nothing"
		return
	fi
	if ! bash -n "$m" 2>/dev/null; then
		fail "$mlabel" "the mutated gate is not valid bash — it would redden every case for the wrong reason"
		return
	fi
	expect "$mlabel" "$mrepo" "$m" "$mwrc" "$mwsub"
}

printf 'test-check-hooks-path: nine cases, each red one with its mutant\n'

# ---------------------------------------------------------------- 1. clean
R="$(mkrepo clean)" || { echo "could not build fixture" >&2; exit 2; }
expect "1 clean: relative + both hooks executable -> exit 0" "$R" "$GATE" 0 "CLEAN"

# ---------------------------------------------------------------- 2. unset
#
# The fixture puts EXECUTABLE pre-push and commit-msg at the repository ROOT as well. That
# is not decoration: it is the shape the hazard actually takes. With hooksPath unset git
# uses $GIT_COMMON_DIR/hooks and this repository's .githooks never runs, while everything a
# careless check would look at is present and correct. It is what lets the mutant leg below
# reach a full CLEAN — i.e. prove the guard is the ONLY thing standing between this state
# and a green verdict, rather than being backstopped by some later check.
R="$(mkrepo unset)" || exit 2
git -C "$R" config --unset core.hooksPath
printf '#!/usr/bin/env bash\nexit 0\n' >"$R/pre-push"
printf '#!/usr/bin/env bash\nexit 0\n' >"$R/commit-msg"
chmod +x "$R/pre-push" "$R/commit-msg"
expect "2 hooksPath UNSET -> exit 1, named" "$R" "$GATE" 1 "NOT SET"
expect_mutant unset 's/if \[ -z "\$value" \]; then/if false; then/' \
	"2m mutant (unset guard defanged) reaches a full CLEAN" "$R" 0 "CLEAN"

# ---------------------------------------------------------------- 3. absolute
R="$(mkrepo absolute)" || exit 2
git -C "$R" config core.hooksPath "$R/.githooks"
expect "3 hooksPath ABSOLUTE -> exit 1, named" "$R" "$GATE" 1 "ABSOLUTE"
# The mutant does NOT go green here, and that is the finding rather than a defect in the
# leg: with the ABSOLUTE arm defanged, the value falls through to `resolved="$TOP/$value"`,
# which concatenates a top level with an absolute path and yields a doubled nonsense route.
# So the push is still refused — by accident, through a later guard, with a message that
# names the wrong problem. The guard is load-bearing for the DIAGNOSIS, and this asserts
# exactly that: the word ABSOLUTE disappears and an unactionable red takes its place.
expect_mutant absolute 's|^/\*)$|/__never_matches__*)|' \
	"3m mutant (absolute guard defanged) loses the ABSOLUTE diagnosis" "$R" 1 "not a directory"

# ------------------------------------------------- 4. points at a file, not a directory
R="$(mkrepo notadir)" || exit 2
printf 'not a directory\n' >"$R/hooksfile"
git -C "$R" config core.hooksPath hooksfile
expect "4 hooksPath is a FILE -> exit 1, named" "$R" "$GATE" 1 "not a directory"

# ------------------------------------------------- 5. points at something absent
R="$(mkrepo missingdir)" || exit 2
git -C "$R" config core.hooksPath .nowhere
expect "5 hooksPath directory ABSENT -> exit 1, named" "$R" "$GATE" 1 "not a directory"
expect_mutant missingdir '/if \[ ! -d "\$resolved" \]; then/,/^fi$/d' "5m mutant (directory guard removed) stops refusing" "$R" 1 "MISSING"

# ---------------------------------------------------------------- 6. pre-push missing
R="$(mkrepo nohook)" || exit 2
rm -f "$R/.githooks/pre-push"
expect "6 pre-push MISSING -> exit 1, named" "$R" "$GATE" 1 "pre-push is MISSING"

# ---------------------------------------------------------- 7. pre-push not executable
R="$(mkrepo noexecbit)" || exit 2
chmod -x "$R/.githooks/pre-push"
expect "7 pre-push NOT EXECUTABLE -> exit 1, named" "$R" "$GATE" 1 "pre-push is NOT EXECUTABLE"
expect_mutant noexecbit 's/if \[ ! -x "\$h" \]; then/if false; then/' \
	"7m mutant (exec-bit guard defanged) calls a silently-skipped hook CLEAN" "$R" 0 "CLEAN"

# --------------------------------------------------------- 8. commit-msg not executable
R="$(mkrepo noexecmsg)" || exit 2
chmod -x "$R/.githooks/commit-msg"
expect "8 commit-msg NOT EXECUTABLE -> exit 1, named" "$R" "$GATE" 1 "commit-msg is NOT EXECUTABLE"

# ------------------------------------------------------ 9. not a repository -> exit 2
NR="$WORK/notarepo"
mkdir -p "$NR"
expect "9 not a git working tree -> exit 2 (COULD NOT LOOK, not a pass)" "$NR" "$GATE" 2 "COULD NOT LOOK"

if [ "$fails" -ne 0 ]; then
	printf 'test-check-hooks-path: %d FAILED\n' "$fails" >&2
	exit 1
fi
printf 'test-check-hooks-path: OK\n'
exit 0
