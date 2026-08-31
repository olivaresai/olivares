#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c03-38-packs-v1.sh — C03-38. Signed blobs carry packs:v1.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-38-packs-v1: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-38-packs-v1: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

CLAIMS="${OLIVARES_C0338_CLAIMS:-commercial/license-worker/src/license/claims.ts}"
TEST="${OLIVARES_C0338_TEST:-commercial/license-worker/test/claims.test.ts}"
DOC="${OLIVARES_C0338_DOC:-design/C03-38-PACKS-V1-2026-08-20.md}"

[ -f "$CLAIMS" ] || cannot "missing $CLAIMS"
[ -f "$TEST" ] || cannot "missing $TEST"
[ -f "$DOC" ] || cannot "missing $DOC"

grep -q 'NOT MERGED' "$DOC" || fail "$DOC lost NOT MERGED"
if grep -qiE 'invented set:\*|FIRMA A claimed|five packs on missing marker' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi

grep -q 'PACKS_VOCAB_V1 = "packs:v1"' "$CLAIMS" \
	|| fail "claims.ts lost the packs:v1 marker constant"
grep -q 'function withPacksVocab' "$CLAIMS" \
	|| fail "claims.ts lost withPacksVocab"
grep -q 'function featuresCarryPacksVocab' "$CLAIMS" \
	|| fail "claims.ts lost featuresCarryPacksVocab"
grep -q 'claims.features = withPacksVocab(ent.features)' "$CLAIMS" \
	|| fail "buildClaims no longer stamps packs:v1"
if grep -q 'if (ent.features.length > 0) claims.features = ent.features' "$CLAIMS"; then
	fail "buildClaims still omitempty-drops an empty feature list"
fi
grep -q 'packs:v1' "$TEST" || fail "claims tests lost packs:v1"
grep -F -q 'featuresCarryPacksVocab([])' "$TEST" \
	|| fail "claims tests lost the empty-list fail-closed case"

say "check-c03-38-packs-v1: CLEAN — packs:v1 on every signed blob; missing marker is fail-closed."
exit 0
