#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c03-03-addon-ids.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0303.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/cmd/olivares" "$TMP/tree/scripts"
  cp "$ROOT/design/PRICING-CANON.md" "$TMP/tree/design/"
  cp "$ROOT/cmd/olivares/cmd_license.go" "$TMP/tree/cmd/olivares/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c03-03-addon-ids.sh"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c03-03-addon-ids.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
  return "$rc"
}

stage
if run; then ok "live fused ids are CLEAN"
else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
sed -i '/self_hosted\.business\.addons\.regulated:/d' "$TMP/tree/design/PRICING-CANON.md"
if run; then bad "dropped regulated from canon stayed CLEAN"
else ok "mutant (canon missing an add-on) is killed"; fi

stage
# Invent a fifth add-on in the CLI allowlist.
sed -i '/"identity-scale",/a\	"business-max",' "$TMP/tree/cmd/olivares/cmd_license.go"
if run; then bad "invented fifth id stayed CLEAN"
else ok "mutant (fifth invented id) is killed"; fi

stage
sed -i '/isFusedCanonAddonID/d' "$TMP/tree/cmd/olivares/cmd_license.go"
if run; then bad "unknown ids accepted stayed CLEAN"
else ok "mutant (no allowlist check) is killed"; fi

stage
if ! run; then bad "no-fire: live should stay CLEAN ($(cat "$TMP/err"))"
else ok "no-fire: live four fused ids stay CLEAN"; fi

stage
rm -f "$TMP/tree/design/PRICING-CANON.md"
if run; then bad "missing canon stayed CLEAN"
else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing canon is COULD NOT LOOK"
  else bad "missing canon should be exit 2 ($(cat "$TMP/err"))"; fi
fi

printf 'check-c03-03-addon-ids selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
