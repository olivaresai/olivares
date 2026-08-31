#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c13-06-canon-proposals.sh — C13-06. Present the three canon
# proposals; do not apply them. Three answers: 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-06-canon-proposals: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-06-canon-proposals: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

CANON="${OLIVARES_C1306_CANON:-design/PRICING-CANON.md}"
DOC="${OLIVARES_C1306_DOC:-design/C13-06-CANON-PROPOSALS-2026-08-19.md}"
WIRE="${OLIVARES_C1306_WIRE:-cmd/olivares/wire_noenterprise.go}"

[ -f "$CANON" ] || cannot "missing $CANON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$WIRE" ] || cannot "missing $WIRE"

# The signed canon must still carry the five keys and the enterprise scope.
grep -q 'modules_day_one:' "$CANON" || fail "$CANON lost modules_day_one: — C13-06 must not apply proposal 1"
grep -q 'modules_growth:' "$CANON" || fail "$CANON lost modules_growth: — C13-06 must not apply proposal 1"
grep -q 'modules_on:' "$CANON" || fail "$CANON lost modules_on: — C13-06 must not apply proposal 1"
grep -q 'modules_hold_gated:' "$CANON" || fail "$CANON lost modules_hold_gated: — C13-06 must not apply proposal 1"
grep -q '    modules:' "$CANON" || fail "$CANON lost modules:"

if ! awk '
  /^  self_hosted.enterprise:/ { p=1; next }
  p && /^  [a-z]/ { exit (found ? 0 : 1) }
  p && /scope:/ { found=1 }
  END { exit (found ? 0 : 1) }
' "$CANON"
then
	fail "$CANON self_hosted.enterprise lost scope: — C13-06 must not apply proposal 2"
fi

grep -q 'retrieval-scan' "$CANON" || fail "$CANON lost retrieval-scan — C13-06 must not unify spelling"
grep -q 'enterprise/computeruse' "$WIRE" || fail "$WIRE lost enterprise/computeruse comment — C13-06 must not apply proposal 3"

# The write-up must present, not apply.
grep -q 'NO ELEGIDO' "$DOC" || fail "$DOC lost NO ELEGIDO"
grep -q 'NO APLICADO' "$DOC" || fail "$DOC lost NO APLICADO"
grep -q 'modules_day_one' "$DOC" && grep -q 'self_hosted.enterprise' "$DOC" && grep -q 'retrievalscan' "$DOC" \
	|| fail "$DOC no longer names the three proposals"
if grep -qiE 'aplicamos la propuesta|applied proposal|canon rewritten' "$DOC"; then
	fail "$DOC claims an application this lote does not have"
fi

say "check-c13-06-canon-proposals: CLEAN — five keys and enterprise scope still in the canon; proposals presented, none applied."
exit 0
