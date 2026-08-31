#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-eco11-reserve-funded.sh — ECO-11. The RESERVE-FUNDED package
# is presentable. Signs the row, amount and custody. This check
# refuses a silent decision. Three answers. It does not fund anything.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-eco11-reserve-funded: FAIL — $*" >&2; exit 1; }
cannot() { say "check-eco11-reserve-funded: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

PKG=design/ECO-11-RESERVE-FUNDED-PACKAGE-2026-08-19.md
AB=design/preparado/AB-01-RESERVA-HOJA-DE-DECISION-2026-08-01.md
CANON=design/PRICING-CANON.md
[ -r "$PKG" ]   || cannot "missing $PKG"
[ -r "$AB" ]    || cannot "missing $AB"
[ -r "$CANON" ] || cannot "missing $CANON"

grep -q 'Se presenta' "$PKG" \
  || fail "package no longer says it presents"
grep -Eq 'NO se decide|No se decide' "$PKG" \
  || fail "package no longer says it does not decide"
grep -q 'AB-01' "$PKG" \
  || fail "package lost the AB-01 signature vehicle"
grep -q 'no firma, no fondea' "$PKG" \
  || fail "package no longer refuses to dictate"

grep -q '\[ \] ADOPTO S-B' "$AB" \
  || fail "AB-01 lost the S-B signature box"
grep -q '\[ \] ELIJO cuenta a la vista' "$AB" \
  || fail "AB-01 lost the custody signature box"
grep -q '\[ \] MANTENGO checkout Cloud cerrado' "$AB" \
  || fail "AB-01 lost the closed-checkout signature box"

# A decision that landed in the canon without the boxes is the defect.
# Every occurrence must stay UNKNOWN: one leftover UNKNOWN would hide a
# sibling that was filled in.
still_open() {
  local key="$1" what="$2"
  local lines
  lines="$(grep -E "^[[:space:]]*${key}:" "$CANON" || true)"
  [ -n "$lines" ] || fail "$what ($key) disappeared from the canon"
  if grep -qv 'UNKNOWN' <<<"$lines"; then
    fail "$what ($key) is no longer UNKNOWN"
  fi
}
still_open selected_rate_row "AF-02 row"
still_open refund_fee_adder "U_f"
still_open dispute_fee_adder "U_d"
still_open funded_amount "funded_amount"
grep -qE '^[[:space:]]*recommended_rate_row:[[:space:]]+S-B' "$CANON" \
  || fail "recommended_rate_row is no longer S-B (the model candidate, not a signature)"
grep -qE '^[[:space:]]*checkout_when_unknown_used_as_zero:[[:space:]]+forbidden' "$CANON" \
  || fail "using UNKNOWN as zero is no longer forbidden"
grep -q 'RESERVE-FUNDED:.*founder-approved-row-amount-and-custody' "$CANON" \
  || fail "RESERVE-FUNDED no longer requires founder approval of row, amount and custody"

# The package itself must not smuggle a funded dollar amount.
if grep -Eiq 'funded_amount:[[:space:]]*\$?[0-9]' "$PKG"; then
  fail "package states a funded_amount number — the amount is the owner's"
fi

say "check-eco11-reserve-funded: CLEAN — package presentable; AB-01 boxes open; row/U_f/U_d/amount UNKNOWN; no dictamen."
exit 0
