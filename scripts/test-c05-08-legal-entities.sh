#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for C05-08. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c05-08-legal-entities.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0508.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" \
		"$TMP/tree/commercial/commerce/migrations" \
		"$TMP/tree/commercial/commerce/internal/orders" \
		"$TMP/tree/design"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c05-08-legal-entities.sh"
	cp "$ROOT/commercial/commerce/migrations/001_entities_orders.up.sql" \
		"$TMP/tree/commercial/commerce/migrations/"
	cp "$ROOT/design/C05-08-LEGAL-ENTITIES-PREP-2026-08-19.md" \
		"$TMP/tree/design/"
	# a test INSERT is legal
	printf '%s\n' 'package orders_test' \
		'// INSERT INTO commerce.legal_entities (entity_id) VALUES (1)' \
		>"$TMP/tree/commercial/commerce/internal/orders/orders_test.go"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c05-08-legal-entities.sh" \
		>/dev/null 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return "$rc"
}

stage
if run; then ok "live prep is CLEAN"
else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
sed -i "s/'unverified', 'pending', 'verified', 'rejected'/'verified'/" \
	"$TMP/tree/commercial/commerce/migrations/001_entities_orders.up.sql"
if run; then bad "verified-only CHECK stayed CLEAN"
else ok "mutant (CHECK only verified — the stale canon sentence) is killed"; fi

stage
mkdir -p "$TMP/tree/commercial/commerce/internal/write"
printf '%s\n' 'package write' \
	'const q = `INSERT INTO commerce.legal_entities (entity_id, display_name, country, verification_state)`' \
	>"$TMP/tree/commercial/commerce/internal/write/writer.go"
if run; then bad "production INSERT stayed CLEAN"
else ok "mutant (production INSERT) is killed"; fi

stage
sed -i 's/NO ELEGIDO/elegido: el webhook writes verified/' \
	"$TMP/tree/design/C05-08-LEGAL-ENTITIES-PREP-2026-08-19.md"
if run; then bad "doc that claims a decision stayed CLEAN"
else ok "mutant (doc claims we decided) is killed"; fi

stage
rm -f "$TMP/tree/design/C05-08-LEGAL-ENTITIES-PREP-2026-08-19.md"
if run; then bad "missing prep doc stayed CLEAN"
else
	if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing prep doc is COULD NOT LOOK"
	else bad "missing doc should be 2 ($(cat "$TMP/err"))"; fi
fi

stage
if run; then ok "no-fire: live prep stays CLEAN"
else bad "restored live should be CLEAN ($(cat "$TMP/err"))"; fi

printf 'check-c05-08-legal-entities selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
