#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c04-lb-deletion-protection.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c04-lb-deletion-protection.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c04dp.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/deploy/aws/modules/ingress" \
		"$TMP/tree/deploy/aws/modules/data" "$TMP/tree/design"
	cp "$CHECK" "$TMP/tree/scripts/check-c04-lb-deletion-protection.sh"
	chmod +x "$TMP/tree/scripts/check-c04-lb-deletion-protection.sh"
	cat >"$TMP/tree/deploy/aws/modules/ingress/main.tf" <<'EOF'
resource "aws_lb" "nlb" {
  load_balancer_type         = "network"
  enable_deletion_protection = true
}
resource "aws_lb" "alb" {
  load_balancer_type         = "application"
  enable_deletion_protection = true
}
EOF
	cat >"$TMP/tree/deploy/aws/modules/data/main.tf" <<'EOF'
resource "aws_db_instance" "this" {
  deletion_protection = true
}
EOF
	cat >"$TMP/tree/design/C04-LB-DELETION-PROTECTION-2026-08-19.md" <<'EOF'
NO APLICADO. NLB+ALB deletion protection on.
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c04-lb-deletion-protection.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: both LBs + RDS protected is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i '/resource "aws_lb" "nlb"/,/^}/ { /enable_deletion_protection/d; }' \
	"$TMP/tree/deploy/aws/modules/ingress/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'NLB' "$TMP/err"; then
	ok "firing: missing NLB protection is FAIL"
else
	bad "missing NLB should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i 's/enable_deletion_protection = true/enable_deletion_protection = false/' \
	"$TMP/tree/deploy/aws/modules/ingress/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'disables' "$TMP/err"; then
	ok "firing: explicit false is FAIL"
else
	bad "false should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i 's/deletion_protection = true/deletion_protection = false/' \
	"$TMP/tree/deploy/aws/modules/data/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'RDS' "$TMP/err"; then
	ok "firing: RDS protection lost is FAIL"
else
	bad "RDS false should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'applied the estate' >>"$TMP/tree/design/C04-LB-DELETION-PROTECTION-2026-08-19.md"
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
echo "test-c04-lb-deletion-protection: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
