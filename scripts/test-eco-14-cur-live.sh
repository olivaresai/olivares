#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-eco-14-cur-live.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/eco-14.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts" "$TMP/tree/deploy/aws"
  cp "$ROOT/design/eco-14-cur-live.json" "$TMP/tree/design/"
  cp "$ROOT/design/ECO-14-CUR-LIVE-2026-08-20.md" "$TMP/tree/design/"
  cp "$ROOT/design/PRICING-CANON.md" "$TMP/tree/design/"
  cp "$ROOT/deploy/aws/main.tf" "$TMP/tree/deploy/aws/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-eco-14-cur-live.sh"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-eco-14-cur-live.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "live remasurement is CLEAN"
else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/eco-14-cur-live.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["observed"] = 12345
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (invented live USD) is killed"
else bad "invented USD stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/eco-14-cur-live.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["c04_applied"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (C04 applied) is killed"
else bad "c04_applied stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/PRICING-CANON.md" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read().replace(
    "protected_envelope_minor_per_shard_month: 14000",
    "protected_envelope_minor_per_shard_month: 1",
    1,
)
open(p, "w", encoding="utf-8").write(t)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (drop CUR-BASE 14000) is killed"
else bad "dropped 14000 stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/deploy/aws/main.tf" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read().replace("NEVER APPLIED", "APPLIED", 1)
open(p, "w", encoding="utf-8").write(t)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (drop NEVER APPLIED) is killed"
else bad "dropped NEVER APPLIED stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/design/eco-14-cur-live.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing JSON is COULD NOT LOOK"
else bad "missing JSON rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: live remasurement stays CLEAN"
else bad "no-fire should stay CLEAN ($(cat "$TMP/err"))"; fi

echo "check-eco-14-cur-live selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
