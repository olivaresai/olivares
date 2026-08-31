#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c04-ecs-circuit-breaker.sh — C04. ECS circuit breaker on, unapplied.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-ecs-circuit-breaker: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-ecs-circuit-breaker: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

COMPUTE="${OLIVARES_C04CB_COMPUTE:-deploy/aws/modules/compute/main.tf}"
DOC="${OLIVARES_C04CB_DOC:-design/C04-ECS-CIRCUIT-BREAKER-2026-08-20.md}"

[ -f "$COMPUTE" ] || cannot "missing $COMPUTE"
[ -f "$DOC" ] || cannot "missing $DOC"

grep -q 'NO APLICADO' "$DOC" || fail "$DOC lost NO APLICADO"
if grep -qiE 'tofu apply ran|applied the estate' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

n="$(grep -c 'deployment_circuit_breaker' "$COMPUTE" || true)"
[ "$n" -ge 2 ] || fail "both ECS services must name deployment_circuit_breaker (found $n)"
en="$(grep -cE 'enable[[:space:]]*=[[:space:]]*true' "$COMPUTE" || true)"
[ "$en" -ge 2 ] || fail "circuit breaker enable must be true on both services (found $en)"
rb="$(grep -cE 'rollback[[:space:]]*=[[:space:]]*true' "$COMPUTE" || true)"
[ "$rb" -ge 2 ] || fail "circuit breaker rollback must be true on both services (found $rb)"
if grep -qE 'rollback[[:space:]]*=[[:space:]]*false' "$COMPUTE"; then
	fail "a service enables the breaker without rollback"
fi
grep -q 'resource "aws_ecs_service" "cp"' "$COMPUTE" \
	|| fail "compute lost the control-plane ECS service"
grep -q 'resource "aws_ecs_service" "engine"' "$COMPUTE" \
	|| fail "compute lost the engine ECS service"

say "check-c04-ecs-circuit-breaker: CLEAN — circuit breaker enable+rollback on both services; unapplied."
exit 0
