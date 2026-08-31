#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c13-06-canon-proposals.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c13-06-canon-proposals.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c1306.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

good_canon() {
	cat >"$TMP/tree/design/PRICING-CANON.md" <<'EOF'
    modules:
      - reporting
    modules_day_one:
      - content-firewall
    modules_growth:
      - retrieval-scan
    modules_on:
      - retrievalscan
    modules_hold_gated:
      - reporting
  self_hosted.enterprise:
    scope:
      - custom-credential-exchange
EOF
}

good_doc() {
	cat >"$TMP/tree/design/C13-06-CANON-PROPOSALS-2026-08-19.md" <<'EOF'
NO ELEGIDO. NO APLICADO.
modules_day_one, self_hosted.enterprise, retrievalscan.
EOF
}

good_wire() {
	mkdir -p "$TMP/tree/cmd/olivares"
	cat >"$TMP/tree/cmd/olivares/wire_noenterprise.go" <<'EOF'
// (enterprise/computeruse) is additive
EOF
}

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/design" "$TMP/tree/scripts" "$TMP/tree/cmd/olivares"
	cp "$CHECK" "$TMP/tree/scripts/check-c13-06-canon-proposals.sh"
	chmod +x "$TMP/tree/scripts/check-c13-06-canon-proposals.sh"
	good_canon
	good_doc
	good_wire
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-c13-06-canon-proposals.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

# 1. no-fire
stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: proposals presented, canon untouched is CLEAN"
else
	bad "untouched canon should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# 2. firing: proposal 1 applied (lost modules_day_one)
stage
grep -v modules_day_one "$TMP/tree/design/PRICING-CANON.md" >"$TMP/tree/design/PRICING-CANON.md.tmp"
mv "$TMP/tree/design/PRICING-CANON.md.tmp" "$TMP/tree/design/PRICING-CANON.md"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'modules_day_one' "$TMP/err"; then
	ok "firing: applying proposal 1 is FAIL"
else
	bad "lost modules_day_one should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# 3. firing: proposal 3 applied (unified spelling)
stage
sed -i 's/retrieval-scan/retrievalscan/' "$TMP/tree/design/PRICING-CANON.md"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'retrieval-scan' "$TMP/err"; then
	ok "firing: unifying retrieval-scan is FAIL"
else
	bad "lost retrieval-scan should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# 4. firing: doc claims application
stage
echo 'canon rewritten' >>"$TMP/tree/design/C13-06-CANON-PROPOSALS-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'claims an application' "$TMP/err"; then
	ok "firing: doc claiming application is FAIL"
else
	bad "applied claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# 5. LOOK: missing doc
stage
rm -f "$TMP/tree/design/C13-06-CANON-PROPOSALS-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing proposals doc is LOOK (2)"
else
	bad "missing doc should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# 6. no-fire after firing
stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire after a firing case still CLEAN"
else
	bad "second untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo
echo "test-c13-06-canon-proposals: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
