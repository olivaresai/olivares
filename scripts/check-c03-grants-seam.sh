#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-c03-grants-seam.sh — C03. The live holder crosses one edition port.
# Its grants remain available; Community does not consult it. Three answers.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-grants-seam: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-grants-seam: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

HOLD=cmd/olivares/license_holder.go
BOOT=cmd/olivares/boot.go
NOENT=cmd/olivares/wire_noenterprise.go
PORTS=cmd/olivares/edition_ports.go
WIRE=cmd/olivares/seatcapwire.go
[ -r "$HOLD" ] || cannot "missing $HOLD"
[ -r "$BOOT" ] || cannot "missing $BOOT"
[ -r "$NOENT" ] || cannot "missing $NOENT"
[ -r "$PORTS" ] || cannot "missing $PORTS"
[ -r "$WIRE" ] || cannot "missing $WIRE"

grep -Fq 'func (h *licenseHolder) grants() ([]license.Grant, bool)' "$HOLD" \
  || fail "licenseHolder lost grants()"
grep -Fq 'thisEdition.seatPolicy(licHolder, crlViewFromDataDir(b.cfg.DataDir))' "$BOOT" \
  || fail "boot.go does not bind the live holder and its CRL through the license port"
grep -Fq 'seatPolicy func(*licenseHolder, crlViewFunc) auth.SeatPolicy' "$PORTS" \
  || fail "the holder license port disappeared"
if grep -q 'bindEntitlement' "$PORTS"; then
  fail "a second license port remains"
fi
# Community returns an unlimited policy and never reads the holder.
grep -q '^func editionPortsForBuild()' "$NOENT" \
  || fail "the Community edition initializer disappeared"
if grep -Eq '\.(claims|grants|live)\(' "$NOENT"; then
  fail "AGPL consults the license holder"
fi
grep -Fq 'return auth.NewCommunitySeatPolicy()' "$NOENT" \
  || fail "AGPL lost its unlimited policy"
grep -q 'type licenseGrantsFunc' "$WIRE" \
  || fail "licenseGrantsFunc adapter disappeared"

# Doctrine: this build must not import the closed gate.
# Capturar y decidir, sin `| grep -q` final: bajo pipefail esa forma devuelve 141 EN ÉXITO cuando
# el consumidor cierra antes de que el productor termine.
_fugas="$(grep -n 'enterprise/addongate' cmd/olivares/*.go 2>/dev/null | grep -v '_enterprise.go' || true)"
if [ -n "$_fugas" ]; then
  fail "AGPL cmd/olivares imports enterprise/addongate"
fi

# The comment next to the no-op must keep the three off-limits.
grep -q 'does not gate reads, export, or deny-closed evaluation' "$PORTS" \
  || fail "AGPL binder lost the addongate doctrine"

say "check-c03-grants-seam: CLEAN — one holder port; AGPL does not consult it; no addongate import."
exit 0
