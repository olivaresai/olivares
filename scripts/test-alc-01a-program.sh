#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-alc-01a-program.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/alc01a.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts"
  cp "$ROOT/design/PROGRAMA-ALC-SCIM-GESTIONADO-2026-08-18.md" "$TMP/tree/design/"
  cp "$ROOT/design/ALC-01A-PROGRAM-PREP-2026-08-20.md" "$TMP/tree/design/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-alc-01a-program.sh"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-alc-01a-program.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "program + S1..S4 is CLEAN"
else bad "live pin should be CLEAN ($(cat "$TMP/err"))"; fi

stage
sed -i '/ALC-01-S3/d' "$TMP/tree/design/PROGRAMA-ALC-SCIM-GESTIONADO-2026-08-18.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (drop S3) is killed"
else bad "dropped S3 stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
sed -i 's/No es el motor/Ya es el motor/' \
  "$TMP/tree/design/PROGRAMA-ALC-SCIM-GESTIONADO-2026-08-18.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (program claims it is the motor) is killed"
else bad "motor claim stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/design/PROGRAMA-ALC-SCIM-GESTIONADO-2026-08-18.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing program is COULD NOT LOOK"
else bad "missing program rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: live pin stays CLEAN"
else bad "no-fire should stay CLEAN ($(cat "$TMP/err"))"; fi

echo "check-alc-01a-program selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
