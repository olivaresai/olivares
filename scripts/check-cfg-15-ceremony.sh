#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-cfg-15-ceremony.sh — CFG-15. Three generated pairs are not custody.
# The remainder must stay named until executes it.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-cfg-15-ceremony: FAIL — $*" >&2; exit 1; }
cannot() { say "check-cfg-15-ceremony: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"
DOC="design/CFG-15-CEREMONIA-REMAINDER-2026-08-19.md"
REG="design/REGISTRO-DECISIONES-2026-08-01.md"
[ -r "$DOC" ] || cannot "$DOC missing"
[ -r "$REG" ] || cannot "$REG missing"

required=(
  move-private-keys-off-clone
  encrypted-backup-1
  encrypted-backup-2
  restore-from-backup
  smoke-sign-verify
  rev-parse-HEAD-at-ceremony
)
for r in "${required[@]}"; do
  grep -qE "^remainder:[[:space:]]*${r}[[:space:]]*$" "$DOC" \
    || fail "remainder missing: $r"
done

# The remainder document must not declare the ceremony done.
# Do not match "cannot be re-closed" / "does not claim … closed".
if grep -qiE 'the ceremony is complete|ceremonia (está )?cerrada|custody is complete' "$DOC"; then
  fail "CFG-15 doc claims the ceremony is complete"
fi

# The register must still carry the reopened YA-74 remainder.
grep -q 'YA-74' "$REG" || fail "REGISTRO lost YA-74"
grep -q 'siguen pendientes' "$REG" || fail "REGISTRO YA-74 lost «siguen pendientes»"

say "check-cfg-15-ceremony: CLEAN — 6 remainders named; ceremony not claimed complete."
exit 0
