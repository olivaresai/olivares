#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c03-38-packs-v1.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c03-38-packs-v1.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0338.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" \
		"$TMP/tree/design" \
		"$TMP/tree/commercial/license-worker/src/license" \
		"$TMP/tree/commercial/license-worker/test"
	cp "$CHECK" "$TMP/tree/scripts/check-c03-38-packs-v1.sh"
	chmod +x "$TMP/tree/scripts/check-c03-38-packs-v1.sh"
	cat >"$TMP/tree/commercial/license-worker/src/license/claims.ts" <<'EOF'
export const PACKS_VOCAB_V1 = "packs:v1";
export function featuresCarryPacksVocab(features) { return true; }
export function withPacksVocab(features) { return [PACKS_VOCAB_V1]; }
claims.features = withPacksVocab(ent.features);
EOF
	cat >"$TMP/tree/commercial/license-worker/test/claims.test.ts" <<'EOF'
packs:v1
featuresCarryPacksVocab([])
EOF
	cat >"$TMP/tree/design/C03-38-PACKS-V1-2026-08-20.md" <<'EOF'
NOT MERGED. packs:v1 on the wire. No invented set: membership.
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c03-38-packs-v1.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: pins present is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i 's/PACKS_VOCAB_V1 = "packs:v1"/PACKS_VOCAB_V1 = "packs:v0"/' \
	"$TMP/tree/commercial/license-worker/src/license/claims.ts"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: marker renamed is FAIL"
else
	bad "renamed marker should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i 's/claims.features = withPacksVocab(ent.features)/if (ent.features.length > 0) claims.features = ent.features/' \
	"$TMP/tree/commercial/license-worker/src/license/claims.ts"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: omitempty empty list is FAIL"
else
	bad "omitempty empty list should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'invented set:* membership' >>"$TMP/tree/design/C03-38-PACKS-V1-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: invented set membership is FAIL"
else
	bad "invented membership should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/commercial/license-worker/src/license/claims.ts"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing claims.ts is LOOK (2)"
else
	bad "missing claims should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

echo
echo "test-c03-38-packs-v1: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
