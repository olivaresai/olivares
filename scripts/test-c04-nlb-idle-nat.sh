#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c04-nlb-idle-nat.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c04-nlb-idle-nat.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c04nlb.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

good_ing() {
	mkdir -p "$TMP/tree/deploy/aws/modules/ingress"
	cat >"$TMP/tree/deploy/aws/modules/ingress/main.tf" <<'EOF'
resource "aws_lb_listener" "collectors" {
  protocol                 = "TCP"
  tcp_idle_timeout_seconds = 6000
}
EOF
}

good_net() {
	mkdir -p "$TMP/tree/deploy/aws/modules/network"
	cat >"$TMP/tree/deploy/aws/modules/network/main.tf" <<'EOF'
resource "aws_eip" "nat" { domain = "vpc" }
resource "aws_nat_gateway" "this" { allocation_id = aws_eip.nat[0].id }
resource "aws_route" "private_nat" { nat_gateway_id = aws_nat_gateway.this[0].id }
EOF
}

good_doc() {
	mkdir -p "$TMP/tree/design"
	cat >"$TMP/tree/design/C04-NLB-IDLE-NAT-2026-08-19.md" <<'EOF'
NO APLICADO. TCP idle timeout owned. NAT obligatory.
EOF
}

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts"
	cp "$CHECK" "$TMP/tree/scripts/check-c04-nlb-idle-nat.sh"
	chmod +x "$TMP/tree/scripts/check-c04-nlb-idle-nat.sh"
	good_ing
	good_net
	good_doc
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-c04-nlb-idle-nat.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: timeout 6000 + NAT is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
grep -v tcp_idle_timeout_seconds "$TMP/tree/deploy/aws/modules/ingress/main.tf" \
	>"$TMP/tree/deploy/aws/modules/ingress/main.tf.tmp"
mv "$TMP/tree/deploy/aws/modules/ingress/main.tf.tmp" "$TMP/tree/deploy/aws/modules/ingress/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'tcp_idle_timeout_seconds' "$TMP/err"; then
	ok "firing: missing idle timeout is FAIL"
else
	bad "missing timeout should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i 's/6000/350/' "$TMP/tree/deploy/aws/modules/ingress/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q '350' "$TMP/err"; then
	ok "firing: 350 s TLS ceiling is FAIL"
else
	bad "350 s should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
grep -v aws_nat_gateway "$TMP/tree/deploy/aws/modules/network/main.tf" \
	>"$TMP/tree/deploy/aws/modules/network/main.tf.tmp"
mv "$TMP/tree/deploy/aws/modules/network/main.tf.tmp" "$TMP/tree/deploy/aws/modules/network/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'aws_nat_gateway' "$TMP/err"; then
	ok "firing: dropping NAT is FAIL"
else
	bad "lost NAT should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'applied the estate' >>"$TMP/tree/design/C04-NLB-IDLE-NAT-2026-08-19.md"
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
echo "test-c04-nlb-idle-nat: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
