#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# hub-leg.sh — run a Taskfile leg whose SCRIPT only exists in the private hub, with the
# same THREE distinguishable answers scripts/private-leg.sh gives for a hub-only DIRECTORY:
#
#   1. the script exists                 -> run it, propagating its exact exit code
#   2. missing, and this really is the    -> say NOT APPLICABLE where the job summary
#      PUBLIC export                         shows it, naming the script, and exit 0
#   3. missing, anything else            -> exit 1: refuse to guess which tree this is
#
# WHY THIS EXISTS (measured 2026-08-02). The Taskfile ships to the public export VERBATIM,
# but the export curation script curates hub-only tooling OUT of that same export
# (SCRIPTS_BLOCK). A published target that invokes a curated-out script therefore dies in
# the public tree with exit 127 and the shell's own message ("No such file or directory"),
# which names a missing file but not a reason — the published tree looks broken rather than
# curated. That is the same failure shape as the export battery invoking a script the
# export removes (audit F10).
#
# WHAT COUNTS AS "THE PUBLIC EXPORT" (hardened 2026-08-02, adversarial review X-07). The
# first version keyed on `[ -f PUBLIC-EXPORT.md ]` alone. That is a PASSWORD, and one that
# anybody — including a stray `cp`, a half-finished export or a reviewer poking at a hub —
# can type: in a fixture holding this script, an EMPTY PUBLIC-EXPORT.md and no target, the
# wrapper answered NOT APPLICABLE and exited 0. That is exactly the state it promises to
# deny-close. The tree is now classified on two pieces of evidence that a copied file
# cannot fabricate:
#
#   * the marker must carry the SENTENCE the generator stamps (the export curation script
#     writes it into PUBLIC-EXPORT.md before the leak gate). An empty or hand-made file
#     fails this. `--marker-signature` prints that sentence so the mutation matrix can
#     assert the two files still agree — a silent divergence would make every public leg
#     refuse instead of skip.
#   * NO hub-only path may exist. The export removes every one of them (verified against
#     the export curation script --manifest`: 0 occurrences of any HUB_SENTINELS entry). A
#     hub with a copied marker therefore classifies as `hub`, and a missing script there is
#     answer 3 — a loud red — not a green skip.
#
# The absence of the script itself is never the discriminator: a hub that LOST the script
# degrades to answer 3 (a loud red with an explanation), never to a silent green.
#
# Usage: hub-leg.sh <task-name> <script-path-relative-to-root> [args...]
#        hub-leg.sh --classify [--root DIR]     -> prints hub | public | unknown, exit 0
#        hub-leg.sh --marker-signature          -> prints the sentence the export stamps
# Exit codes: the script's own · 0 (not applicable in the public tree) · 1 (missing, and
#             this tree is not a stamped public export) · 2 (usage).
set -euo pipefail

# The sentence the export curation script writes into the marker it stamps. Kept as one
# literal so `--marker-signature` and the matrix can compare it against the generator.
MARKER_SIGNATURE='This repository is the public, curated export of the Olivares AI control plane.'

# Paths the export ALWAYS removes. Presence of any one of them means "this is a development
# tree", whatever a file named PUBLIC-EXPORT.md claims. Kept short and stable on purpose:
# every entry is curated out by name (SCRIPTS_BLOCK / the private-directory block lists),
# not by accident of what a given session happens to have checked out.
#
# export-closure: absent-by-design scripts/export-public.sh — a SENTINEL, not a dependency:
#   this script tests for its ABSENCE to classify the tree and never runs it. The export
#   removing it is the whole point, and the export-closure gate proves that claim
#   (in the hub, and never handed to an execution verb here).
# export-closure: absent-by-design scripts/ai-state.sh — the same, for the hub-internal
#   measuring stick that no shipping surface calls.
HUB_SENTINELS=(
	scripts/export-public.sh
	scripts/ai-state.sh
	sessions
	design
	ESTADO-PROYECTO.md
	CLAUDE.md
)

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="run"

if [ "${1:-}" = "--marker-signature" ]; then
	printf '%s\n' "$MARKER_SIGNATURE"
	exit 0
fi
if [ "${1:-}" = "--classify" ]; then
	MODE="classify"
	shift
	if [ "${1:-}" = "--root" ]; then
		ROOT="$(cd "${2:?--root needs a directory}" && pwd)"
		shift 2
	fi
elif [ "$#" -lt 2 ]; then
	echo "usage: hub-leg.sh <task-name> <script-path> [args...]" >&2
	echo "       hub-leg.sh --classify [--root DIR] | --marker-signature" >&2
	exit 2
fi

# TREE / WHY are set by classify_tree; it prints nothing, so callers keep both halves (a
# `$(...)` capture would drop WHY in the subshell and the refusal would lose its reason).
TREE=""
WHY=""
classify_tree() {
	local s rc=0
	for s in "${HUB_SENTINELS[@]}"; do
		if [ -e "$ROOT/$s" ]; then
			TREE="hub"
			WHY="hub-only path '$s' is present, so this is a development tree"
			return 0
		fi
	done
	if [ ! -f "$ROOT/PUBLIC-EXPORT.md" ]; then
		TREE="unknown"
		WHY="there is no PUBLIC-EXPORT.md marker and no hub-only path either"
		return 0
	fi
	# grep's THREE answers: matched / did not match / could not read. The third must not
	# collapse into the second — an unreadable marker proves nothing about this tree.
	grep -qF -- "$MARKER_SIGNATURE" "$ROOT/PUBLIC-EXPORT.md" || rc=$?
	if [ "$rc" -eq 0 ]; then
		TREE="public"
		WHY="PUBLIC-EXPORT.md carries the export's own signature and no hub-only path exists"
	elif [ "$rc" -eq 1 ]; then
		TREE="unknown"
		WHY="PUBLIC-EXPORT.md exists but does not carry the sentence the export stamps"
		WHY="$WHY (an empty, stale or hand-made marker is not an export)"
	else
		TREE="unknown"
		WHY="PUBLIC-EXPORT.md could not be read (grep exited $rc); nothing about this tree"
		WHY="$WHY was established"
	fi
	return 0
}

if [ "$MODE" = "classify" ]; then
	classify_tree
	printf '%s\n' "$TREE"
	printf 'hub-leg: %s\n' "$WHY" >&2
	exit 0
fi

NAME="$1"
SCRIPT="$2"
shift 2

if [ -f "$ROOT/$SCRIPT" ]; then
	cd "$ROOT"
	exec bash "$SCRIPT" "$@"
fi

classify_tree
if [ "$TREE" = "public" ]; then
	NOTE="NOT APPLICABLE: task '$NAME' — $SCRIPT is hub-only tooling and is not part of"
	NOTE="$NOTE the public tree (see PUBLIC-EXPORT.md). Nothing was checked by this leg,"
	NOTE="$NOTE and in this tree that is the expected state."
	echo "hub-leg: $NOTE"
	if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
		printf '### %s\n\n%s\n\n' "$NAME: NOT APPLICABLE (public tree)" "$NOTE" >>"$GITHUB_STEP_SUMMARY"
	fi
	if [ -n "${GITHUB_ACTIONS:-}" ]; then
		echo "::notice title=$NAME not applicable in the public tree::$NOTE"
	fi
	exit 0
fi

echo "hub-leg: $NAME: $SCRIPT is MISSING and this tree is not a stamped public export." >&2
echo "  Classified as '$TREE': $WHY." >&2
echo "  A complete hub HAS $SCRIPT; a stamped export carries the marker the generator" >&2
echo "  writes and none of the hub-only paths. Refusing to guess: skipping the leg here" >&2
echo "  would report it green against nothing." >&2
exit 1
