#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Self-test for check-eco11-reserve-funded.sh. Each case names the
# guard it would kill if deleted.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-eco11-reserve-funded.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/eco11.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/scripts" \
           "$TMP/tree/design/preparado"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-eco11-reserve-funded.sh"
  cp "$ROOT/design/ECO-11-RESERVE-FUNDED-PACKAGE-2026-08-19.md" "$TMP/tree/design/"
  cp "$ROOT/design/preparado/AB-01-RESERVA-HOJA-DE-DECISION-2026-08-01.md" \
    "$TMP/tree/design/preparado/"
  cp "$ROOT/design/PRICING-CANON.md" "$TMP/tree/design/"
}

run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-eco11-reserve-funded.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
  return "$rc"
}

stage
if run; then
  ok "the live package is CLEAN"
else
  bad "the live package should be CLEAN ($(cat "$TMP/err"))"
fi

stage
sed -i 's/selected_rate_row: UNKNOWN/selected_rate_row: S-B/' \
  "$TMP/tree/design/PRICING-CANON.md"
if run; then
  bad "selected_rate_row decided in canon stayed CLEAN"
else
  ok "mutant (AF-02 decided without AB-01) is killed"
fi

stage
sed -i 's/funded_amount: UNKNOWN/funded_amount: 10000/' \
  "$TMP/tree/design/PRICING-CANON.md"
if run; then
  bad "invented funded_amount stayed CLEAN"
else
  ok "mutant (reserve amount invented) is killed"
fi

stage
sed -i 's/refund_fee_adder: UNKNOWN/refund_fee_adder: 0/g' \
  "$TMP/tree/design/PRICING-CANON.md"
if run; then
  bad "U_f set to zero stayed CLEAN"
else
  ok "mutant (UNKNOWN used as zero for U_f) is killed"
fi

stage
sed -i '/\[ \] ADOPTO S-B/d' \
  "$TMP/tree/design/preparado/AB-01-RESERVA-HOJA-DE-DECISION-2026-08-01.md"
if run; then
  bad "AB-01 without the S-B box stayed CLEAN"
else
  ok "mutant (signature vehicle lost S-B) is killed"
fi

stage
sed -i -e 's/NO se decide/YA se decide/' -e 's/No se decide/Ya se decide/' \
  "$TMP/tree/design/ECO-11-RESERVE-FUNDED-PACKAGE-2026-08-19.md"
if run; then
  bad "package claiming it decided stayed CLEAN"
else
  ok "mutant (package dictates the row) is killed"
fi

stage
printf '\nfunded_amount: $25000\n' \
  >> "$TMP/tree/design/ECO-11-RESERVE-FUNDED-PACKAGE-2026-08-19.md"
if run; then
  bad "package with a funded dollar stayed CLEAN"
else
  ok "mutant (package smuggles an amount) is killed"
fi

stage
if ! run; then
  bad "no-fire: live package should stay CLEAN ($(cat "$TMP/err"))"
else
  ok "no-fire: live presentable package stays CLEAN"
fi

stage
rm -f "$TMP/tree/design/ECO-11-RESERVE-FUNDED-PACKAGE-2026-08-19.md"
if run; then
  bad "missing package stayed CLEAN"
else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then
    ok "missing package is COULD NOT LOOK"
  else
    bad "missing package should be exit 2 ($(cat "$TMP/err"))"
  fi
fi

printf 'check-eco11-reserve-funded selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
