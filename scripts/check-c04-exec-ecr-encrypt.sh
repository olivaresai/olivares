#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c04-exec-ecr-encrypt.sh — C04. ECS execute_command off; ECR AES256.
# Unapplied. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-exec-ecr-encrypt: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-exec-ecr-encrypt: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

COMPUTE="${OLIVARES_C04EXEC_COMPUTE:-deploy/aws/modules/compute/main.tf}"
DOC="${OLIVARES_C04EXEC_DOC:-design/C04-EXEC-ECR-ENCRYPT-2026-08-20.md}"

[ -f "$COMPUTE" ] || cannot "missing $COMPUTE"
[ -f "$DOC" ] || cannot "missing $DOC"

grep -q 'NO APLICADO' "$DOC" || fail "$DOC lost NO APLICADO"
if grep -qiE 'tofu apply ran|applied the estate' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

grep -q 'resource "aws_ecr_repository"' "$COMPUTE" \
	|| fail "compute lost the ECR repository"
grep -q 'encryption_configuration' "$COMPUTE" \
	|| fail "ECR lost encryption_configuration"
grep -qE 'encryption_type[[:space:]]*=[[:space:]]*"AES256"' "$COMPUTE" \
	|| fail "ECR encryption_type is not AES256"
if grep -qE 'encryption_type[[:space:]]*=[[:space:]]*"KMS"' "$COMPUTE"; then
	fail "ECR names KMS without a provisioned customer key"
fi

n="$(grep -cE 'enable_execute_command[[:space:]]*=[[:space:]]*false' "$COMPUTE" || true)"
[ "$n" -ge 2 ] || fail "both ECS services must pin execute_command off (found $n)"
if grep -qE 'enable_execute_command[[:space:]]*=[[:space:]]*true' "$COMPUTE"; then
	fail "an ECS service turns execute_command on"
fi
grep -q 'resource "aws_ecs_service" "cp"' "$COMPUTE" \
	|| fail "compute lost the control-plane ECS service"
grep -q 'resource "aws_ecs_service" "engine"' "$COMPUTE" \
	|| fail "compute lost the engine ECS service"

say "check-c04-exec-ecr-encrypt: CLEAN — execute_command off; ECR AES256; unapplied."
exit 0
