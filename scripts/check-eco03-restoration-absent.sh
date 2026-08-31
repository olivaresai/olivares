#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ECO-03: authorized-restoration must not be silently invented. While
# the 0013 entitlement_authority file is absent AND refund.requested
# does not terminate, CLEAN. A new 0013 or a terminate-on-requested
# path is a FINDING — implement the restoration then, do not hush.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-eco03-restoration-absent: FAIL — $*" >&2; exit 1; }
cannot() { say "check-eco03-restoration-absent: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"
MIG="commercial/license-worker/migrations"
SRC="commercial/license-worker/src"
[ -d "$MIG" ] || cannot "missing $MIG"
[ -d "$SRC" ] || cannot "missing $SRC"

hits=0
if ls "$MIG"/0013_*.sql >/dev/null 2>&1; then
  say "check-eco03-restoration-absent: 0013 exists — restoration must be implemented, not this CHECK" >&2
  hits=1
fi
if grep -R --include='*.ts' --include='*.sql' -n 'authorized-restoration\|AuthorizedRestoration' "$SRC" "$MIG" >/dev/null 2>&1; then
  say "check-eco03-restoration-absent: authorized-restoration appeared in src/migrations" >&2
  hits=1
fi
if grep -R --include='*.ts' -n 'refund.requested' "$SRC" >/dev/null 2>&1; then
  say "check-eco03-restoration-absent: refund.requested is now parsed — restoration case is live" >&2
  hits=1
fi
[ "$hits" -eq 0 ] || fail "the open case now has a surface; implement authorized-restoration in that lote"
say "check-eco03-restoration-absent: CLEAN — no 0013, no restoration symbol, no refund.requested terminate path."
exit 0
