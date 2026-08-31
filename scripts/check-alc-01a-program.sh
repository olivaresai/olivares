#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ALC-01a unique leftover unique vs #956: the managed-SCIM PROGRAM is on
# main. This CHECK pins it. Does not build the motor.
# Three answers: 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-alc-01a-program: FAIL — $*" >&2; exit 1; }
cannot() { say "check-alc-01a-program: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

PROG="${OLIVARES_ALC01A_PROG:-design/PROGRAMA-ALC-SCIM-GESTIONADO-2026-08-18.md}"
DOC="${OLIVARES_ALC01A_DOC:-design/ALC-01A-PROGRAM-PREP-2026-08-20.md}"

[ -r "$PROG" ] || cannot "missing $PROG"
[ -r "$DOC" ] || cannot "missing $DOC"

grep -q 'Unique leftover unique vs `#956`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #956"
grep -q 'No es el motor' "$DOC" \
  || fail "prepare doc lost No es el motor"
grep -q 'ALC-01 no arranca sin esto' "$DOC" \
  || fail "prepare doc lost ALC-01 no arranca sin esto"

grep -q 'No es el motor' "$PROG" \
  || fail "program lost No es el motor"
grep -q 'ALC-01 no arranca sin esto' "$PROG" \
  || fail "program lost ALC-01 no arranca sin esto"
grep -q 'ALC-01-S1' "$PROG" || fail "program lost ALC-01-S1"
grep -q 'ALC-01-S2' "$PROG" || fail "program lost ALC-01-S2"
grep -q 'ALC-01-S3' "$PROG" || fail "program lost ALC-01-S3"
grep -q 'ALC-01-S4' "$PROG" || fail "program lost ALC-01-S4"
grep -Fq '**no es** managed SCIM' "$PROG" \
  || fail "program lost inbound is not managed SCIM"

say "check-alc-01a-program: CLEAN — SCIM program on main; S1..S4 named; not the motor."
exit 0
