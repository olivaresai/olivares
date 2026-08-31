#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-c05-16-serial-env.sh — C05-16. The signed Dodo serial names the
# issuer purpose so sandbox and production cannot share a document id.
# Three answers: 0 / 1 / 2.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-16-serial-env: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-16-serial-env: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"
ART=commercial/license-worker/src/dodo/provider-id.ts
ISS=commercial/license-worker/src/license/issue-context.ts
STORE=commercial/license-worker/src/store/store.ts
[ -r "$ART" ] || cannot "missing $ART"
[ -r "$ISS" ] || cannot "missing $ISS"
[ -r "$STORE" ] || cannot "missing $STORE"

grep -q 'export function credentialSerial' "$ART" \
  || fail "credentialSerial helper missing — the serial formula would live inlined and drift"
grep -q 'cred_\${purpose}_\${businessId}_\${paymentId}' "$ART" \
  || fail "credentialSerial does not embed purpose — sandbox and production share a document id"
grep -q 'export function serialMatchesIssuance' "$ART" \
  || fail "serialMatchesIssuance missing — new writes could recommit the env-less formula"
grep -q 'credentialSerial(purpose, purchase.businessId, purchase.paymentId)' "$ISS" \
  || fail "issuanceFor does not stamp purpose into the serial"
if grep -Fq 'cred_${purchase.businessId}_${purchase.paymentId}' "$ISS"; then
  fail "issuanceFor still mints the env-less serial"
fi
# ⛔ THE WRITE SIDE AND THE READ SIDE ARE NOW DIFFERENT PREDICATES, AND THIS GATE CHECKS BOTH.
#
# It used to grep for one call to `serialMatchesIssuance` inside store.ts. H-02 fix split
# the validator: migration 0029 declares the env-less `cred_<business>_<payment>` VALID for rows
# written before it, and the runtime refused it on every READ — so a complete, committed effect
# reported `incomplete` (measured: six of ten issuances on the Dodo sandbox). The reader was
# widened by exactly that one shape and the WRITER was not.
#
# A single-call grep can no longer tell those two apart, so checking it would be checking text
# instead of the property. These three lines pin the split itself: the discriminator is
# `options.initial` — set only by commitIssuanceBatch — the narrow predicate is on its TRUE arm,
# and the wide one is on the FALSE arm. Widening the write side breaks the first two.
grep -q 'const serialOk = options.initial' "$STORE" \
  || fail "the serial predicate is no longer chosen by options.initial — write/read split is gone"
grep -q '? serialMatchesIssuance(input.issuance.serial' "$STORE" \
  || fail "commitIssuanceBatch does not require a purpose-namespaced serial"
grep -q ': serialMatchesStoredIssuance(' "$STORE" \
  || fail "the read side no longer accepts the legacy serial 0029 declares valid"
if grep -Fq 'cred_${issuance.businessId}_${issuance.paymentId}' "$STORE"; then
  fail "commitIssuanceBatch still accepts the env-less serial as the live identity"
fi
grep -q 'export function serialMatchesStoredIssuance' "$ART" \
  || fail "serialMatchesStoredIssuance missing — the reader would refuse rows 0029 admits"

MIG=commercial/license-worker/migrations/0029_dodo_serial_purpose.sql
[ -r "$MIG" ] || cannot "missing $MIG — D1 CHECK still pins the env-less serial"
grep -q "cred_production_" "$MIG" \
  || fail "0029 does not admit cred_production_<business>_<payment>"
grep -q "cred_staging_" "$MIG" \
  || fail "0029 does not admit cred_staging_<business>_<payment>"
grep -q "cred_' || business_id" "$MIG" \
  || fail "0029 does not keep the env-less serial for existing rows (rewriting a signed artefact is forbidden)"

# The DB says it too, and that is strictly more than this gate asked before: a writer that
# bypasses TypeScript entirely meets 0034's BEFORE INSERT refusal.
MIG34=commercial/license-worker/migrations/0034_dodo_serial_purpose_on_write.sql
[ -r "$MIG34" ] || cannot "missing $MIG34 — nothing stops a NEW env-less serial at the database"
grep -q 'dodo_issuance_serial_names_purpose' "$MIG34" \
  || fail "0034 does not create the trigger that refuses a new env-less serial"

say "check-c05-16-serial-env: CLEAN — serial is cred_<purpose>_<business>_<payment>"
exit 0
