#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c02-09-cost.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0209.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/scripts" "$TMP/tree/design"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c02-09-cost.sh"
  cp "$ROOT/design/C02-09-MATRIZ-COSTE-2026-08-19.md" "$TMP/tree/design/"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c02-09-cost.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
  return "$rc"
}

stage
if run; then ok "live C02-09 is CLEAN"
else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
sed -i '/4412/d' "$TMP/tree/design/C02-09-MATRIZ-COSTE-2026-08-19.md"
if run; then bad "dropped 4412 stayed CLEAN"
else ok "mutant (drop live total) is killed"; fi

stage
printf '\nThe live tarball on R2 is 266 MB remasured live.\n' \
  >> "$TMP/tree/design/C02-09-MATRIZ-COSTE-2026-08-19.md"
if run; then bad "false 266-live claim stayed CLEAN"
else ok "mutant (266 MB remasured live) is killed"; fi

stage
printf '\n4,8 GB is this matrix.\n' \
  >> "$TMP/tree/design/C02-09-MATRIZ-COSTE-2026-08-19.md"
if run; then bad "4,8 GB-as-this-matrix stayed CLEAN"
else ok "mutant (4,8 GB is this matrix) is killed"; fi

stage
printf '\nprod R2 has a tarball of the commercial binary.\n' \
  >> "$TMP/tree/design/C02-09-MATRIZ-COSTE-2026-08-19.md"
if run; then bad "false prod-has-tarball stayed CLEAN"
else ok "mutant (prod has artifacts) is killed"; fi

stage
if ! run; then bad "no-fire: live should stay CLEAN ($(cat "$TMP/err"))"
else ok "no-fire: live write-up stays CLEAN"; fi

stage
rm -f "$TMP/tree/design/C02-09-MATRIZ-COSTE-2026-08-19.md"
if run; then bad "missing write-up stayed CLEAN"
else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing write-up is COULD NOT LOOK"
  else bad "missing write-up should be exit 2 ($(cat "$TMP/err"))"; fi
fi

printf 'check-c02-09-cost selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
