#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c04-lb-deletion-protection.sh — C04. NLB and ALB refuse destroy.
# RDS already does. Unapplied. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-lb-deletion-protection: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-lb-deletion-protection: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

ING="${OLIVARES_C04DP_INGRESS:-deploy/aws/modules/ingress/main.tf}"
DATA="${OLIVARES_C04DP_DATA:-deploy/aws/modules/data/main.tf}"
DOC="${OLIVARES_C04DP_DOC:-design/C04-LB-DELETION-PROTECTION-2026-08-19.md}"

[ -f "$ING" ] || cannot "missing $ING"
[ -f "$DATA" ] || cannot "missing $DATA"
[ -f "$DOC" ] || cannot "missing $DOC"

if grep -q 'enable_deletion_protection *= *false' "$ING"; then
	fail "an LB explicitly disables deletion protection"
fi
# false before true: an LB that sets false would otherwise still match true elsewhere.
nlb_true=$(awk '
  $0 ~ /resource "aws_lb" "nlb"/ {p=1}
  p && /enable_deletion_protection *= *true/ {print "yes"; exit}
  p && /^resource / && $0 !~ /resource "aws_lb" "nlb"/ {exit}
' "$ING")
alb_true=$(awk '
  $0 ~ /resource "aws_lb" "alb"/ {p=1}
  p && /enable_deletion_protection *= *true/ {print "yes"; exit}
  p && /^resource / && $0 !~ /resource "aws_lb" "alb"/ {exit}
' "$ING")
[ "$nlb_true" = yes ] || fail "NLB does not set enable_deletion_protection = true"
[ "$alb_true" = yes ] || fail "ALB does not set enable_deletion_protection = true"

grep -q 'deletion_protection *= *true' "$DATA" \
	|| fail "RDS lost deletion_protection = true"

grep -q 'NO APLICADO' "$DOC" || fail "$DOC lost NO APLICADO"
if grep -qiE 'tofu apply ran|applied the estate' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

say "check-c04-lb-deletion-protection: CLEAN — NLB+ALB+RDS deletion protection on; unapplied."
exit 0
