#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-caep-inbound.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/caep-in.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/core/auth" "$TMP/tree/cmd/olivares" "$TMP/tree/scripts"
  cp "$ROOT/core/auth/caep_events.go" "$TMP/tree/core/auth/"
  cp "$ROOT/cmd/olivares/caeptransmitgate.go" "$TMP/tree/cmd/olivares/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-caep-inbound.sh"
}
run() { OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-caep-inbound.sh" >/dev/null 2>"$TMP/err"; }

stage
if run; then ok "live tree is CLEAN"; else bad "live tree should be CLEAN ($(cat "$TMP/err"))"; fi

stage
sed -i '/CAEPReceiveEvent/d' "$TMP/tree/core/auth/caep_events.go"
if run; then bad "deleting CAEPReceiveEvent stayed CLEAN"; else ok "missing receiver is a finding"; fi

stage
sed -i '/open-core (core\/auth\/caep_events.go)/d' "$TMP/tree/cmd/olivares/caeptransmitgate.go"
if run; then bad "dropping the open-core name stayed CLEAN"; else ok "unnamed inbound seam is a finding"; fi

stage
rm -f "$TMP/tree/core/auth/caep_events.go"
if run; then bad "missing file stayed CLEAN"; else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing file is COULD NOT LOOK"
  else bad "missing file should be exit 2 ($(cat "$TMP/err"))"; fi
fi

printf 'check-caep-inbound selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
