#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-int-12-no-land-prep.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/int12.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts"
  cp "$ROOT/design/INT-12-NO-LAND-ENT58-2026-08-20.md" "$TMP/tree/design/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-int-12-no-land-prep.sh"
}
run() {
  local rc=0
  unset OLIVARES_ENT_DIR || true
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-int-12-no-land-prep.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "hub-safe refuse without overlay clone is CLEAN"
else bad "live pin should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/INT-12-NO-LAND-ENT58-2026-08-20.md" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read().replace("int-12-land-as-is: no", "int-12-land-as-is: yes")
open(p, "w", encoding="utf-8").write(t)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (land #58 as-is) is killed"
else bad "land-as-is yes stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/INT-12-NO-LAND-ENT58-2026-08-20.md" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read().replace(
    "allows-additional-active-idp-on-overlay-main: yes",
    "allows-additional-active-idp-on-overlay-main: no",
)
open(p, "w", encoding="utf-8").write(t)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (restore compile premise) is killed"
else bad "missing AllowsAdditionalActiveIdP stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/INT-12-NO-LAND-ENT58-2026-08-20.md" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read().replace(
    "snapshot-on-overlay-main: deliberately-ungated",
    "snapshot-on-overlay-main: gated",
)
open(p, "w", encoding="utf-8").write(t)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (Snapshot gated on overlay main) is killed"
else bad "gated Snapshot stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/design/INT-12-NO-LAND-ENT58-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing measure is COULD NOT LOOK"
else bad "missing measure rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: hub-safe refuse stays CLEAN"
else bad "no-fire should stay CLEAN ($(cat "$TMP/err"))"; fi

echo "check-int-12-no-land-prep selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
