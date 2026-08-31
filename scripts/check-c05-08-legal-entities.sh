#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c05-08-legal-entities.sh — C05-08. Prepare who writes
# commerce.legal_entities; do not decide; do not add a production
# INSERT. Three answers.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-08-legal-entities: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-08-legal-entities: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

MIG=commercial/commerce/migrations/001_entities_orders.up.sql
DOC=design/C05-08-LEGAL-ENTITIES-PREP-2026-08-19.md
[ -r "$MIG" ] || cannot "missing $MIG"
[ -r "$DOC" ] || cannot "missing $DOC"

# The live CHECK admits four states. A "verified-only CHECK" is the
# stale canon sentence, not this tree.
grep -q "verification_state IN ('unverified', 'pending', 'verified', 'rejected')" "$MIG" \
	|| fail "CHECK no longer lists unverified/pending/verified/rejected"
grep -q "DEFAULT 'unverified'" "$MIG" \
	|| fail "DEFAULT is no longer unverified"
grep -q 'require_verified_entity' "$MIG" \
	|| fail "lost require_verified_entity — checkout would sell to an unverified row"
grep -q "IS DISTINCT FROM 'verified'" "$MIG" \
	|| fail "trigger no longer refuses a non-verified entity"

# Production Go must not INSERT. Tests may.
if command grep -R --include='*.go' --exclude='*_test.go' -n \
	'INSERT INTO commerce.legal_entities' commercial/commerce >/dev/null 2>&1; then
	fail "a production .go file INSERTs commerce.legal_entities — C05-08 does not add a writer"
fi

# The write-up must present, not apply.
grep -q 'NO ELEGIDO' "$DOC" || fail "prep doc lost NO ELEGIDO"
if grep -qiE 'elegido:|aplicamos la opción|the webhook writes verified' "$DOC"; then
	fail "prep doc claims a decision or a writer this lote does not have"
fi
grep -q 'Operator insert' "$DOC" && grep -q 'Dodo as evidence' "$DOC" \
	&& grep -q 'Dedicated verifier' "$DOC" \
	|| fail "prep doc no longer names the three options"

say "check-c05-08-legal-entities: CLEAN — four-state CHECK; trigger still deny-closed on orders; no production INSERT; options presented, none chosen."
exit 0
