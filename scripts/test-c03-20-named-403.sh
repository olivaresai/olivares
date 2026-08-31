#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c03-20-named-403.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0320.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
    "$TMP/tree/commercial/license-worker/src/download" \
    "$TMP/tree/commercial/license-worker/src/store" \
    "$TMP/tree/commercial/license-worker/test"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c03-20-named-403.sh"
  cp "$ROOT/design/C03-20-NAMED-403-2026-08-19.md" "$TMP/tree/design/"
  cp "$ROOT/commercial/license-worker/src/download/gate.ts" \
    "$TMP/tree/commercial/license-worker/src/download/"
  cp "$ROOT/commercial/license-worker/src/store/db.ts" \
    "$TMP/tree/commercial/license-worker/src/store/"
  cp "$ROOT/commercial/license-worker/test/c03-20-named-403.test.ts" \
    "$TMP/tree/commercial/license-worker/test/"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c03-20-named-403.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
  return "$rc"
}

stage
if run; then ok "live C03-20 census is CLEAN"
else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
printf '\nreturn text("Forbidden: no active license", 403);\n' \
  >> "$TMP/tree/commercial/license-worker/src/download/gate.ts"
if run; then bad "mute 403 string stayed CLEAN"
else ok "mutant (restore mute 403) is killed"; fi

stage
sed -i "s/status IN ('active', 'terminated')/status = 'active'/" \
  "$TMP/tree/commercial/license-worker/src/store/db.ts"
if run; then bad "active-only predicate stayed CLEAN"
else ok "mutant (cut downloads on cancel day 2) is killed"; fi

stage
sed -i '/function namedDownloadForbidden/d' \
  "$TMP/tree/commercial/license-worker/src/download/gate.ts"
if run; then bad "deleted named helper stayed CLEAN"
else ok "mutant (drop namedDownloadForbidden) is killed"; fi

stage
if ! run; then bad "no-fire: live should stay CLEAN ($(cat "$TMP/err"))"
else ok "no-fire: live C03-20 stays CLEAN"; fi

stage
rm -f "$TMP/tree/design/C03-20-NAMED-403-2026-08-19.md"
if run; then bad "missing census stayed CLEAN"
else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing census is COULD NOT LOOK"
  else bad "missing census should be exit 2 ($(cat "$TMP/err"))"; fi
fi

printf 'check-c03-20-named-403 selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
