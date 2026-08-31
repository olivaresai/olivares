#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# hub-only-gate.sh — take a scoped verdict for a WHOLE Taskfile leg whose INPUTS are
# hub-only, the way scripts/hub-leg.sh does for a leg whose SCRIPT is hub-only.
#
#   1. this tree is the stamped public export -> say what was not checked, and exit 0
#   2. anything else                          -> run the real leg, propagating its code
#
# WHY A GATE-LEVEL VERDICT AND NOT A GUARD PER SCRIPT (measured 2026-08-31).
# lint:addon-sets-gate invokes 217 scripts that copy fixtures out of design/ and
# commercial/, roots the curated export drops ON PURPOSE. From an exported tree with
# `git init`, 182 of them died with `cp: cannot stat .../design/...` and the gate exited 1
# — and the canon's fail-closed rule turns a non-zero fast-lint into a rejected push, so in
# the published tree that leg could not pass anywhere. Guarding them one at a time was
# tried first and is chasing a chain: curing the first script only moved the failure to the
# second, and a chain patched from the front leaves every unpatched link behind it.
#
# WHY NOT `status:` IN THE TASKFILE, which is one line and was the first attempt: go-task
# suppresses a status command's output on BOTH streams, so the leg answered a mute
# `Task "lint:addon-sets-gate" is up to date`. A skip nobody can read is the silent green
# this whole class of bug is about — the reason has to reach the log where the leg ran.
#
# The classification is hub-leg.sh's, deliberately NOT a bare marker file: it keys on the
# sentence the generator stamps AND the absence of every hub-only path, because adversarial
# review X-07 showed that a lone marker is a password a stray `cp` can type.
#
# Usage: hub-only-gate.sh <leg-name> <what-it-checks> <task> [args...]
set -euo pipefail

[ "$#" -ge 3 ] || { echo "usage: hub-only-gate.sh <leg-name> <what-it-checks> <task> [args...]" >&2; exit 2; }
NAME="$1"; SUBJECT="$2"; shift 2
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Fail-closed: any failure to ask (script absent, non-zero, unreadable) leaves TREE empty,
# which is not "public", so the real leg runs and answers for itself.
TREE="$(bash scripts/hub-leg.sh --classify --root "$ROOT" 2>/dev/null || true)"

if [ "$TREE" = "public" ]; then
	NOTE="NOT APPLICABLE: leg '$NAME' verifies $SUBJECT, which is hub-only and curated out"
	NOTE="$NOTE of the public tree (see PUBLIC-EXPORT.md). Nothing was checked by this leg,"
	NOTE="$NOTE and in this tree that is the expected state."
	echo "hub-only-gate: $NOTE"
	if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
		printf '### %s\n\n%s\n\n' "$NAME: NOT APPLICABLE (public tree)" "$NOTE" >>"$GITHUB_STEP_SUMMARY"
	fi
	exit 0
fi

exec task "$@"
