#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c04-nlb-cross-zone.sh — C04. Collector NLB cross-zone is on
# (desired_count=2 is not HA if AZ-a cannot reach AZ-b). Unapplied.
# Three answers: 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-nlb-cross-zone: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-nlb-cross-zone: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

ING="${OLIVARES_C04XZ_INGRESS:-deploy/aws/modules/ingress/main.tf}"
DOC="${OLIVARES_C04XZ_DOC:-design/C04-NLB-CROSS-ZONE-2026-08-19.md}"
VARS="${OLIVARES_C04XZ_VARS:-deploy/aws/variables.tf}"

[ -f "$ING" ] || cannot "missing $ING"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$VARS" ] || cannot "missing $VARS"

if grep -q 'enable_cross_zone_load_balancing *= *false' "$ING"; then
	fail "NLB cross-zone is explicitly false"
fi
grep -q 'enable_cross_zone_load_balancing *= *true' "$ING" \
	|| fail "NLB does not set enable_cross_zone_load_balancing = true — two AZs are two singletons"

_desired="$(grep -A4 'variable "desired_count"' "$VARS" || true)"
case "$_desired" in
*default*=*2*) : ;;
*) fail "desired_count default is not 2 (cross-zone would have nothing to balance)" ;;
esac

grep -q 'NO APLICADO' "$DOC" || fail "$DOC lost NO APLICADO"
if grep -qiE 'tofu apply ran|applied the estate' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

say "check-c04-nlb-cross-zone: CLEAN — NLB cross-zone on; desired_count 2; unapplied."
exit 0
