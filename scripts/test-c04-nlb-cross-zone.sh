#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c04-nlb-cross-zone.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c04-nlb-cross-zone.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c04xz.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/deploy/aws/modules/ingress" "$TMP/tree/design"
	cp "$CHECK" "$TMP/tree/scripts/check-c04-nlb-cross-zone.sh"
	chmod +x "$TMP/tree/scripts/check-c04-nlb-cross-zone.sh"
	cat >"$TMP/tree/deploy/aws/modules/ingress/main.tf" <<'EOF'
resource "aws_lb" "nlb" {
  load_balancer_type               = "network"
  enable_cross_zone_load_balancing = true
}
EOF
	cat >"$TMP/tree/deploy/aws/variables.tf" <<'EOF'
variable "desired_count" {
  default = 2
}
EOF
	cat >"$TMP/tree/design/C04-NLB-CROSS-ZONE-2026-08-19.md" <<'EOF'
NO APLICADO. NLB cross-zone on. desired_count 2.
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c04-nlb-cross-zone.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: cross-zone true + desired_count 2 is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
grep -v enable_cross_zone_load_balancing "$TMP/tree/deploy/aws/modules/ingress/main.tf" \
	>"$TMP/ing.tmp" && mv "$TMP/ing.tmp" "$TMP/tree/deploy/aws/modules/ingress/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'cross-zone' "$TMP/err"; then
	ok "firing: missing cross-zone is FAIL"
else
	bad "missing cross-zone should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i 's/true/false/' "$TMP/tree/deploy/aws/modules/ingress/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'false' "$TMP/err"; then
	ok "firing: cross-zone false is FAIL"
else
	bad "false should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i 's/default = 2/default = 1/' "$TMP/tree/deploy/aws/variables.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'desired_count' "$TMP/err"; then
	ok "firing: desired_count 1 is FAIL"
else
	bad "desired_count 1 should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'applied the estate' >>"$TMP/tree/design/C04-NLB-CROSS-ZONE-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'claims an apply' "$TMP/err"; then
	ok "firing: doc claiming apply is FAIL"
else
	bad "apply claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/deploy/aws/modules/ingress/main.tf"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing ingress is LOOK (2)"
else
	bad "missing ingress should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire after a firing case still CLEAN"
else
	bad "second untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo
echo "test-c04-nlb-cross-zone: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
