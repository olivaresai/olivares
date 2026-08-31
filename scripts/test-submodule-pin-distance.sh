#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-submodule-pin-distance.sh — battery for scripts/check-submodule-pin-distance.sh.
#
# The case that carries this file is the COUNTERFACTUAL PAIR: two fixtures whose pins are the
# same distance behind, differing only in whether that distance crosses a toolchain bump. One
# must be clean and the other must be caught. Without the clean half, a gate that simply hated
# every lag would pass this battery and then be switched off in a week for crying wolf; without
# the caught half, the gate is decoration. Neither test proves anything on its own.
#
# Hermetic: every fixture is a throwaway repository built here with plumbing. In particular the
# gitlink is written with `update-index --cacheinfo 160000`, never with `git submodule add` —
# the gate is specified to read the pin from the SUPERPROJECT'S TREE with the submodule
# uninitialised, so the battery must be able to produce exactly that state.
set -uo pipefail

# ⛔ AISLAMIENTO DE ENTORNO GIT. Git EXPORTA `GIT_DIR` a los hooks desde todo worktree ENLAZADO
# —o sea, desde cualquier sesion en paralelo— y `GIT_DIR` MANDA SOBRE `-C`: sin sanear, los
# repositorios desechables que construye este banco son el repositorio VIVO de quien lo invoque.
# MEDIDO el 2026-08-30 contra un repositorio de destino desechable, con este mismo fichero y sin
# esta linea: el destino recibio COMMITS. Falla cerrado: no poder aislar es «no he podido».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
olivares_git_env_isolate
SUT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-submodule-pin-distance.sh"
PASS=0; FAIL=0
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t

hub_new() { # hub_new <name> <toolchain> -> prints dir
	local d="$WORK/$1"; git init -q -b main "$d"
	printf 'go 1.26.5\n\ntoolchain %s\n\nuse (\n\t./a\n)\n' "$2" > "$d/go.work"
	git -C "$d" add -A && git -C "$d" commit -qm "root"
	printf '%s' "$d"
}
hub_advance() { # hub_advance <dir> <n> [new toolchain]
	local d="$1" n="$2" tc="${3:-}" i
	if [ -n "$tc" ]; then
		printf 'go 1.26.5\n\ntoolchain %s\n\nuse (\n\t./a\n)\n' "$tc" > "$d/go.work"
		git -C "$d" add -A && git -C "$d" commit -qm "bump toolchain"
	fi
	for i in $(seq 1 "$n"); do
		printf '%s\n' "$i" > "$d/f$i"
		git -C "$d" add -A && git -C "$d" commit -qm "c$i"
	done
}
super_new() { # super_new <name> <pin sha> -> prints dir
	local d="$WORK/$1"; git init -q -b main "$d"
	git -C "$d" update-index --add --cacheinfo "160000,$2,public"
	git -C "$d" -c commit.gpgsign=false commit -qm "pin"
	printf '%s' "$d"
}
run() { # run <super> <hub> <want-rc> <label> [want-substring] [extra env assignments...]
	local super="$1" hub="$2" want="$3" label="$4" want_txt="${5:-}"; shift 5 2>/dev/null || shift 4
	local out rc
	out=$(env OLIVARES_SUPERPROJECT="$super" OLIVARES_HUB_GIT_DIR="$hub" OLIVARES_PIN_NO_FETCH=1 \
		"$@" bash "$SUT" ${ARGS:-} 2>&1); rc=$?
	if [ "$rc" -ne "$want" ]; then
		FAIL=$((FAIL+1)); printf 'FAIL %-56s got rc=%d want %d\n     %s\n' "$label" "$rc" "$want" "$(printf '%s' "$out" | tr '\n' ' ' | cut -c1-150)"; return
	fi
	if [ -n "$want_txt" ]; then
		case "$out" in *"$want_txt"*) ;; *)
			FAIL=$((FAIL+1)); printf 'FAIL %-56s rc right, message never said: %s\n     %s\n' "$label" "$want_txt" "$(printf '%s' "$out" | tr '\n' ' ' | cut -c1-150)"; return ;;
		esac
	fi
	PASS=$((PASS+1)); printf 'ok   %-56s rc=%d\n' "$label" "$rc"
}

# ---------------------------------------------------------------------------------------------
# THE COUNTERFACTUAL PAIR. Both pins are exactly 5 commits behind. Only one crosses a bump.
# ---------------------------------------------------------------------------------------------
SAME="$(hub_new same go1.26.6)"
PIN_SAME="$(git -C "$SAME" rev-parse HEAD)"
hub_advance "$SAME" 5
S_SAME="$(super_new sup-same "$PIN_SAME")"
run "$S_SAME" "$SAME" 0 "5 behind, toolchain unchanged -> CLEAN" "behind   5 commit(s)"

CROSS="$(hub_new cross go1.26.5)"
PIN_CROSS="$(git -C "$CROSS" rev-parse HEAD)"
hub_advance "$CROSS" 4 go1.26.6   # the bump, then 4 more: also 5 commits behind
S_CROSS="$(super_new sup-cross "$PIN_CROSS")"
run "$S_CROSS" "$CROSS" 1 "5 behind ACROSS a toolchain bump -> STALE" "go1.26.5 at the pin, go1.26.6 at main"

# The August incident itself, at its measured scale: a pin far behind, across the bump.
FAR="$(hub_new far go1.26.5)"
PIN_FAR="$(git -C "$FAR" rev-parse HEAD)"
hub_advance "$FAR" 30 go1.26.6
S_FAR="$(super_new sup-far "$PIN_FAR")"
run "$S_FAR" "$FAR" 1 "the 2026-08 shape: far behind, across the bump" "contain the standard-library fixes"

# ---------------------------------------------------------------------------------------------
# Ancestry: 'behind by N' is meaningless off the branch, so it must not be the answer given.
# ---------------------------------------------------------------------------------------------
FORK="$(hub_new fork go1.26.6)"
hub_advance "$FORK" 2
git -C "$FORK" checkout -q -b other HEAD~1
printf 'divergent\n' > "$FORK/d"; git -C "$FORK" add -A; git -C "$FORK" commit -qm "off main"
PIN_FORK="$(git -C "$FORK" rev-parse HEAD)"
git -C "$FORK" checkout -q main
S_FORK="$(super_new sup-fork "$PIN_FORK")"
run "$S_FORK" "$FORK" 1 "pin off the branch -> named as NOT an ancestor" "NOT an ancestor of main"

# ---------------------------------------------------------------------------------------------
# Release strictness. Same fixture, both verdicts, so the mode is what decides and not the tree.
# ---------------------------------------------------------------------------------------------
ARGS=--release run "$S_SAME" "$SAME" 1 "--release, 5 behind, same toolchain -> STALE" "may not be approximately"
AT_HEAD="$(git -C "$SAME" rev-parse main)"
S_HEAD="$(super_new sup-head "$AT_HEAD")"
ARGS=--release run "$S_HEAD" "$SAME" 0 "--release, pinned AT the head -> CLEAN" "behind   0 commit(s)"
ARGS= run "$S_HEAD" "$SAME" 0 "branch mode, pinned at the head -> CLEAN" "CLEAN"

# ---------------------------------------------------------------------------------------------
# The optional bound, and its refusal to read a value it cannot parse as a number.
# ---------------------------------------------------------------------------------------------
run "$S_SAME" "$SAME" 1 "MAX_BEHIND=4 with 5 behind -> STALE" "over the declared bound" OLIVARES_PIN_MAX_BEHIND=4
run "$S_SAME" "$SAME" 0 "MAX_BEHIND=5 with 5 behind -> CLEAN" "CLEAN" OLIVARES_PIN_MAX_BEHIND=5
run "$S_SAME" "$SAME" 2 "MAX_BEHIND=lots -> COULD NOT LOOK" "not a non-negative integer" OLIVARES_PIN_MAX_BEHIND=lots

# ---------------------------------------------------------------------------------------------
# THE THIRD ANSWER. Every one of these was a green in some earlier instrument in this tree.
# ---------------------------------------------------------------------------------------------
run "$S_SAME" "$WORK/nope" 2 "no clone to compare against -> COULD NOT LOOK" "is not a git repository"
run "$WORK/nothing-here" "$SAME" 2 "superproject is not a repo -> COULD NOT LOOK" "is not a git repository"
run "$S_SAME" "$SAME" 2 "wrong submodule path -> COULD NOT LOOK" "no entry for 'wrong'" OLIVARES_SUBMODULE_PATH=wrong

PLAIN="$WORK/plain"; git init -q -b main "$PLAIN"
mkdir -p "$PLAIN/public"; printf 'not a submodule\n' > "$PLAIN/public/README"
git -C "$PLAIN" add -A; git -C "$PLAIN" commit -qm "plain dir"
run "$PLAIN" "$SAME" 2 "path is a plain directory, not a gitlink -> COULD NOT LOOK" "is not a submodule"

NOWORK="$(hub_new nowork go1.26.6)"
PIN_NW="$(git -C "$NOWORK" rev-parse HEAD)"
git -C "$NOWORK" rm -q go.work; git -C "$NOWORK" commit -qm "drop go.work"
S_NW="$(super_new sup-nowork "$PIN_NW")"
run "$S_NW" "$NOWORK" 2 "go.work gone at the branch head -> COULD NOT LOOK" "declares no go/toolchain"

# The branch itself missing. Until 2026-08-15 no fixture reached this line, and a mutation that
# made the head resolution fail-OPEN kept the battery at 17/17 — the gate would then have gone on
# to compare against an EMPTY head and answered "stale" for a branch it never found. Wrong answer,
# right colour, which is the shape that survives review.
run "$S_SAME" "$SAME" 2 "reference branch does not exist -> COULD NOT LOOK" "could not resolve the head" OLIVARES_HUB_BRANCH=nope

# go.work missing at the PIN rather than at the head: the two sides are read by the same helper,
# so only one of them being covered is how the other silently stops being checked.
NOWORK_PIN="$WORK/nowork-pin"; git init -q -b main "$NOWORK_PIN"
printf 'placeholder\n' > "$NOWORK_PIN/README"; git -C "$NOWORK_PIN" add -A; git -C "$NOWORK_PIN" commit -qm "no go.work yet"
PIN_NWP="$(git -C "$NOWORK_PIN" rev-parse HEAD)"
printf 'go 1.26.5\n\ntoolchain go1.26.6\n' > "$NOWORK_PIN/go.work"
git -C "$NOWORK_PIN" add -A; git -C "$NOWORK_PIN" commit -qm "add go.work"
run "$(super_new sup-nowork-pin "$PIN_NWP")" "$NOWORK_PIN" 2 "go.work absent AT THE PIN -> COULD NOT LOOK" "at the pinned commit"

# Two refs both called by the branch name, disagreeing. Found by running the gate against the
# shared hub clone, where the local branch lagged origin by more than a hundred commits: it
# answered "the pin is NOT an ancestor of main", which is the most alarming verdict it has, about
# a tree nobody was using. Preferring either one silently is the defect; the pair is the refusal.
AMBIG="$(hub_new ambig go1.26.6)"
PIN_AMBIG="$(git -C "$AMBIG" rev-parse HEAD)"
hub_advance "$AMBIG" 3
git -C "$AMBIG" update-ref refs/remotes/origin/main "$(git -C "$AMBIG" rev-parse main)"
git -C "$AMBIG" update-ref refs/heads/main "$PIN_AMBIG"        # la local se queda atrás
S_AMBIG="$(super_new sup-ambig "$PIN_AMBIG")"
run "$S_AMBIG" "$AMBIG" 2 "two refs called main that disagree -> COULD NOT LOOK" "they disagree"

# And the control that stops the refusal being a blanket one: when they AGREE there is nothing
# ambiguous, and the gate must answer normally.
git -C "$AMBIG" update-ref refs/heads/main "$(git -C "$AMBIG" rev-parse refs/remotes/origin/main)"
run "$S_AMBIG" "$AMBIG" 0 "the same two refs AGREEING is not ambiguous" "behind   3 commit(s)"

SHALLOW="$WORK/shallow"; git init -q -b main "$SHALLOW"
printf 'go 1.26.5\n\ntoolchain go1.26.6\n' > "$SHALLOW/go.work"
git -C "$SHALLOW" add -A; git -C "$SHALLOW" commit -qm root
run "$(super_new sup-absent 1111111111111111111111111111111111111111)" "$SHALLOW" 2 \
	"pinned commit absent from the clone -> COULD NOT LOOK" "is not present in"

ARGS=--bogus run "$S_SAME" "$SAME" 2 "unknown argument -> COULD NOT LOOK" "refuses to guess"

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
