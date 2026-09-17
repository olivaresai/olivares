#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Route a gate that has both public and private verification tasks.
# A successful public classification selects the applicable public task.
# Every other result selects the complete private task, which checks its inputs.
# Classification stays in hub-leg.sh; both tasks propagate failures unchanged.
# Usage: edition-split-gate.sh <name> <private-inputs> <public-task> <private-task>
set -euo pipefail

[ "$#" -eq 4 ] || {
	echo "usage: edition-split-gate.sh <leg-name> <what-is-not-checked> <public-task> <private-task>" >&2
	exit 2
}
NAME="$1"; SUBJECT="$2"; PUBLIC_TASK="$3"; PRIVATE_TASK="$4"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Only a successful classifier can select the public task. Discard partial output
# on failure; the complete private sequence then enforces its own prerequisites.
if ! TREE="$(bash scripts/hub-leg.sh --classify --root "$ROOT" 2>/dev/null)"; then
	TREE=unknown
fi

if [ "$TREE" != "public" ]; then
	exec task "$PRIVATE_TASK"
fi

NOTE="PARTIALLY APPLICABLE: leg '$NAME' also verifies $SUBJECT, which is hub-only and curated"
NOTE="$NOTE out of the public tree (see PUBLIC-EXPORT.md). Those entries are NOT checked here."
NOTE="$NOTE The entries that do verify this tree run now, as '$PUBLIC_TASK', and a failure in"
NOTE="$NOTE any of them fails this leg exactly as it would in the hub."
echo "edition-split-gate: $NOTE"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
	printf '### %s\n\n%s\n\n' "$NAME: PARTIALLY APPLICABLE (public tree)" "$NOTE" >>"$GITHUB_STEP_SUMMARY"
fi

exec task "$PUBLIC_TASK"
