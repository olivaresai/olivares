#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c05-19-deployment-pop-prep.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0519prep.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts" \
    "$TMP/tree/commercial/license-worker/src" \
    "$TMP/tree/commercial/license-worker/migrations"
  cp "$ROOT/design/c05-19-deployment-pop-prep-2026-08-20.json" "$TMP/tree/design/"
  cp "$ROOT/design/C05-19-DEPLOYMENT-POP-PREP-2026-08-20.md" "$TMP/tree/design/"
  cp "$ROOT/commercial/license-worker/src/index.ts" \
    "$TMP/tree/commercial/license-worker/src/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c05-19-deployment-pop-prep.sh"
}
run() {
  local rc=0
  unset OLIVARES_ENT_DIR || true
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c05-19-deployment-pop-prep.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "hub-safe deployment-pop pin is CLEAN"
else bad "live pin should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/c05-19-deployment-pop-prep-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["remainder_applied"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (remainder-applied) is killed"
else bad "remainder-applied stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
mkdir -p "$TMP/tree/commercial/license-worker/src/connect"
printf 'export {}\n' > "$TMP/tree/commercial/license-worker/src/connect/pop.ts"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (pop.ts landed) is killed"
else bad "pop.ts stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
printf 'SELECT 1;\n' > \
  "$TMP/tree/commercial/license-worker/migrations/0030_deployment_pop.sql"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (0030 landed) is killed"
else bad "0030 stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/c05-19-deployment-pop-prep-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["overlay_remeasured_in_this_gate"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (overlay remasure leaked) is killed"
else bad "overlay remasure stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/design/c05-19-deployment-pop-prep-2026-08-20.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing JSON is COULD NOT LOOK"
else bad "missing JSON rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: live pin stays CLEAN"
else bad "no-fire should stay CLEAN ($(cat "$TMP/err"))"; fi

echo "check-c05-19-deployment-pop-prep selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
