#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-unit-econ-unknowns.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/unit-econ.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts"
  cp "$ROOT/design/PRICING-CANON.md" "$ROOT/design/UNIT-ECONOMICS-SOURCED-2026-08-18.md" "$TMP/tree/design/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-unit-econ-unknowns.sh"
}
run() { OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-unit-econ-unknowns.sh" >/dev/null 2>"$TMP/err"; }

stage
if run; then ok "live snapshot keeps UNKNOWN"; else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/UNIT-ECONOMICS-SOURCED-2026-08-18.md" <<'PY'
import sys
p=sys.argv[1]
t=open(p,encoding="utf-8").read()
t=t.replace("refund_fee_adder: UNKNOWN", "refund_fee_adder: 1", 1)
# Also plant an assigned adder in the snapshot so the check has
# something to refuse even if the canon line is the only hit.
t=t.replace("**UNKNOWN**", "U_f: 1", 1)
open(p,"w",encoding="utf-8").write(t)
PY
if run; then bad "assigning a numeric U_f stayed CLEAN"; else ok "mutant (numeric U_f) is killed"; fi

stage
rm -f "$TMP/tree/design/PRICING-CANON.md"
if run; then bad "missing canon stayed CLEAN"; else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing canon is COULD NOT LOOK"
  else bad "missing canon should be exit 2 ($(cat "$TMP/err"))"; fi
fi

printf 'check-unit-econ-unknowns selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
