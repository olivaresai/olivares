#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C03-20: named 403 + paid_through boundary. Three answers: 0 / 1 / 2.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-20-named-403: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-20-named-403: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"
DOC=design/C03-20-NAMED-403-2026-08-19.md
[ -r "$DOC" ] || cannot "missing $DOC"
grep -q 'Mute 403: gone. Paid-through boundary: present.' "$DOC" \
  || fail "census no longer says the two defects are closed"

GATE=commercial/license-worker/src/download/gate.ts
DB=commercial/license-worker/src/store/db.ts
[ -r "$GATE" ] || cannot "missing $GATE"
[ -r "$DB" ] || cannot "missing $DB"

grep -q 'function namedDownloadForbidden' "$GATE" \
  || fail "namedDownloadForbidden missing"
grep -q 'namedDownloadForbidden' "$GATE" \
  || fail "gate no longer calls the named helper"
# Both paths must call it (binary + manifest).
n=$(grep -c 'namedDownloadForbidden' "$GATE" || true)
[ "$n" -ge 3 ] || fail "named helper not used on both paths (hits=$n)"

if grep -n 'Forbidden: no active license' "$GATE" >/dev/null; then
  fail "mute 403 string is still in the gate"
fi

grep -q "status IN ('active', 'terminated')" "$DB" \
  || fail "getActiveLicenseByHolder still requires status=active only"
grep -q 'explainDownloadRefusal' "$DB" \
  || fail "D1 store missing explainDownloadRefusal"

TST=commercial/license-worker/test/c03-20-named-403.test.ts
[ -r "$TST" ] || cannot "missing $TST"
grep -q 'terminate during the paid term still opens the binary' "$TST" \
  || fail "during-term serve test missing"
grep -q 'paid term ended' "$TST" \
  || fail "named paid-term 403 test missing"

say "check-c03-20-named-403: CLEAN — mute 403 gone; paid-through boundary present."
exit 0
