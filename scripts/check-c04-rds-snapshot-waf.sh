#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c04-rds-snapshot-waf.sh — C04. RDS keeps a final snapshot.
# WAF is not an empty allow. Unapplied.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-rds-snapshot-waf: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-rds-snapshot-waf: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

DATA="${OLIVARES_C04SW_DATA:-deploy/aws/modules/data/main.tf}"
ING="${OLIVARES_C04SW_INGRESS:-deploy/aws/modules/ingress/main.tf}"
DOC="${OLIVARES_C04SW_DOC:-design/C04-RDS-SNAPSHOT-WAF-2026-08-19.md}"

[ -f "$DATA" ] || cannot "missing $DATA"
[ -f "$ING" ] || cannot "missing $ING"
[ -f "$DOC" ] || cannot "missing $DOC"

grep -q 'NO APLICADO' "$DOC" || fail "$DOC lost NO APLICADO"
if grep -qiE 'tofu apply ran|applied the estate' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

# skip_final_snapshot = true is the data-loss footgun. Catch it first.
if grep -qE 'skip_final_snapshot[[:space:]]*=[[:space:]]*true' "$DATA"; then
	fail "RDS skip_final_snapshot is true — destroy would drop the volume"
fi
grep -qE 'skip_final_snapshot[[:space:]]*=[[:space:]]*false' "$DATA" \
	|| fail "RDS does not set skip_final_snapshot = false"
grep -qE 'publicly_accessible[[:space:]]*=[[:space:]]*false' "$DATA" \
	|| fail "RDS does not set publicly_accessible = false"

# WAF with no rule block is an allow-all wearing a WAF name.
if ! grep -q 'managed_rule_group_statement' "$ING"; then
	fail "ALB WAF has no managed rule group"
fi
grep -q 'AWSManagedRulesCommonRuleSet' "$ING" \
	|| fail "ALB WAF does not attach AWSManagedRulesCommonRuleSet"

say "check-c04-rds-snapshot-waf: CLEAN — snapshot kept; WAF has common rules; unapplied."
exit 0
