#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c13-07-holds.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c13-07.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts"
  cp "$ROOT/design/PRICING-CANON.md" "$TMP/tree/design/"
  cp "$ROOT/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" "$TMP/tree/design/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/"*.sh
}
run() { OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c13-07-holds.sh" >/dev/null 2>"$TMP/err"; }

stage
if run; then ok "live HOLD + canon growth list is CLEAN"; else bad "live tree should be CLEAN ($(cat "$TMP/err"))"; fi

stage
# Mutant: drop one growth HOLD so a modules_growth slug looks shipping.
python3 - "$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" <<'PY'
import sys
p = sys.argv[1]
text = open(p, encoding="utf-8").read().replace("hold-growth-slug: render-inspector\n", "")
open(p, "w", encoding="utf-8").write(text)
PY
if run; then bad "dropping a growth HOLD stayed CLEAN"; else ok "mutant (drop hold-growth-slug) is killed"; fi

stage
# Mutant: delete the AR-2 row (honest approval) and keep claiming the bar exists.
python3 - "$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" <<'PY'
import re, sys
p = sys.argv[1]
text = re.sub(r"^\| AR-2 \|.*\n", "", open(p, encoding="utf-8").read(), flags=re.M)
open(p, "w", encoding="utf-8").write(text)
PY
if run; then bad "dropping AR-2 stayed CLEAN"; else ok "mutant (drop AR-2 row) is killed"; fi

stage
# Mutant: leak a growth slug into day-one. Permit-half: the buyer of day-one
# AIRS would be promised payload still on HOLD.
python3 - "$TMP/tree/design/PRICING-CANON.md" <<'PY'
import sys
p = sys.argv[1]
text = open(p, encoding="utf-8").read()
text = text.replace("      - hook-firewall\n", "      - hook-firewall\n      - render-inspector\n", 1)
open(p, "w", encoding="utf-8").write(text)
PY
if run; then bad "growth slug in modules_day_one stayed CLEAN"; else ok "mutant (growth in day_one) is killed"; fi

stage
rm -f "$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md"
if run; then bad "missing HOLD stayed CLEAN"; else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing HOLD is COULD NOT LOOK"
  else bad "missing HOLD should be exit 2 ($(cat "$TMP/err"))"; fi
fi

# No-fire: the live tree (this checkout) stays CLEAN.
if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
  ok "no-fire: live HOLD stays CLEAN"
else
  bad "no-fire live HOLD went RED ($(cat "$TMP/err"))"
fi

printf 'check-c13-07-holds selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
