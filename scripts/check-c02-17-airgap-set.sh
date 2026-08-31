#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c02-17-airgap-set.sh — C02-17. Enterprise air-gap bundles are per
# commercial set. export-update-bundle.sh must take --set, refuse a mixed
# tree without it, and pack only that slug's archives. Community flat
# dirs stay valid without --set. Three answers.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-17-airgap-set: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-17-airgap-set: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

BUNDLE="$ROOT/scripts/export-update-bundle.sh"
SETS="$ROOT/commercial/license-worker/src/download/sets.ts"
[ -r "$BUNDLE" ] || cannot "missing $BUNDLE"
[ -r "$SETS" ] || cannot "missing $SETS (ALLOWED_SET_SLUGS is the allowlist)"

grep -q -- '--set)' "$BUNDLE" || fail "export-update-bundle.sh lost --set"
grep -q 'ALLOWED_SET_SLUGS' "$BUNDLE" || fail "bundle producer does not read ALLOWED_SET_SLUGS"
grep -q 'has_set_layout' "$BUNDLE" || fail "bundle producer lost the mixed-tree refusal"
grep -q 'ARCHDIR' "$BUNDLE" || fail "bundle producer does not resolve a per-set ARCHDIR"
# Packing must glob ARCHDIR, not DIR/*/ — a recursive glob on a mixed tree
# would pack two SKUs that share olivares_<v>_<os>_<arch>.tar.gz.
if grep -E 'for f in "\$DIR"/\*/olivares_|for f in "\$DIR"/\*\*/olivares_' "$BUNDLE"; then
	fail "archive glob is recursive on DIR; a mixed tree would pack two SKUs under one basename"
fi
grep -q 'for f in "\$ARCHDIR"/olivares_' "$BUNDLE" \
	|| fail "archive glob is not ARCHDIR/olivares_ (would ignore --set)"
grep -q -- '--list-files)' "$BUNDLE" || fail "lost --list-files (the hermetic seam)"

# Community path must survive: omitting --set on a FLAT dir is still legal.
grep -q 'ARCHDIR="\$DIR"' "$BUNDLE" || fail "community ARCHDIR=DIR assignment is gone"

say "check-c02-17-airgap-set: CLEAN — --set is wired; mixed trees refuse; glob is ARCHDIR; community flat dir stays."
exit 0
