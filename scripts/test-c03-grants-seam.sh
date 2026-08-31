#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c03-grants-seam.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c03-grants.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/scripts" "$TMP/tree/cmd/olivares"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c03-grants-seam.sh"
  cp "$ROOT/cmd/olivares/license_holder.go" \
     "$ROOT/cmd/olivares/boot.go" \
     "$ROOT/cmd/olivares/wire_noenterprise.go" \
     "$ROOT/cmd/olivares/seatcapwire.go" \
     "$TMP/tree/cmd/olivares/"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c03-grants-seam.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
  return "$rc"
}

stage
if run; then ok "live seam is CLEAN"
else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
sed -i '/func (h \*licenseHolder) grants()/d' "$TMP/tree/cmd/olivares/license_holder.go"
if run; then bad "grants() dropped stayed CLEAN"
else ok "mutant (drop grants) is killed"; fi

stage
sed -i 's/bindEnterpriseEntitlement(licHolder.grants)/bindEnterpriseEntitlement(nil)/' \
  "$TMP/tree/cmd/olivares/boot.go"
if run; then bad "boot unbound stayed CLEAN"
else ok "mutant (boot does not bind grants) is killed"; fi

stage
sed -i 's/func bindEnterpriseEntitlement(_ licenseGrantsFunc) {}/func bindEnterpriseEntitlement(_ licenseGrantsFunc) { panic("gate") }/' \
  "$TMP/tree/cmd/olivares/wire_noenterprise.go"
if run; then bad "AGPL binder that gates stayed CLEAN"
else ok "mutant (AGPL no-op became a gate) is killed"; fi

stage
if ! run; then bad "no-fire: live seam should stay CLEAN ($(cat "$TMP/err"))"
else ok "no-fire: live AGPL seam stays CLEAN"; fi

stage
rm -f "$TMP/tree/cmd/olivares/boot.go"
if run; then bad "missing boot.go stayed CLEAN"
else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing boot.go is COULD NOT LOOK"
  else bad "missing boot.go should be exit 2 ($(cat "$TMP/err"))"; fi
fi

printf 'check-c03-grants-seam selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
