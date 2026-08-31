#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-eco-13-scale-gates.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-eco-13-scale-gates.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/eco13.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

good_json() {
	cat >"$TMP/tree/design/eco-13-scale-gates.json" <<'EOF'
{
  "lote": "ECO-13",
  "implemented": false,
  "sales_lane_opened": false,
  "u_f": "UNKNOWN",
  "u_d": "UNKNOWN",
  "gates": [
    {"id":"SCALE-REPORTING","evidence":"tenant-scoped-summary-plus-cross-tenant-race-test","status":"HOLD"},
    {"id":"SCALE-PQCPOSTURE","evidence":"reachable-product-surface-plus-e2e","status":"HOLD"},
    {"id":"SCALE-ONBOARDING","evidence":"reachable-product-surface-plus-e2e","status":"HOLD"},
    {"id":"SCALE-COMPUTER-USE-GATE","evidence":"pinned-tool-taxonomy-deny-unknown-and-drift-fixtures","status":"HOLD"},
    {"id":"SCALE-RENDER-INSPECTOR","evidence":"calibrated-fp-fn-corpus-fail-semantics-and-load","status":"HOLD"},
    {"id":"SCALE-CREDENTIAL-MINTER","evidence":"tenant-subject-audience-scoped-cache-and-isolation-test","status":"HOLD"},
    {"id":"SCALE-LOGIN-ENFORCEMENT","evidence":"per-tenant-posture-and-cross-tenant-login-tests","status":"HOLD"}
  ]
}
EOF
}

good_hold() {
	cat >"$TMP/tree/design/HOLD-SCALE-GATES-2026-08-19.md" <<'EOF'
HOLD. NO ABIERTO. NO IMPLEMENTADO.
| SCALE-REPORTING | x |
| SCALE-PQCPOSTURE | x |
| SCALE-ONBOARDING | x |
| SCALE-COMPUTER-USE-GATE | x |
| SCALE-RENDER-INSPECTOR | x |
| SCALE-CREDENTIAL-MINTER | x |
| SCALE-LOGIN-ENFORCEMENT | x |
EOF
}

good_canon() {
	cat >"$TMP/tree/design/PRICING-CANON.md" <<'EOF'
    SCALE-REPORTING:        { evidence: tenant-scoped-summary-plus-cross-tenant-race-test }
    SCALE-PQCPOSTURE:       { evidence: reachable-product-surface-plus-e2e }
    SCALE-ONBOARDING:       { evidence: reachable-product-surface-plus-e2e }
    SCALE-COMPUTER-USE-GATE: { evidence: pinned-tool-taxonomy-deny-unknown-and-drift-fixtures }
    SCALE-RENDER-INSPECTOR: { evidence: calibrated-fp-fn-corpus-fail-semantics-and-load }
    SCALE-CREDENTIAL-MINTER: { evidence: tenant-subject-audience-scoped-cache-and-isolation-test }
    SCALE-LOGIN-ENFORCEMENT: { evidence: per-tenant-posture-and-cross-tenant-login-tests }
    SCALE-ALL-HOLD-GATES:
      requires: [SCALE-REPORTING]
  cloud-scale-monthly:
    state: closed
EOF
}

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/design" "$TMP/tree/scripts"
	cp "$CHECK" "$TMP/tree/scripts/check-eco-13-scale-gates.sh"
	chmod +x "$TMP/tree/scripts/check-eco-13-scale-gates.sh"
	good_json
	good_hold
	good_canon
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-eco-13-scale-gates.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: seven HOLDs, lane closed is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-13-scale-gates.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["sales_lane_opened"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'JSON/canon/HOLD failed' "$TMP/err"; then
	ok "firing: sales_lane_opened true is FAIL"
else
	bad "opened lane should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i 's/state: closed/state: open/' "$TMP/tree/design/PRICING-CANON.md"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'JSON/canon/HOLD failed' "$TMP/err"; then
	ok "firing: opening cloud-scale-monthly in the canon is FAIL"
else
	bad "canon lane open should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-13-scale-gates.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["gates"] = d["gates"][:6]
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'JSON/canon/HOLD failed' "$TMP/err"; then
	ok "firing: dropping a SCALE gate is FAIL"
else
	bad "six gates should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'abrimos cloud-scale' >>"$TMP/tree/design/HOLD-SCALE-GATES-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'claims an opening' "$TMP/err"; then
	ok "firing: HOLD claiming an opening is FAIL"
else
	bad "opening claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/HOLD-SCALE-GATES-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing HOLD doc is LOOK (2)"
else
	bad "missing HOLD should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire after a firing case still CLEAN"
else
	bad "second untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo
echo "test-eco-13-scale-gates: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
