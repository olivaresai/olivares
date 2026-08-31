#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c04-alb-drop-invalid-headers.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c04-alb-drop-invalid-headers.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c04hdr.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/deploy/aws/modules/ingress" "$TMP/tree/design"
	cp "$CHECK" "$TMP/tree/scripts/check-c04-alb-drop-invalid-headers.sh"
	chmod +x "$TMP/tree/scripts/check-c04-alb-drop-invalid-headers.sh"
	cat >"$TMP/tree/deploy/aws/modules/ingress/main.tf" <<'EOF'
resource "aws_lb" "nlb" {
  load_balancer_type = "network"
}
resource "aws_lb" "alb" {
  load_balancer_type         = "application"
  drop_invalid_header_fields = true
}
EOF
	cat >"$TMP/tree/design/C04-ALB-DROP-INVALID-HEADERS-2026-08-19.md" <<'EOF'
NO APLICADO. ALB drops invalid headers.
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c04-alb-drop-invalid-headers.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: ALB true, NLB silent is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i '/drop_invalid_header_fields/d' "$TMP/tree/deploy/aws/modules/ingress/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'ALB' "$TMP/err"; then
	ok "firing: missing ALB flag is FAIL"
else
	bad "missing flag should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i 's/drop_invalid_header_fields = true/drop_invalid_header_fields = false/' \
	"$TMP/tree/deploy/aws/modules/ingress/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'false' "$TMP/err"; then
	ok "firing: explicit false is FAIL"
else
	bad "false should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i '/resource "aws_lb" "nlb"/,/^}/ s/^}/  drop_invalid_header_fields = true\n}/' \
	"$TMP/tree/deploy/aws/modules/ingress/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'NLB' "$TMP/err"; then
	ok "firing: flag on NLB is FAIL"
else
	bad "NLB flag should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'applied the estate' >>"$TMP/tree/design/C04-ALB-DROP-INVALID-HEADERS-2026-08-19.md"
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
echo "test-c04-alb-drop-invalid-headers: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
