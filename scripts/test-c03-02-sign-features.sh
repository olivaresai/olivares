#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c03-02-sign-features.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0302.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/cmd/olivares" "$TMP/tree/scripts"
  cp "$ROOT/cmd/olivares/cmd_license.go" "$TMP/tree/cmd/olivares/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c03-02-sign-features.sh"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c03-02-sign-features.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
  return "$rc"
}

stage
if run; then ok "live --features wiring is CLEAN"
else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
sed -i '/StringVar(&featuresCSV, "features"/d' "$TMP/tree/cmd/olivares/cmd_license.go"
if run; then bad "dropped --features flag stayed CLEAN"
else ok "mutant (no --features flag) is killed"; fi

stage
sed -i 's/Features: feats/Features: nil/' "$TMP/tree/cmd/olivares/cmd_license.go"
if run; then bad "Sign without Features stayed CLEAN"
else ok "mutant (flag parsed, Claims.Features empty) is killed"; fi

stage
if ! run; then bad "no-fire: live should stay CLEAN ($(cat "$TMP/err"))"
else ok "no-fire: live --features wiring stays CLEAN"; fi

stage
rm -f "$TMP/tree/cmd/olivares/cmd_license.go"
if run; then bad "missing source stayed CLEAN"
else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing source is COULD NOT LOOK"
  else bad "missing source should be exit 2 ($(cat "$TMP/err"))"; fi
fi

printf 'check-c03-02-sign-features selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
