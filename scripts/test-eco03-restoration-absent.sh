#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-eco03-restoration-absent.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/eco03.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/commercial/license-worker/migrations" \
           "$TMP/tree/commercial/license-worker/src/dodo" \
           "$TMP/tree/scripts"
  cp "$ROOT/commercial/license-worker/migrations/"*.sql \
    "$TMP/tree/commercial/license-worker/migrations/" 2>/dev/null || true
  cp -a "$ROOT/commercial/license-worker/src/." \
    "$TMP/tree/commercial/license-worker/src/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-eco03-restoration-absent.sh"
}
run() { OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-eco03-restoration-absent.sh" >/dev/null 2>"$TMP/err"; }

stage
if run; then ok "live worker is CLEAN"; else bad "live worker should be CLEAN ($(cat "$TMP/err"))"; fi

stage
printf '%s\n' '-- fake 0013' 'SELECT 1;' > "$TMP/tree/commercial/license-worker/migrations/0013_entitlement_authority.sql"
if run; then bad "invented 0013 stayed CLEAN"; else ok "a new 0013 is a finding"; fi

stage
printf 'export const x = "authorized-restoration";\n' \
  > "$TMP/tree/commercial/license-worker/src/restore.ts"
if run; then bad "restoration symbol stayed CLEAN"; else ok "authorized-restoration in src is a finding"; fi

stage
printf 'export const t = "refund.requested";\n' \
  > "$TMP/tree/commercial/license-worker/src/dodo/requested.ts"
if run; then bad "refund.requested stayed CLEAN"; else ok "refund.requested parse path is a finding"; fi

stage
rm -rf "$TMP/tree/commercial/license-worker/migrations"
if run; then bad "missing migrations stayed CLEAN"; else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing migrations is COULD NOT LOOK"
  else bad "missing migrations should be exit 2 ($(cat "$TMP/err"))"; fi
fi

printf 'check-eco03-restoration-absent selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
