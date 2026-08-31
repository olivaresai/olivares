#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail

# Aislamiento de git: este guion empareja `mktemp -d` con `git`, y sin sanear el
# entorno un GIT_DIR envenenado lo apunta al repo real. Fallar cerrado: no poder
# sanear es «no he podido aislar», nunca «no hacia falta aislar».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-0022-published-schema.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/0022-schema.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" \
           "$TMP/tree/commercial/license-worker/migrations" \
           "$TMP/tree/commercial/license-worker/src/store" \
           "$TMP/tree/scripts"
  cp "$CHECK" "$TMP/tree/scripts/check-0022-published-schema.sh"
  chmod +x "$TMP/tree/scripts/check-0022-published-schema.sh"
  cp "$ROOT/design/ADJUDICACION-0022-SALIDA-1-2026-08-18.md" "$TMP/tree/design/"
  cp "$ROOT/commercial/license-worker/src/store/db.ts" "$TMP/tree/commercial/license-worker/src/store/"
  cp -a "$ROOT/commercial/license-worker/migrations/." "$TMP/tree/commercial/license-worker/migrations/"
}

run() {
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-0022-published-schema.sh" >/dev/null 2>"$TMP/err"
}

stage
if run; then
  ok "live tree is CLEAN (no-fire)"
else
  bad "live tree should be CLEAN ($(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/ADJUDICACION-0022-SALIDA-1-2026-08-18.md"
if run; then
  bad "missing verdict stayed CLEAN"
else
  ok "missing verdict is a finding (mutant: drop the written Exit 1)"
fi

stage
python3 - "$TMP/tree/design/ADJUDICACION-0022-SALIDA-1-2026-08-18.md" <<'PY'
import sys
p=sys.argv[1]
t=open(p,encoding="utf-8").read().replace("0022-exit: 1", "0022-exit: 2")
open(p,"w",encoding="utf-8").write(t)
PY
if run; then
  bad "Exit 2 marker stayed CLEAN"
else
  ok "mutant (claim Exit 2) is killed"
fi

stage
printf '%s\n' 'CREATE TABLE dodo_line_grants (id TEXT);' \
  > "$TMP/tree/commercial/license-worker/migrations/0022_flight_wins.sql"
if run; then
  bad "0022 CREATE dodo_line_grants stayed CLEAN"
else
  ok "0022 CREATE of the published table is a finding"
fi

stage
printf '%s\n' 'CREATE TABLE IF NOT EXISTS dodo_cohort_fragments (id TEXT);' \
  > "$TMP/tree/commercial/license-worker/migrations/0022_if_not_exists.sql"
if run; then
  bad "0022 IF NOT EXISTS fragments stayed CLEAN"
else
  ok "0022 IF NOT EXISTS of fragments is a finding (the silent bomb)"
fi

stage
rm -rf "$TMP/tree/commercial/license-worker/migrations"
if run; then
  bad "missing migrations stayed CLEAN"
else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then
    ok "missing migrations is COULD NOT LOOK"
  else
    bad "missing migrations should be exit 2 ($(cat "$TMP/err"))"
  fi
fi

stage
# Flight-only column on the live INSERT (Exit 2 by stealth).
sed -i 's/kind, quantity/kind, quantity, billing_period/' \
  "$TMP/tree/commercial/license-worker/src/store/db.ts"
if run; then
  bad "flight-only INSERT column stayed CLEAN"
else
  ok "flight-only INSERT column is a finding (mutant: Exit 2 without saying so)"
fi

# The shipped check must not take ROOT from the caller's git tree.
# A foreign git repo with no verdict used to make this report FAIL.
git -C "$TMP" init -q other-cwd
if (cd "$TMP/other-cwd" && env -u OLIVARES_ROOT bash "$CHECK" >/dev/null 2>"$TMP/err-cwd"); then
  ok "no-fire: live check from another git tree stays CLEAN"
else
  bad "live check is cwd-dependent ($(cat "$TMP/err-cwd"))"
fi

printf 'check-0022 selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
