#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c04-vpc-flow-logs.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c04-vpc-flow-logs.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c04flow.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" \
		"$TMP/tree/deploy/aws/modules/observability" \
		"$TMP/tree/design"
	cp "$CHECK" "$TMP/tree/scripts/check-c04-vpc-flow-logs.sh"
	chmod +x "$TMP/tree/scripts/check-c04-vpc-flow-logs.sh"
	cat >"$TMP/tree/deploy/aws/modules/observability/main.tf" <<'EOF'
resource "aws_flow_log" "vpc" {
  vpc_id       = var.vpc_id
  traffic_type = "ALL"
}
EOF
	cat >"$TMP/tree/deploy/aws/main.tf" <<'EOF'
module "observability" {
  source = "./modules/observability"
  vpc_id = module.network.vpc_id
}
EOF
	cat >"$TMP/tree/design/C04-VPC-FLOW-LOGS-2026-08-19.md" <<'EOF'
NO APLICADO. VPC flow logs ALL.
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c04-vpc-flow-logs.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: flow logs ALL is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
grep -v aws_flow_log "$TMP/tree/deploy/aws/modules/observability/main.tf" \
	>"$TMP/o.tmp" && mv "$TMP/o.tmp" "$TMP/tree/deploy/aws/modules/observability/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: missing aws_flow_log is FAIL"
else
	bad "missing flow log should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i 's/"ALL"/"ACCEPT"/' "$TMP/tree/deploy/aws/modules/observability/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: ACCEPT-only is FAIL"
else
	bad "ACCEPT-only should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i 's/module.network.vpc_id/""/' "$TMP/tree/deploy/aws/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: unwired vpc_id is FAIL"
else
	bad "unwired vpc should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'applied the estate' >>"$TMP/tree/design/C04-VPC-FLOW-LOGS-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claiming apply is FAIL"
else
	bad "apply claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/deploy/aws/modules/observability/main.tf"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing observability is LOOK (2)"
else
	bad "missing obs should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

echo
echo "test-c04-vpc-flow-logs: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
