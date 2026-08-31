#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c04-alb-drop-invalid-headers.sh — C04. ALB drops invalid
# headers (default OFF). Unapplied. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-alb-drop-invalid-headers: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-alb-drop-invalid-headers: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

ING="${OLIVARES_C04HDR_INGRESS:-deploy/aws/modules/ingress/main.tf}"
DOC="${OLIVARES_C04HDR_DOC:-design/C04-ALB-DROP-INVALID-HEADERS-2026-08-19.md}"

[ -f "$ING" ] || cannot "missing $ING"
[ -f "$DOC" ] || cannot "missing $DOC"

if grep -q 'drop_invalid_header_fields *= *false' "$ING"; then
	fail "ALB drop_invalid_header_fields is explicitly false"
fi
alb_true=$(awk '
  $0 ~ /resource "aws_lb" "alb"/ {p=1}
  p && /drop_invalid_header_fields *= *true/ {print "yes"; exit}
  p && /^resource / && $0 !~ /resource "aws_lb" "alb"/ {exit}
' "$ING")
[ "$alb_true" = yes ] || fail "ALB does not set drop_invalid_header_fields = true"

# Sin tuberia: `<lista> | grep -q` bajo `pipefail` devuelve 141 EN EXITO cuando
# grep cierra el tubo antes de que awk termine de escribir. Se captura y se
# compara, igual que la comprobacion gemela del ALB unas lineas arriba.
nlb_set="$(awk '
  $0 ~ /resource "aws_lb" "nlb"/ {p=1}
  p && /drop_invalid_header_fields/ {print "yes"; exit}
  p && /^resource / && $0 !~ /resource "aws_lb" "nlb"/ {exit}
' "$ING")"
if [ "$nlb_set" = yes ]; then
	fail "NLB must not set drop_invalid_header_fields — it has no header parser"
fi

grep -q 'NO APLICADO' "$DOC" || fail "$DOC lost NO APLICADO"
if grep -qiE 'tofu apply ran|applied the estate' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

say "check-c04-alb-drop-invalid-headers: CLEAN — ALB drops invalid headers; unapplied."
exit 0
