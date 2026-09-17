#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
#
# delta-legs.sh — which pre-push legs does the base have that a branch's tree does NOT?
#
# WHY IT EXISTS. `core.hooksPath` is relative (`.githooks`), so every worktree gates its
# pushes with ITS OWN copy of the hook. Measured 2026-09-01 across twelve live session
# trees: main carried 216 legs and the lanes carried between 170 and 215 — nine distinct
# hook versions. So "my fast lane went green" means "the legs MY TREE KNEW ABOUT passed",
# and a lane that has not rebased in days pushes a branch that has never seen the newest
# gates. That is not a design defect (the hook travels with the tree, and gating a tree
# with its own hook is correct) but it does invalidate reading "green on the branch" as
# "green on main".
#
# Integration policy adopted 2026-09-01: for a lot whose tree is more than ~20 legs behind
# the base, the integrator runs the DELTA over the merged tree, not only the legs chosen by
# the class of the touched files. This prints that delta.
#
# It is TOOLING, not a gate: nothing wires it, nothing depends on its exit code landing
# green, and it is deliberately not in the hook. It answers a question an integrator asks
# before choosing a review; it does not block anything on its own.
#
# THREE ANSWERS, because "no delta" and "I could not look" are different facts:
#   0  no delta — the class legs suffice
#   1  there is a delta — the legs are listed
#   2  COULD NOT LOOK — and it says which of the two hooks it could not read
#
# Usage:  bash scripts/delta-legs.sh <branch-ref> [base-ref]
#         base-ref defaults to origin/main.
#         OLIVARES_DELTA_REPO overrides which repository is asked (default: this one).
set -uo pipefail

RAMA="${1:-}"
BASE="${2:-origin/main}"

if [ -z "$RAMA" ]; then
	echo "delta-legs: COULD NOT LOOK — no branch ref given (usage: delta-legs.sh <branch-ref> [base-ref])" >&2
	exit 2
fi

REPO="${OLIVARES_DELTA_REPO:-}"
if [ -z "$REPO" ]; then
	REPO="$(git rev-parse --show-toplevel 2>/dev/null)" || {
		echo "delta-legs: COULD NOT LOOK — not inside a git repository and OLIVARES_DELTA_REPO is unset" >&2
		exit 2
	}
fi
[ -d "$REPO/.git" ] || [ -f "$REPO/.git" ] || {
	echo "delta-legs: COULD NOT LOOK — $REPO is not a git repository" >&2
	exit 2
}

# Extract the lint: legs a hook invokes, from a REF (never from a path: a shared clone's
# working tree sits at whatever commit someone left it on, and reading by path returns that
# old content without failing).
extrae() {
	local ref="$1" texto
	texto="$(git -C "$REPO" show "${ref}:.githooks/pre-push" 2>/dev/null)" || return 1
	[ -n "$texto" ] || return 1
	# ⛔ The `|| true` are NOT hygiene, and must not be removed as such. A grep with no
	#    matches exits 1; under `pipefail` that 1 propagates out of the group and the caller
	#    reads it as its own third answer. Measured while writing this: the function returned
	#    1 having produced all 216 legs correctly, because the second pattern (the
	#    variable-prefixed form) matches nothing today. A legitimate zero disguised as
	#    blindness — and in a gate that would block a push with no cause.
	{
		{ printf '%s\n' "$texto" | grep -oE '^[[:space:]]*task (lint:[a-zA-Z0-9:_-]+)' | awk '{print $2}'; } || true
		# The canonical probe misses `VAR=1 task lint:x`, which the hook documents as
		# invisible to check-gate-parity.sh for the same reason. Counted here so the figure
		# does not silently undercount — and as of 2026-09-02 that is no longer hypothetical:
		# origin/main has TWO, `OLIVARES_NETWORK_ADVISORY=1 task lint:session-numbers`
		# (.githooks/pre-push:737) and the same form for lint:hub-web-fidelity (:1483), put
		# there on purpose by check-network-observation-wiring.sh, which REFUSES the bare form.
		# Do not delete this second pattern as dead code: it is the half that sees them.
		{ printf '%s\n' "$texto" | grep -oE '^[[:space:]]*[A-Z_][A-Z0-9_]*=[^[:space:]]+ +task +(lint:[a-zA-Z0-9:_-]+)' \
			| grep -oE 'lint:[a-zA-Z0-9:_-]+'; } || true
	} | LC_ALL=C sort -u
}

A="$(extrae "$BASE")" || {
	echo "delta-legs: COULD NOT LOOK — cannot read the hook at ${BASE}" >&2
	exit 2
}
B="$(extrae "$RAMA")" || {
	echo "delta-legs: COULD NOT LOOK — cannot read the hook at ${RAMA}" >&2
	exit 2
}

NA="$(printf '%s\n' "$A" | grep -c . || true)"
NB="$(printf '%s\n' "$B" | grep -c . || true)"
if [ "${NA:-0}" -eq 0 ] || [ "${NB:-0}" -eq 0 ]; then
	echo "delta-legs: COULD NOT LOOK — one of the two leg lists came back empty" >&2
	exit 2
fi

# `comm` demands LEXICOGRAPHIC order, which is what LC_ALL=C sort -u above produces.
DELTA="$(comm -23 <(printf '%s\n' "$A") <(printf '%s\n' "$B") || true)"
ND="$(printf '%s\n' "$DELTA" | grep -c . || true)"
ND="${ND:-0}"

printf 'delta-legs: %s has %s legs · %s has %s · DELTA %s\n' "$BASE" "$NA" "$RAMA" "$NB" "$ND"
if [ "$ND" -gt 0 ]; then
	printf '%s\n' "$DELTA" | sed 's/^/    /'
	printf '  => run these %s over the MERGED tree before publishing (integration policy, ~20 threshold)\n' "$ND"
	exit 1
fi
echo "  => no delta: the legs chosen by file class are enough"
exit 0
