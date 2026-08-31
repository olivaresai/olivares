#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-eco-15-sandbox-experiments.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-eco-15-sandbox-experiments.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/eco15.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

good_json() {
	cat >"$TMP/tree/design/eco-15-sandbox-experiments.json" <<'EOF'
{
  "lote": "ECO-15",
  "implemented": false,
  "ran": false,
  "sales_lane_opened": false,
  "u_f": "UNKNOWN",
  "u_d": "UNKNOWN",
  "experiments": [
    {"id":"annual-renewal-price-vintage-sandbox","evidence":"exact-sandbox-annual-renewal-honors-price-vintage","status":"HOLD","capture":null},
    {"id":"exact-quantity-billing-sandbox","evidence":"exact-sandbox-quantity-2-billing-capture","status":"HOLD","capture":null}
  ]
}
EOF
}

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" "$TMP/tree/commercial/dodo-sandbox/evidence"
	cp "$CHECK" "$TMP/tree/scripts/check-eco-15-sandbox-experiments.sh"
	chmod +x "$TMP/tree/scripts/check-eco-15-sandbox-experiments.sh"
	good_json
	cat >"$TMP/tree/design/ECO-15-SANDBOX-EXPERIMENTS-2026-08-19.md" <<'EOF'
NOT RUN. Two HOLDs. Lanes closed.
EOF
	cat >"$TMP/tree/design/PRICING-CANON.md" <<'EOF'
  self-hosted-annual:
    state: closed
  cloud-scale-monthly:
    state: closed
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-eco-15-sandbox-experiments.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: HOLDs, lanes closed, not run is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-15-sandbox-experiments.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["ran"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: ran true is FAIL"
else
	bad "ran true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i 's/state: closed/state: open/' "$TMP/tree/design/PRICING-CANON.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: opening a lane in the canon is FAIL"
else
	bad "open lane should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-15-sandbox-experiments.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["u_f"] = "12.3"
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: filling U_f is FAIL"
else
	bad "U_f fill should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
touch "$TMP/tree/commercial/dodo-sandbox/evidence/exact-sandbox-annual-renewal-honors-price-vintage.json"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: capture present while HOLD is FAIL"
else
	bad "stale capture should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'capture written' >>"$TMP/tree/design/ECO-15-SANDBOX-EXPERIMENTS-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claiming a run is FAIL"
else
	bad "run claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/PRICING-CANON.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing canon is LOOK (2)"
else
	bad "missing canon should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo
echo "test-eco-15-sandbox-experiments: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
