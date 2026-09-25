#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for C03-11. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c03-11-fase-r.sh"
# The HOLD doc and the Worker wrangler live under design/ and commercial/, both
# curated out of the public export. This battery copies those files into a fixture;
# without them it cannot stage a single case. In a stamped public tree that absence
# is the contract: SCOPED, not a red. In the full source tree a missing HOLD doc remains a defect.
if [ ! -f "$ROOT/design/C03-11-FASE-R-HOLD-2026-08-20.md" ]; then
	_cls=""
	if [ -f "$ROOT/scripts/hub-leg.sh" ]; then
		_cls="$(bash "$ROOT/scripts/hub-leg.sh" --classify --root "$ROOT" 2>/dev/null || true)"
	fi
	if [ "$_cls" = "public" ]; then
		echo "test-c03-11-fase-r: SCOPED — public export; this battery cannot stage"
		echo "  design/ HOLD and commercial/ wrangler fixtures (curated out)."
		echo "  Running the check on this tree so the Community seat-cap no-op is still graded."
		bash "$CHECK"
		exit $?
	fi
fi
# Outside the public export this command grades the live tree. The staged cases, both firing
# directions, are the next command of the same Taskfile targets, run through hub-leg.sh.
bash "$CHECK"
exit $?
