#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# export-closure: hub-only cloud/control-plane/README.md — el modulo cloud/ no viaja al export
# export-closure: hub-only cloud/control-plane/docs/RUNBOOK.md — el modulo cloud/ no viaja al export
# export-closure: hub-only cloud/staging/docker-compose.yml — el modulo cloud/ no viaja al export
# Sin el modulo no hay sujeto que comprobar. La respuesta correcta es la TERCERA del
# canon —«no he podido mirar»—, no un verde y no el error crudo con el que muere hoy.
if [ ! -f "$ROOT"/cloud/control-plane/README.md ]; then
	printf '%s\n' "test-c04-04-fly: COULD NOT LOOK — cloud/control-plane/README.md is not in this tree" >&2
	exit 2
fi
if [ ! -f "$ROOT"/cloud/control-plane/docs/RUNBOOK.md ]; then
	printf '%s\n' "test-c04-04-fly: COULD NOT LOOK — cloud/control-plane/docs/RUNBOOK.md is not in this tree" >&2
	exit 2
fi
if [ ! -f "$ROOT"/cloud/staging/docker-compose.yml ]; then
	printf '%s\n' "test-c04-04-fly: COULD NOT LOOK — cloud/staging/docker-compose.yml is not in this tree" >&2
	exit 2
fi
CHECK="$ROOT/scripts/check-c04-04-fly.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c04-04.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/cloud/staging" "$TMP/tree/cloud/control-plane/docs" "$TMP/tree/scripts"
  cp "$ROOT/cloud/staging/docker-compose.yml" "$TMP/tree/cloud/staging/"
  cp "$ROOT/cloud/control-plane/README.md" "$TMP/tree/cloud/control-plane/"
  cp "$ROOT/cloud/control-plane/docs/RUNBOOK.md" "$TMP/tree/cloud/control-plane/docs/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/"*.sh
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c04-04-fly.sh" >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "live tree without fly.toml is CLEAN"; else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
mkdir -p "$TMP/tree/cloud/engine"
printf 'app = "olivares-engine"\n' >"$TMP/tree/cloud/engine/fly.toml"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (restore engine fly.toml) is killed"
else bad "restored fly.toml stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
# Permit-half mutant: drop --dsn so the engine starts like the retired Fly process.
python3 - "$TMP/tree/cloud/staging/docker-compose.yml" <<'PY'
import sys
p = sys.argv[1]
text = open(p, encoding="utf-8").read().replace("      - --dsn\n      - env:ENGINE_DSN\n", "")
open(p, "w", encoding="utf-8").write(text)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (drop --dsn) is killed"
else bad "dropped --dsn stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
printf '\nflyctl deploy --remote-only\n' >>"$TMP/tree/cloud/control-plane/README.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (Fly CLI deploy instruction) is killed"
else bad "Fly CLI instruction stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/cloud/staging/docker-compose.yml"
run
if [ "$(cat "$TMP/rc")" = 2 ] && grep -q 'COULD NOT LOOK' "$TMP/err"; then
  ok "missing compose is COULD NOT LOOK"
else
  bad "missing compose should be exit 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
  ok "no-fire: live checkout stays CLEAN"
else
  bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

printf 'check-c04-04-fly selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
