#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c05-33-portal.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c05-33.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/scripts" \
           "$TMP/tree/commercial/license-worker/src/portal/pages" \
           "$TMP/tree/commercial/license-worker/test"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c05-33-portal.sh"
  cp "$ROOT/commercial/license-worker/src/portal/pages/cloud.ts" \
    "$TMP/tree/commercial/license-worker/src/portal/pages/"
  cp "$ROOT/commercial/license-worker/test/cloud-portal.test.ts" \
    "$TMP/tree/commercial/license-worker/test/"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c05-33-portal.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
  return "$rc"
}

stage
if run; then ok "live C05-33 page is CLEAN"
else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
sed -i 's/Get started with Cloud Standard/Get started with a Pro or Business plan/' \
  "$TMP/tree/commercial/license-worker/src/portal/pages/cloud.ts"
if run; then bad "Pro-or-Business CTA stayed CLEAN"
else ok "mutant (Pro or Business CTA) is killed"; fi

stage
sed -i 's/"cloud-standard-m": "Cloud Standard"/"pro": "Pro"/' \
  "$TMP/tree/commercial/license-worker/src/portal/pages/cloud.ts"
if run; then bad "pro titled as Pro stayed CLEAN"
else ok "mutant (pro→Pro map) is killed"; fi

stage
sed -i '/Cloud Standard/d' \
  "$TMP/tree/commercial/license-worker/src/portal/pages/cloud.ts"
if run; then bad "page without Cloud Standard stayed CLEAN"
else ok "mutant (drop Cloud Standard) is killed"; fi

stage
if ! run; then bad "no-fire: live C05-33 should stay CLEAN ($(cat "$TMP/err"))"
else ok "no-fire: live Cloud page stays CLEAN"; fi

stage
rm -f "$TMP/tree/commercial/license-worker/src/portal/pages/cloud.ts"
if run; then bad "missing page stayed CLEAN"
else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing page is COULD NOT LOOK"
  else bad "missing page should be exit 2 ($(cat "$TMP/err"))"; fi
fi

printf 'check-c05-33-portal selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
