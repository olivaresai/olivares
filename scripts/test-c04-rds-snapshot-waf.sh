#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c04-rds-snapshot-waf.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c04-rds-snapshot-waf.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c04sw.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" \
		"$TMP/tree/deploy/aws/modules/data" \
		"$TMP/tree/deploy/aws/modules/ingress" \
		"$TMP/tree/design"
	cp "$CHECK" "$TMP/tree/scripts/check-c04-rds-snapshot-waf.sh"
	chmod +x "$TMP/tree/scripts/check-c04-rds-snapshot-waf.sh"
	cat >"$TMP/tree/deploy/aws/modules/data/main.tf" <<'EOF'
resource "aws_db_instance" "this" {
  skip_final_snapshot = false
  publicly_accessible = false
  deletion_protection = true
}
EOF
	cat >"$TMP/tree/deploy/aws/modules/ingress/main.tf" <<'EOF'
resource "aws_wafv2_web_acl" "alb" {
  default_action { allow {} }
  rule {
    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesCommonRuleSet"
        vendor_name = "AWS"
      }
    }
  }
}
EOF
	cat >"$TMP/tree/design/C04-RDS-SNAPSHOT-WAF-2026-08-19.md" <<'EOF'
NO APLICADO. RDS keeps a final snapshot. WAF has common rules.
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c04-rds-snapshot-waf.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: snapshot false + WAF rules is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i 's/skip_final_snapshot = false/skip_final_snapshot = true/' \
	"$TMP/tree/deploy/aws/modules/data/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: skip_final_snapshot true is FAIL"
else
	bad "skip_final true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
grep -v publicly_accessible "$TMP/tree/deploy/aws/modules/data/main.tf" \
	>"$TMP/d.tmp" && mv "$TMP/d.tmp" "$TMP/tree/deploy/aws/modules/data/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: missing publicly_accessible false is FAIL"
else
	bad "missing publicly_accessible should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
grep -v managed_rule_group_statement "$TMP/tree/deploy/aws/modules/ingress/main.tf" \
	>"$TMP/i.tmp" && mv "$TMP/i.tmp" "$TMP/tree/deploy/aws/modules/ingress/main.tf"
# also drop the CommonRuleSet name so the second grep cannot pass alone
sed -i '/AWSManagedRulesCommonRuleSet/d' "$TMP/tree/deploy/aws/modules/ingress/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: empty WAF is FAIL"
else
	bad "empty WAF should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'applied the estate' >>"$TMP/tree/design/C04-RDS-SNAPSHOT-WAF-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claiming apply is FAIL"
else
	bad "apply claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/deploy/aws/modules/data/main.tf"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing data module is LOOK (2)"
else
	bad "missing data should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

echo
echo "test-c04-rds-snapshot-waf: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
