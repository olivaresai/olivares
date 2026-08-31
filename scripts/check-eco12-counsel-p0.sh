#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-eco12-counsel-p0.sh — ECO-12. The counsel-P0 package is
# presentable. Sends it. We do not close the items. Three answers.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-eco12-counsel-p0: FAIL — $*" >&2; exit 1; }
cannot() { say "check-eco12-counsel-p0: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

PKG=design/ECO-12-COUNSEL-P0-PACKAGE-2026-08-19.md
LOTE=design/legal/COUNSEL-LOTE-1-2026-07.md
CANON=design/PRICING-CANON.md
LIC=LICENSING.md
[ -r "$PKG" ]  || cannot "missing $PKG"
[ -r "$LOTE" ] || cannot "missing $LOTE"
[ -r "$CANON" ] || cannot "missing $CANON"
[ -r "$LIC" ]  || cannot "missing $LIC"

grep -q 'No se envía\|NO se envía' "$PKG" \
  || fail "package no longer says it is not sent"
grep -q 'COUNSEL-LOTE-1' "$PKG" \
  || fail "package lost the existing counsel vehicle"
grep -q '14d' "$PKG" && grep -q '7d' "$PKG" \
  || fail "package lost the 14d vs 7d item"
grep -qiE 'indemniz' "$PKG" \
  || fail "package lost the indemnification item"

grep -q 'No enviado' "$LOTE" \
  || fail "COUNSEL-LOTE-1 no longer says it was not sent"
grep -q 'COUNSEL-P0' "$CANON" \
  || fail "canon lost COUNSEL-P0 (14d vs 7d would look decided)"
grep -q 'draft-for-counsel' "$CANON" \
  || fail "purchase_terms no longer draft-for-counsel"
grep -q 'pending a legal decision' "$LIC" \
  || fail "LICENSING.md no longer says indemnification is pending"

say "check-eco12-counsel-p0: CLEAN — package presentable; lote unsent; 14d/7d and indemnity still COUNSEL."
exit 0
