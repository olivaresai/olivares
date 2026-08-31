#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c04-ecr-lifecycle-rds-logs.sh — C04 unique remainder.
# Unapplied. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-ecr-lifecycle-rds-logs: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-ecr-lifecycle-rds-logs: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

COMPUTE="${OLIVARES_C04LC_COMPUTE:-deploy/aws/modules/compute/main.tf}"
DATA="${OLIVARES_C04LC_DATA:-deploy/aws/modules/data/main.tf}"
DOC="${OLIVARES_C04LC_DOC:-design/C04-ECR-LIFECYCLE-RDS-LOGS-2026-08-20.md}"

[ -f "$COMPUTE" ] || cannot "missing $COMPUTE"
[ -f "$DATA" ] || cannot "missing $DATA"
[ -f "$DOC" ] || cannot "missing $DOC"

grep -q 'NO APLICADO' "$DOC" || fail "$DOC lost NO APLICADO"
if grep -qiE 'tofu apply ran|applied the estate|terraform apply succeeded' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi
if grep -qiE 'FIRMA A claimed|FIRMA A is met' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi

grep -q 'resource "aws_ecr_lifecycle_policy"' "$COMPUTE" \
	|| fail "compute lost the ECR lifecycle policy"
grep -q 'tagStatus[[:space:]]*=[[:space:]]*"untagged"' "$COMPUTE" \
	|| fail "lifecycle does not expire untagged images first"
grep -q 'imageCountMoreThan' "$COMPUTE" \
	|| fail "lifecycle lost the tagged-count cap"
grep -q 'countNumber[[:space:]]*=[[:space:]]*10' "$COMPUTE" \
	|| fail "lifecycle tagged cap is not ten"

n="$(grep -cE 'health_check_grace_period_seconds[[:space:]]*=[[:space:]]*60' "$COMPUTE" || true)"
[ "$n" -ge 2 ] || fail "both ECS services must pin a 60s health-check grace (found $n)"

grep -q 'enabled_cloudwatch_logs_exports' "$DATA" \
	|| fail "RDS lost enabled_cloudwatch_logs_exports"
grep -q '"postgresql"' "$DATA" || fail "RDS does not export postgresql logs"
grep -q '"upgrade"' "$DATA" || fail "RDS does not export upgrade logs"
grep -qE 'auto_minor_version_upgrade[[:space:]]*=[[:space:]]*true' "$DATA" \
	|| fail "RDS auto_minor_version_upgrade is not true"
grep -qE 'skip_final_snapshot[[:space:]]*=[[:space:]]*false' "$DATA" \
	|| fail "RDS skip_final_snapshot regressed"
grep -qE 'storage_encrypted[[:space:]]*=[[:space:]]*true' "$DATA" \
	|| fail "RDS storage_encrypted regressed"

say "check-c04-ecr-lifecycle-rds-logs: CLEAN — ECR lifecycle, RDS logs, ECS grace; unapplied."
exit 0
