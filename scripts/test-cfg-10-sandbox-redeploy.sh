#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-cfg-10-sandbox-redeploy.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-cfg-10-sandbox-redeploy.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/cfg10.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" "$TMP/tree/commercial/license-worker"
	cp "$CHECK" "$TMP/tree/scripts/check-cfg-10-sandbox-redeploy.sh"
	chmod +x "$TMP/tree/scripts/check-cfg-10-sandbox-redeploy.sh"
	cat >"$TMP/tree/design/cfg-10-sandbox-redeploy.json" <<'EOF'
{
  "lote": "CFG-10",
  "deployed": false,
  "production_targeted": false,
  "live_deploy": "auth-10000",
  "fulfillment_sandbox": true,
  "fulfillment_production": false,
  "buy_to_bytes": "cannot-look"
}
EOF
	cat >"$TMP/tree/design/CFG-10-SANDBOX-REDEPLOY-2026-08-19.md" <<'EOF'
NOT DEPLOYED. Token 10000 on /memberships. Production not targeted.
EOF
	cat >"$TMP/tree/commercial/license-worker/wrangler.jsonc" <<'EOF'
{
  "vars": { "FULFILLMENT_ENABLED": "false" },
  "env": {
    "sandbox": {
      "vars": { "FULFILLMENT_ENABLED": "true" }
    },
    "production": {
      "vars": { "FULFILLMENT_ENABLED": "false" }
    }
  }
}
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-cfg-10-sandbox-redeploy.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: not deployed, sandbox on, production off is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/cfg-10-sandbox-redeploy.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["deployed"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: deployed true is FAIL"
else
	bad "deployed true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/cfg-10-sandbox-redeploy.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["buy_to_bytes"] = "green"
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: fake buy-to-bytes is FAIL"
else
	bad "buy-to-bytes green should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'live deploy succeeded' >>"$TMP/tree/design/CFG-10-SANDBOX-REDEPLOY-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claiming live deploy is FAIL"
else
	bad "doc live deploy should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/commercial/license-worker/wrangler.jsonc"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing wrangler is LOOK (2)"
else
	bad "missing wrangler should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire after a firing case still CLEAN"
else
	bad "second untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo
echo "test-cfg-10-sandbox-redeploy: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
