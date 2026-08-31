#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c05-first-owner.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c05-first-owner.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c05inv.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" \
		"$TMP/tree/cloud/control-plane/internal/billing" \
		"$TMP/tree/cloud/control-plane/internal/engine" \
		"$TMP/tree/cloud/control-plane/internal/tenant" \
		"$TMP/tree/cloud/control-plane/cmd/cloud-cp" \
		"$TMP/tree/commercial/license-worker"
	cp "$CHECK" "$TMP/tree/scripts/check-c05-first-owner.sh"
	chmod +x "$TMP/tree/scripts/check-c05-first-owner.sh"
	cat >"$TMP/tree/cloud/control-plane/internal/billing/dodo-cloud-product-map.json" <<'EOF'
{"products":{"pdt_0NlE7N9AZ9CV7wNAemXAO":{"tier":"cloud-standard-m"},"pdt_0NlE7ZtwL8GfOeYefL7M8":{"tier":"cloud-standard-y"}}}
EOF
	cat >"$TMP/tree/cloud/control-plane/internal/engine/client.go" <<'EOF'
func (c *Client) CreateUser() {}
func (c *Client) GrantMembership() {}
EOF
	cat >"$TMP/tree/cloud/control-plane/internal/tenant/manager.go" <<'EOF'
func (m *Manager) inviteFirstOwner() { CreateUser(); GrantMembership() }
EOF
	cat >"$TMP/tree/cloud/control-plane/cmd/cloud-cp/main.go" <<'EOF'
raw = billing.DecidedDodoCloudProductMap
EOF
	cat >"$TMP/tree/commercial/license-worker/wrangler.jsonc" <<'EOF'
"cloud_products":["pdt_0NlE7N9AZ9CV7wNAemXAO","pdt_0NlE7ZtwL8GfOeYefL7M8"]
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c05-first-owner.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: mapped SKUs + users/memberships is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'c.do(ctx, http.MethodPost, "/v1/onboard", body, &resp)' >>"$TMP/tree/cloud/control-plane/internal/engine/client.go"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q '/v1/onboard' "$TMP/err"; then
	ok "firing: calling /v1/onboard is FAIL"
else
	bad "onboard should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/cloud/control-plane/internal/billing/dodo-cloud-product-map.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p))
del d["products"]["pdt_0NlE7N9AZ9CV7wNAemXAO"]
json.dump(d, open(p,"w"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'monthly' "$TMP/err"; then
	ok "firing: dropping monthly Cloud SKU is FAIL"
else
	bad "lost monthly SKU should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/cloud/control-plane/internal/billing/dodo-cloud-product-map.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing map is LOOK (2)"
else
	bad "missing map should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire after a firing case still CLEAN"
else
	bad "second untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo
echo "test-c05-first-owner: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
