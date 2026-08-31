#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c02-08-blobs-ids.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0208.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/scripts" "$TMP/tree/design"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c02-08-blobs-ids.sh"
  cp "$ROOT/design/C02-08-GORELEASER-BLOBS-IDS-2026-08-19.md" "$TMP/tree/design/"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c02-08-blobs-ids.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
  return "$rc"
}

stage
if run; then ok "live C02-08 is CLEAN"
else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
# Delete only the verdict marker, not the title (which also says "admits").
sed -i '/\*\*Yes\.\*\*/d' \
  "$TMP/tree/design/C02-08-GORELEASER-BLOBS-IDS-2026-08-19.md"
if run; then bad "dropped admits-yes stayed CLEAN"
else ok "mutant (drop admits-yes) is killed"; fi

stage
printf '\nThe overlay already publishes per set via blobs.\n' \
  >> "$TMP/tree/design/C02-08-GORELEASER-BLOBS-IDS-2026-08-19.md"
if run; then bad "false overlay-applied claim stayed CLEAN"
else ok "mutant (claim overlay already publishes) is killed"; fi

stage
printf '\nblobs.ids is Pro only.\n' \
  >> "$TMP/tree/design/C02-08-GORELEASER-BLOBS-IDS-2026-08-19.md"
if run; then bad "false Pro claim stayed CLEAN"
else ok "mutant (ids is Pro) is killed"; fi

stage
if ! run; then bad "no-fire: live should stay CLEAN ($(cat "$TMP/err"))"
else ok "no-fire: live write-up stays CLEAN"; fi

stage
rm -f "$TMP/tree/design/C02-08-GORELEASER-BLOBS-IDS-2026-08-19.md"
if run; then bad "missing write-up stayed CLEAN"
else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing write-up is COULD NOT LOOK"
  else bad "missing write-up should be exit 2 ($(cat "$TMP/err"))"; fi
fi

printf 'check-c02-08-blobs-ids selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
