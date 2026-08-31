#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-c03-grants-seam.sh — C03. The AGPL build exposes grants() and
# binds it. It does not gate. Three answers.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-grants-seam: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-grants-seam: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

HOLD=cmd/olivares/license_holder.go
BOOT=cmd/olivares/boot.go
NOENT=cmd/olivares/wire_noenterprise.go
WIRE=cmd/olivares/seatcapwire.go
[ -r "$HOLD" ] || cannot "missing $HOLD"
[ -r "$BOOT" ] || cannot "missing $BOOT"
[ -r "$NOENT" ] || cannot "missing $NOENT"
[ -r "$WIRE" ] || cannot "missing $WIRE"

grep -Fq 'func (h *licenseHolder) grants() ([]license.Grant, bool)' "$HOLD" \
  || fail "licenseHolder lost grants()"
grep -q 'bindEnterpriseEntitlement(licHolder.grants)' "$BOOT" \
  || fail "boot.go does not bind licHolder.grants"
grep -q 'func bindEnterpriseEntitlement(_ licenseGrantsFunc) {}' "$NOENT" \
  || fail "AGPL bindEnterpriseEntitlement is not a no-op"
grep -q 'type licenseGrantsFunc' "$WIRE" \
  || fail "licenseGrantsFunc seam disappeared"

# Doctrine: this build must not import the closed gate.
# Capturar y decidir, sin `| grep -q` final: bajo pipefail esa forma devuelve 141 EN ÉXITO cuando
# el consumidor cierra antes de que el productor termine.
_fugas="$(grep -n 'enterprise/addongate' cmd/olivares/*.go 2>/dev/null | grep -v '_enterprise.go' || true)"
if [ -n "$_fugas" ]; then
  fail "AGPL cmd/olivares imports enterprise/addongate"
fi

# The comment next to the no-op must keep the three off-limits.
grep -q 'does not gate reads, export, or deny-closed evaluation' "$NOENT" \
  || fail "AGPL binder lost the addongate doctrine"

say "check-c03-grants-seam: CLEAN — grants() bound; AGPL no-op; no addongate import."
exit 0
