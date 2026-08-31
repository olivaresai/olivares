#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c03-21-channel-right.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0321.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
    "$TMP/tree/commercial/license-worker/src/download" \
    "$TMP/tree/commercial/license-worker/test"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c03-21-channel-right.sh"
  cp "$ROOT/design/C03-21-CHANNEL-RIGHT-2026-08-19.md" "$TMP/tree/design/"
  cp "$ROOT/commercial/license-worker/src/download/manifests.ts" \
    "$TMP/tree/commercial/license-worker/src/download/"
  cp "$ROOT/commercial/license-worker/src/download/gate.ts" \
    "$TMP/tree/commercial/license-worker/src/download/"
  cp "$ROOT/commercial/license-worker/test/c03-21-channel-right.test.ts" \
    "$TMP/tree/commercial/license-worker/test/"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c03-21-channel-right.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
  return "$rc"
}

stage
if run; then ok "live C03-21 census is CLEAN"
else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
sed -i 's/channelIsSelfServe(channel)/true/' \
  "$TMP/tree/commercial/license-worker/src/download/gate.ts"
if run; then bad "lts treated as self-serve stayed CLEAN"
else ok "mutant (serve lts to any live licence) is killed"; fi

stage
sed -i '/export function channelIsSelfServe/d' \
  "$TMP/tree/commercial/license-worker/src/download/manifests.ts"
if run; then bad "deleted channelIsSelfServe stayed CLEAN"
else ok "mutant (drop channelIsSelfServe) is killed"; fi

stage
if ! run; then bad "no-fire: live should stay CLEAN ($(cat "$TMP/err"))"
else ok "no-fire: live C03-21 stays CLEAN"; fi

stage
rm -f "$TMP/tree/design/C03-21-CHANNEL-RIGHT-2026-08-19.md"
if run; then bad "missing census stayed CLEAN"
else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing census is COULD NOT LOOK"
  else bad "missing census should be exit 2 ($(cat "$TMP/err"))"; fi
fi

printf 'check-c03-21-channel-right selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
