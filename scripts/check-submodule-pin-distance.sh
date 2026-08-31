#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-submodule-pin-distance.sh — measures the DISTANCE between the tree a superproject
# pins and the branch that tree comes from.
#
# WHY THIS EXISTS, measured 2026-08-15. The enterprise repository assembles the binary we
# sell from a `public` submodule pinned at a hub commit from 2026-08-08. Hub main was 2083
# commits ahead. The consequence was not cosmetic: the pinned tree declares
# `toolchain go1.26.5`, so the artefact that gets sold was being compiled with the toolchain
# carrying the seven CALLED vulnerabilities that #775 closed the night before — plus none of
# the 27 fixes of #768 nor the 22 PRs of #783.
#
# Nothing caught it, and the reason is the whole point of this gate: BOTH sides were green
# and BOTH were telling the truth. The enterprise CI honestly measures what it assembles.
# The hub gate honestly measures the hub. Nobody measured the distance between them. A stale
# submodule pin does not fail — it checks out, it compiles, it ships.
#
# WHAT IS ENFORCED, and what is only declared. There is no invented commit threshold here,
# because a number nobody can defend is how a gate becomes noise:
#
#   * ALWAYS ENFORCED — the pinned tree must declare the SAME Go toolchain as the branch
#     head. This is the property that was actually violated. A lag of a thousand commits
#     that never crosses a toolchain bump costs us nothing; a lag of ONE that crosses it
#     ships a binary built with vulnerabilities we had already closed.
#   * ALWAYS ENFORCED — the pin must be an ancestor of the branch head. A pin that is not
#     on that history is not "behind", it is a different tree, and "behind by N" would be a
#     meaningless number rather than a small one.
#   * ALWAYS DECLARED — the exact commit count and the pin's age, printed on every run,
#     green or red, so the number lands in a log even when nothing is refused.
#   * ENFORCED IN RELEASE MODE (--release) — the pin must equal the branch head exactly. A
#     release does not get to be approximately the tree we audited.
#   * OPTIONAL — OLIVARES_PIN_MAX_BEHIND, if set, additionally bounds the count.
#
# THREE ANSWERS, NEVER TWO. Exit 0 clean, 1 stale, 2 could-not-look. Every path that cannot
# complete a measurement exits 2 and names what it could not read. "I did not manage to look"
# is not "it is clean" — that conflation is the failure mode this repository has now measured
# in six separate instruments, and it is the one that costs the most, because the number that
# comes back is always plausible.
#
# INPUTS (all overridable, which is what makes the battery hermetic):
#   OLIVARES_SUPERPROJECT      root of the repo holding the submodule       (default: cwd's toplevel)
#   OLIVARES_SUBMODULE_PATH    path of the submodule inside it              (default: public)
#   OLIVARES_HUB_GIT_DIR       a clone of the submodule's origin to resolve
#                              ancestry against. Defaults to the submodule
#                              checkout itself; needed because the pin can be
#                              read from the superproject WITHOUT the
#                              submodule ever being initialised.
#   OLIVARES_HUB_BRANCH        branch that is the reference                 (default: main)
#   OLIVARES_PIN_NO_FETCH=1    skip the fetch (batteries, air-gapped runs)
#   OLIVARES_PIN_MAX_BEHIND    optional upper bound on the commit count
set -uo pipefail

RC_CLEAN=0; RC_STALE=1; RC_BLIND=2

blind() { printf 'COULD NOT LOOK — %s\n' "$*" >&2; exit "$RC_BLIND"; }
stale() { printf 'STALE PIN — %s\n' "$*" >&2; }

MODE=branch
for a in "$@"; do
	case "$a" in
		--release) MODE=release ;;
		--help|-h) sed -n '6,50p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) blind "unknown argument: $a (this gate refuses to guess what it was asked)" ;;
	esac
done

command -v git >/dev/null 2>&1 || blind "git is not on PATH, so nothing here can be measured"

SUB_PATH="${OLIVARES_SUBMODULE_PATH:-public}"
BRANCH="${OLIVARES_HUB_BRANCH:-main}"

SUPER="${OLIVARES_SUPERPROJECT:-}"
if [ -z "$SUPER" ]; then
	SUPER="$(git rev-parse --show-toplevel 2>/dev/null)" \
		|| blind "not inside a git work tree and OLIVARES_SUPERPROJECT is unset"
fi
[ -d "$SUPER/.git" ] || [ -f "$SUPER/.git" ] \
	|| blind "$SUPER is not a git repository, so its submodule pin cannot be read"

# The pin is read from the SUPERPROJECT'S TREE, not from the checked-out submodule. That is
# deliberate: an uninitialised submodule is the normal state on a fresh clone, and a gate that
# needed `submodule update` first would go blind exactly when the tree is cheapest to check.
ENTRY="$(git -C "$SUPER" ls-tree HEAD -- "$SUB_PATH" 2>/dev/null)" \
	|| blind "could not read HEAD's tree in $SUPER"
[ -n "$ENTRY" ] || blind "no entry for '$SUB_PATH' in HEAD of $SUPER — wrong path, or the submodule was removed"
case "$ENTRY" in
	*" commit "*) ;;
	*) blind "'$SUB_PATH' exists in $SUPER but is not a submodule (gitlink); it is: $(printf '%s' "$ENTRY" | awk '{print $2}')" ;;
esac
PIN="$(printf '%s' "$ENTRY" | awk '{print $3}')"
case "$PIN" in
	[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) ;;
	*) blind "the gitlink for '$SUB_PATH' is not a hex object id: '$PIN'" ;;
esac

HUB="${OLIVARES_HUB_GIT_DIR:-$SUPER/$SUB_PATH}"
git -C "$HUB" rev-parse --git-dir >/dev/null 2>&1 \
	|| blind "no clone of the submodule's origin to compare against: '$HUB' is not a git repository.
	         Point OLIVARES_HUB_GIT_DIR at one, or initialise the submodule. Without a history
	         this gate cannot tell 'behind by two' from 'a different project'."

if [ "${OLIVARES_PIN_NO_FETCH:-0}" != "1" ]; then
	# Fail-closed on fetch. A gate that measured against a stale local ref while announcing it
	# had checked the remote is the exact defect logged in CLAUDE.md for `fetch ... || true`.
	git -C "$HUB" fetch -q origin "$BRANCH" 2>/dev/null \
		|| blind "could not fetch origin/$BRANCH in $HUB, so the reference head would be whatever this clone happened to have"
	HEAD_SHA="$(git -C "$HUB" rev-parse --verify --quiet FETCH_HEAD 2>/dev/null)"
else
	# TWO REFS CAN BOTH BE CALLED "main" AND DISAGREE, and this gate found that out about
	# ITSELF. Run against the shared hub clone on 2026-08-15 it answered "the pin is NOT an
	# ancestor of main" — because it had resolved the clone's LOCAL branch, which was 100+
	# commits behind, rather than origin/main. A confident, specific, wrong verdict about the
	# most alarming case it can report.
	#
	# So it does not pick a winner: if both exist and they differ, the reference is AMBIGUOUS
	# and that is the third answer, with both names and both hashes printed. Choosing silently
	# is how the wrong one gets used; refusing is how someone fetches.
	_local="$(git -C "$HUB" rev-parse --verify --quiet "refs/heads/$BRANCH" 2>/dev/null || true)"
	_remote="$(git -C "$HUB" rev-parse --verify --quiet "refs/remotes/origin/$BRANCH" 2>/dev/null || true)"
	if [ -n "$_local" ] && [ -n "$_remote" ] && [ "$_local" != "$_remote" ]; then
		blind "two refs are both called '$BRANCH' in $HUB and they disagree:
  refs/heads/$BRANCH          $_local
  refs/remotes/origin/$BRANCH $_remote
	         Measuring against the wrong one produces a confident verdict about a tree nobody has.
	         Fetch, or name the one you mean with OLIVARES_HUB_BRANCH."
	fi
	HEAD_SHA="${_remote:-$_local}"
fi
# --verify is load-bearing, and the battery is what proved it: WITHOUT it `git rev-parse` prints
# the ref NAME back on stdout for a ref that does not exist, so HEAD_SHA came out non-empty
# ("refs/heads/nope") and this fail-closed never fired. The gate then went on to answer STALE for
# a branch it had never found — the wrong answer wearing roughly the right colour, which is the
# shape that survives review.
case "${HEAD_SHA:-}" in
	[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) ;;
	*) blind "could not resolve the head of '$BRANCH' in $HUB (got: '${HEAD_SHA:-}')" ;;
esac

git -C "$HUB" cat-file -e "$PIN^{commit}" 2>/dev/null \
	|| blind "the pinned commit $PIN is not present in $HUB (shallow clone?), so ancestry cannot be decided"

if ! git -C "$HUB" merge-base --is-ancestor "$PIN" "$HEAD_SHA" 2>/dev/null; then
	stale "the pin $(printf '%.9s' "$PIN") is NOT an ancestor of $BRANCH ($(printf '%.9s' "$HEAD_SHA")).
	       It is not behind — it is off that history. A rewritten branch or a pin taken from a
	       fork will read as a small number under any 'commits behind' measure, which is worse
	       than reading as large."
	exit "$RC_STALE"
fi

BEHIND="$(git -C "$HUB" rev-list --count "$PIN..$HEAD_SHA" 2>/dev/null)" \
	|| blind "could not count the commits between the pin and $BRANCH"
PIN_DATE="$(git -C "$HUB" show -s --format=%cs "$PIN" 2>/dev/null)"
HEAD_DATE="$(git -C "$HUB" show -s --format=%cs "$HEAD_SHA" 2>/dev/null)"

# Declared on every run, green or red. The number that would have caught this in August was
# never secret — it was simply never printed anywhere a human or a job would read it.
printf 'pin      %.9s  %s  (%s)\n' "$PIN" "${PIN_DATE:-unknown date}" "$SUB_PATH"
printf '%-8s %.9s  %s\n' "$BRANCH" "$HEAD_SHA" "${HEAD_DATE:-unknown date}"
printf 'behind   %s commit(s)\n' "$BEHIND"

FAILED=0

# --- the property that was actually violated -----------------------------------------------
pin_toolchain() { # <sha> — effective toolchain of go.work at that commit
	git -C "$HUB" show "$1:go.work" 2>/dev/null \
		| awk '/^toolchain /{t=$2} /^go /{g=$2} END{print (t!=""?t:(g!=""?"go" g:""))}'
}
T_PIN="$(pin_toolchain "$PIN")"
T_HEAD="$(pin_toolchain "$HEAD_SHA")"
[ -n "$T_HEAD" ] || blind "go.work at $BRANCH declares no go/toolchain directive, so there is no reference to compare against"
[ -n "$T_PIN" ] || blind "go.work is missing or declares no toolchain at the pinned commit, so what it compiles with is unknown"
printf 'toolchain %s at the pin, %s at %s\n' "$T_PIN" "$T_HEAD" "$BRANCH"
if [ "$T_PIN" != "$T_HEAD" ]; then
	stale "the pinned tree compiles with $T_PIN while $BRANCH is on $T_HEAD.
	       This is the whole reason the gate exists: the artefact built from this pin does NOT
	       contain the standard-library fixes the branch has already taken, and every job on
	       both sides stays green while it happens."
	FAILED=1
fi

# --- release strictness ---------------------------------------------------------------------
if [ "$MODE" = release ] && [ "$PIN" != "$HEAD_SHA" ]; then
	stale "release mode: the pin must BE $BRANCH, and it is $BEHIND commit(s) behind it.
	       A release may not be approximately the tree that was audited."
	FAILED=1
fi

# --- optional extra bound --------------------------------------------------------------------
if [ -n "${OLIVARES_PIN_MAX_BEHIND:-}" ]; then
	case "$OLIVARES_PIN_MAX_BEHIND" in
		''|*[!0-9]*) blind "OLIVARES_PIN_MAX_BEHIND is not a non-negative integer: '$OLIVARES_PIN_MAX_BEHIND'" ;;
	esac
	if [ "$BEHIND" -gt "$OLIVARES_PIN_MAX_BEHIND" ]; then
		stale "the pin is $BEHIND commit(s) behind $BRANCH, over the declared bound of $OLIVARES_PIN_MAX_BEHIND."
		FAILED=1
	fi
fi

[ "$FAILED" -eq 0 ] || exit "$RC_STALE"
printf 'CLEAN — the pin is on %s and compiles with the same toolchain (%s commit(s) behind)\n' "$BRANCH" "$BEHIND"
exit "$RC_CLEAN"
