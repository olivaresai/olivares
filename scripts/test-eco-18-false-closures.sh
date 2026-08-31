#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-eco-18-false-closures.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/eco-18.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts"
  cp "$ROOT/design/eco-18-false-closures.json" "$TMP/tree/design/"
  cp "$ROOT/design/REGISTRO-DECISIONES-2026-08-01.md" "$TMP/tree/design/"
  cp "$ROOT/design/ECO-18-FALSOS-CIERRES-2026-08-19.md" "$TMP/tree/design/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/"*.sh
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-eco-18-false-closures.sh" >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "live remasurement is CLEAN"; else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/REGISTRO-DECISIONES-2026-08-01.md" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read().replace("YA-46 **REABIERTA", "YA-46 **CERRADA")
open(p, "w", encoding="utf-8").write(t)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (drop YA-46 REABIERTA) is killed"
else bad "dropped REABIERTA stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/eco-18-false-closures.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["rows"] = [r for r in d["rows"] if r["id"] != "YA-46"]
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (drop YA-46 from JSON) is killed"
else bad "dropped YA-46 JSON stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/eco-18-false-closures.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["u_f"] = 1
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (numeric U_f) is killed"
else bad "numeric U_f stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/eco-18-false-closures.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
for r in d["rows"]:
    if r["id"] == "YA-23":
        r["status"] = "YA-RESPONDIDA"
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (re-close YA-23) is killed"
else bad "re-closed YA-23 stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/ECO-18-FALSOS-CIERRES-2026-08-19.md" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read().replace("Not a decision", "Decision taken")
open(p, "w", encoding="utf-8").write(t)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (claim a decision) is killed"
else bad "decision claim stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/design/eco-18-false-closures.json"
run
if [ "$(cat "$TMP/rc")" = 2 ] && grep -q 'COULD NOT LOOK' "$TMP/err"; then
  ok "missing JSON is COULD NOT LOOK"
else
  bad "missing JSON should be exit 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
  ok "no-fire: live checkout stays CLEAN"
else
  bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

printf 'check-eco-18-false-closures selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
