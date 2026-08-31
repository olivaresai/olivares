#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c13-07-ar-holds-prep.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c1307.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts"
  cp "$ROOT/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" "$TMP/tree/design/"
  cp "$ROOT/design/C13-07-AR-HOLDS-PREP-2026-08-20.md" "$TMP/tree/design/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c13-07-ar-holds-prep.sh"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c13-07-ar-holds-prep.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "AR-1..AR-4 HOLD rows are CLEAN"
else bad "live pin should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" <<'PY'
import re, sys
p = sys.argv[1]
text = re.sub(r"^\| AR-2 \|.*\n", "", open(p, encoding="utf-8").read(), flags=re.M)
open(p, "w", encoding="utf-8").write(text)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (drop AR-2 row) is killed"
else bad "dropped AR-2 stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
sed -i 's/Does not construct the five/We construct the five/' \
  "$TMP/tree/design/C13-07-AR-HOLDS-PREP-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (doc claims construct) is killed"
else bad "construct claim stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing HOLD is COULD NOT LOOK"
else bad "missing HOLD rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: live pin stays CLEAN"
else bad "no-fire should stay CLEAN ($(cat "$TMP/err"))"; fi

echo "check-c13-07-ar-holds-prep selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
