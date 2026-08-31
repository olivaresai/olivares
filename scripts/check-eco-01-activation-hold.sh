#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ECO-01 HOLD: A.2 activation by module list is signed and still missing.
# Three answers: 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-eco-01-activation-hold: FAIL — $*" >&2; exit 1; }
cannot() { say "check-eco-01-activation-hold: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

DOC="${OLIVARES_ECO01_DOC:-design/ECO-01-ACTIVATION-HOLD-2026-08-20.md}"
CANON="${OLIVARES_ECO01_CANON:-design/PRICING-CANON.md}"

[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$CANON" ] || cannot "missing $CANON"

grep -q 'Hoy NO existe' "$DOC" \
  || fail "prepare doc lost Hoy NO existe"
grep -q 'eco-01-implemented: no' "$DOC" \
  || fail "prepare doc lost eco-01-implemented: no"
grep -q 'activation: customer-toggleable-per-module' "$CANON" \
  || fail "PRICING-CANON lost A.2 activation: customer-toggleable-per-module"
grep -q 'Hoy NO existe' "$CANON" \
  || fail "PRICING-CANON lost Hoy NO existe — the hole this lote pins"
if grep -qiE 'ya existe el punto de entrada|--modules shipped' "$DOC"; then
  fail "prepare doc claims the entry exists"
fi

say "check-eco-01-activation-hold: CLEAN — A.2 layer 3 signed; Hoy NO existe; this lote does not add it."
exit 0
