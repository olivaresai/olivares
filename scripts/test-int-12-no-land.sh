#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# La bateria copia an internal design note (not shipped) a su arbol de fixtures, y el
# export lo cura fuera: en el arbol publico moria con `cp: cannot stat`, rc 1. Misma cura y
# mismo clasificador que la pata (check-int-12-no-land.sh), por la misma razon.
if [ ! -r "$ROOT/design/INT-12-NO-LAND-ENT58-2026-08-19.md" ] \
   && [ "$(bash "$ROOT/scripts/hub-leg.sh" --classify --root "$ROOT" 2>/dev/null)" = "public" ]; then
  printf '%s\n' "test-int-12-no-land: SCOPED — public export; the INT-12 acta is curated out."
  exit 0
fi
CHECK="$ROOT/scripts/check-int-12-no-land.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/int-12.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts"
  cp "$ROOT/design/INT-12-NO-LAND-ENT58-2026-08-19.md" "$TMP/tree/design/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-int-12-no-land.sh"
}

run() {
  local rc=0
  if [ -n "${OLIVARES_ENT_DIR_OVERRIDE+x}" ]; then
    export OLIVARES_ENT_DIR="$OLIVARES_ENT_DIR_OVERRIDE"
  else
    unset OLIVARES_ENT_DIR || true
  fi
  OLIVARES_ROOT="$TMP/tree" OLIVARES_HUB_GIT_DIR="$ROOT" \
    bash "$TMP/tree/scripts/check-int-12-no-land.sh" >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
  return 0
}

stage
unset OLIVARES_ENT_DIR_OVERRIDE || true
run
if [ "$(cat "$TMP/rc")" = "0" ]; then ok "document honesty without overlay is CLEAN"
else bad "honesty without overlay should be CLEAN ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/design/INT-12-NO-LAND-ENT58-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = "2" ] && grep -q 'COULD NOT LOOK' "$TMP/err"; then
  ok "missing measure is COULD NOT LOOK"
else bad "missing measure should be exit 2 ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/INT-12-NO-LAND-ENT58-2026-08-19.md" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read().replace("int-12-land-as-is: no", "int-12-land-as-is: yes")
open(p, "w", encoding="utf-8").write(t)
PY
run
if [ "$(cat "$TMP/rc")" = "1" ]; then ok "mutant (land #58 as-is) is killed"
else bad "land-as-is yes stayed CLEAN rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/INT-12-NO-LAND-ENT58-2026-08-19.md" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read().replace(
    "allows-additional-active-idp-on-overlay-main: yes",
    "allows-additional-active-idp-on-overlay-main: no",
)
open(p, "w", encoding="utf-8").write(t)
PY
run
if [ "$(cat "$TMP/rc")" = "1" ]; then ok "mutant (restore compile premise) is killed"
else bad "missing AllowsAdditionalActiveIdP stayed CLEAN rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/INT-12-NO-LAND-ENT58-2026-08-19.md" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read()
# Present the #58 pin as if it were hub main (no regression).
hub = None
for ln in t.splitlines():
    if ln.startswith("hub-main-sha:"):
        hub = ln.split(":",1)[1].strip()
t = t.replace("int-12-pin: 76221568428d8e4c882731d8660b787b63ea9826", f"int-12-pin: {hub}")
open(p, "w", encoding="utf-8").write(t)
PY
run
if [ "$(cat "$TMP/rc")" = "1" ]; then ok "mutant (claim #58 pin is hub main) is killed"
else bad "current-pin claim stayed CLEAN rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/INT-12-NO-LAND-ENT58-2026-08-19.md" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read().replace(
    "snapshot-on-overlay-main: deliberately-ungated",
    "snapshot-on-overlay-main: gated",
)
open(p, "w", encoding="utf-8").write(t)
PY
run
if [ "$(cat "$TMP/rc")" = "1" ]; then ok "mutant (Snapshot already gated on overlay main) is killed"
else bad "Snapshot gated stayed CLEAN rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

if OLIVARES_ROOT="$ROOT" OLIVARES_HUB_GIT_DIR="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
  ok "no-fire: live INT-12 stays CLEAN"
else
  bad "no-fire live INT-12 went RED ($(cat "$TMP/err"))"
fi

printf 'check-int-12-no-land selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
