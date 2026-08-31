#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-ver-10-og.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/ver-10.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts"
  cp "$ROOT/design/VER-10-OG-WEB-2026-08-19.md" "$TMP/tree/design/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-ver-10-og.sh"
}

run() {
  local rc=0
  # Unset OLIVARES_WEB_DIR unless the case plants a tree. An explicit
  # hanging pointer is exit 2 (same rule as check-hub-web-fidelity).
  if [ -n "${OLIVARES_WEB_DIR_OVERRIDE+x}" ]; then
    export OLIVARES_WEB_DIR="$OLIVARES_WEB_DIR_OVERRIDE"
  else
    unset OLIVARES_WEB_DIR || true
  fi
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-ver-10-og.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
  return 0
}

stage
unset OLIVARES_WEB_DIR_OVERRIDE || true
run
if [ "$(cat "$TMP/rc")" = "0" ]; then ok "document honesty without web tree is CLEAN"
else bad "honesty without web should be CLEAN ($(cat "$TMP/err"))"; fi

stage
unset OLIVARES_WEB_DIR_OVERRIDE || true
rm -f "$TMP/tree/design/VER-10-OG-WEB-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = "2" ] && grep -q 'COULD NOT LOOK' "$TMP/err"; then
  ok "missing measure is COULD NOT LOOK"
else bad "missing measure should be exit 2 ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/VER-10-OG-WEB-2026-08-19.md" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read().replace("measured-locales: 13", "measured-locales: 14")
open(p, "w", encoding="utf-8").write(t)
PY
unset OLIVARES_WEB_DIR_OVERRIDE || true
run
if [ "$(cat "$TMP/rc")" = "1" ]; then ok "mutant (claim 14 locales) is killed"
else bad "14 locales stayed CLEAN rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/VER-10-OG-WEB-2026-08-19.md" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read().replace("ver-10-looked: yes", "ver-10-looked: no")
open(p, "w", encoding="utf-8").write(t)
PY
unset OLIVARES_WEB_DIR_OVERRIDE || true
run
if [ "$(cat "$TMP/rc")" = "1" ]; then ok "mutant (claim cannot-look) is killed"
else bad "cannot-look stayed CLEAN rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/VER-10-OG-WEB-2026-08-19.md" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read()
t = t.replace("measured-og-png: 137", "measured-og-png: 137\nmeasured-og-per-page: 14")
open(p, "w", encoding="utf-8").write(t)
PY
unset OLIVARES_WEB_DIR_OVERRIDE || true
run
if [ "$(cat "$TMP/rc")" = "1" ]; then ok "mutant (claim 14 OG per page) is killed"
else bad "14 OG/page stayed CLEAN rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# Planted web tree with 14 locales must go RED when the pointer is explicit.
stage
python3 - "$TMP" <<'PY'
import os, subprocess, sys
root = sys.argv[1]
web = os.path.join(root, "web14")
os.makedirs(os.path.join(web, "src"), exist_ok=True)
os.makedirs(os.path.join(web, "public", "og"), exist_ok=True)
open(os.path.join(web, "src", "consts.ts"), "w", encoding="utf-8").write(
    "export const SITE = {\n"
    "  locales: ['en', 'es', 'fr', 'de', 'it', 'pt', 'nl', 'ja', 'ko', 'ru', 'uk', 'zh', 'pl', 'xx'] as const,\n"
    "} as const;\n"
)
open(os.path.join(web, "public", "og", "og-home.png"), "wb").write(b"x")
subprocess.check_call(["git", "init", "-q"], cwd=web)
subprocess.check_call(["git", "add", "-A"], cwd=web)
subprocess.check_call(
    ["git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "14 locales"],
    cwd=web,
)
open(os.path.join(root, "web14.path"), "w").write(web)
PY
OLIVARES_WEB_DIR_OVERRIDE="$(cat "$TMP/web14.path")"
run
if [ "$(cat "$TMP/rc")" = "1" ]; then ok "mutant (live 14 locales) is killed"
else bad "live 14 locales stayed CLEAN rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# No-fire: live tree (this checkout) stays CLEAN. Drop the planted WEB_DIR
# from the 14-locale mutant — export leaks into this invocation otherwise.
unset OLIVARES_WEB_DIR OLIVARES_WEB_DIR_OVERRIDE || true
if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
  ok "no-fire: live VER-10 stays CLEAN"
else
  bad "no-fire live VER-10 went RED ($(cat "$TMP/err"))"
fi

printf 'check-ver-10-og selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
