#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-unit-econ-unknowns.sh — ECO-05. U_f / U_d stay UNKNOWN until the
# Dodo account panel closes them. A public $1 refund fee is not U_f.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-unit-econ-unknowns: FAIL — $*" >&2; exit 1; }
cannot() { say "check-unit-econ-unknowns: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"
CANON=design/PRICING-CANON.md
SNAP=design/UNIT-ECONOMICS-SOURCED-2026-08-18.md
[ -r "$CANON" ] || cannot "missing $CANON"
[ -r "$SNAP" ] || cannot "missing $SNAP"

grep -q 'refund_fee_adder: UNKNOWN' "$CANON" || fail "canon lost refund_fee_adder: UNKNOWN"
grep -q 'dispute_fee_adder: UNKNOWN' "$CANON" || fail "canon lost dispute_fee_adder: UNKNOWN"
grep -q '| `U_f` refund adder above the $30 floor' "$SNAP" || cannot "snapshot lost the U_f row"
grep -q '\*\*UNKNOWN\*\*' "$SNAP" || fail "snapshot no longer marks U_f/U_d UNKNOWN"
# Only an ASSIGNED numeric adder is a finding. Prose that names the
# public one-dollar fee next to the symbol is the warning, not the fill.
if grep -E '(^|[^`])U_f:[[:space:]]*[0-9]|refund_fee_adder:[[:space:]]*[0-9]' "$SNAP" >/dev/null; then
  fail "snapshot assigned a numeric U_f (DODO-4 still open; public marketing is not the adder)"
fi
say "check-unit-econ-unknowns: CLEAN — U_f and U_d stay UNKNOWN."
exit 0
