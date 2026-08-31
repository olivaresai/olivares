#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-cfg-01-production-hold.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-cfg-01-production-hold.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/cfg01.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/commercial/license-worker"
	cp "$CHECK" "$TMP/tree/scripts/check-cfg-01-production-hold.sh"
	chmod +x "$TMP/tree/scripts/check-cfg-01-production-hold.sh"
	cp "$ROOT/design/cfg-01-production-hold.json" "$TMP/tree/design/"
	cat >"$TMP/tree/design/CFG-01-PRODUCTION-HOLD-2026-08-19.md" <<'EOF'
NOT PROVISIONED. Section 9 not met. Production stays off.
EOF
	cat >"$TMP/tree/commercial/license-worker/wrangler.jsonc" <<'EOF'
{
  "vars": {
    "FULFILLMENT_ENABLED": "false"
  },
  "env": {
    "sandbox": {
      "FULFILLMENT_ENABLED": "true"
    },
    "production": {
      "FULFILLMENT_ENABLED": "false"
    }
  }
}
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-cfg-01-production-hold.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: production HOLD is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/cfg-01-production-hold.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["production_provisioned"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: production_provisioned true is FAIL"
else
	bad "provisioned true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/cfg-01-production-hold.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["sandbox_complete"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: sandbox_complete true is FAIL"
else
	bad "sandbox_complete true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'production deployed' >>"$TMP/tree/design/CFG-01-PRODUCTION-HOLD-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claiming production deployed is FAIL"
else
	bad "deploy claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/commercial/license-worker/wrangler.jsonc" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
p.write_text(p.read_text().replace(
    '"production": {\n      "FULFILLMENT_ENABLED": "false"',
    '"production": {\n      "FULFILLMENT_ENABLED": "true"',
    1,
))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: production fulfillment true is FAIL"
else
	bad "production true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/cfg-01-production-hold.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing JSON is LOOK (2)"
else
	bad "missing JSON should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

echo
echo "test-cfg-01-production-hold: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
