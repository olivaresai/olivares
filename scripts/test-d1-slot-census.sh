#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-d1-slot-census.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/d1-census.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts" \
           "$TMP/tree/commercial/license-worker/migrations"
  cp "$ROOT/design/d1-slot-census.json" "$TMP/tree/design/"
  cp "$ROOT/commercial/license-worker/migrations/"*.sql \
     "$TMP/tree/commercial/license-worker/migrations/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-d1-slot-census.sh"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-d1-slot-census.sh" >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
  return "$rc"
}

stage
if run; then ok "live census matches the directory"; else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

# Mutant: 0022 CREATE of a published table name (Exit 1 bomb).
stage
printf '%s\n' 'CREATE TABLE IF NOT EXISTS dodo_line_grants (id TEXT);' \
  > "$TMP/tree/commercial/license-worker/migrations/0022_recreate_line_grants.sql"
if run; then bad "0022 file stayed CLEAN"; else ok "mutant (0022 file) is killed"; fi

# Mutant: a slot under the already-applied 0028.
stage
echo '-- placeholder' > "$TMP/tree/commercial/license-worker/migrations/0025_too_low.sql"
if run; then bad "0025 under 0028 stayed CLEAN"; else ok "mutant (prefix < next_free) is killed"; fi

stage
rm -f "$TMP/tree/design/d1-slot-census.json"
if run; then bad "missing census stayed CLEAN"; else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing census is COULD NOT LOOK"
  else bad "missing census should be exit 2 ($(cat "$TMP/err"))"; fi
fi

printf 'check-d1-slot-census selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
