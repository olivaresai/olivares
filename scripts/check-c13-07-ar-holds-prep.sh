#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C13-07 unique leftover unique vs #1027: AR-1..AR-4 rows on the HOLD
# file already on origin/main. Does not copy check-c13-07-holds.sh.
# Three answers: 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-07-ar-holds-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-07-ar-holds-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

HOLD="${OLIVARES_C1307_HOLD:-design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md}"
DOC="${OLIVARES_C1307_DOC:-design/C13-07-AR-HOLDS-PREP-2026-08-20.md}"

[ -r "$HOLD" ] || cannot "missing $HOLD"
[ -r "$DOC" ] || cannot "missing $DOC"

grep -q 'Unique leftover unique vs `#1027`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1027"
grep -q 'Does not construct the five' "$DOC" \
  || fail "prepare doc no longer says it does not construct the five growth modules"

grep -q '| Id | Criterio medible | Evidencia |' "$HOLD" \
  || fail "HOLD doc lost the measurable-criteria table header"
for ar in AR-1 AR-2 AR-3 AR-4; do
  grep -q "| $ar |" "$HOLD" || fail "HOLD doc is missing the $ar row"
done

say "check-c13-07-ar-holds-prep: CLEAN — AR-1..AR-4 HOLD rows present; growth not built."
exit 0
