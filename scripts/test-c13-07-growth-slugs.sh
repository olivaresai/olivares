#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c13-07-growth-slugs.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c13-07-growth-slugs.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c1307g.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c13-07-growth-slugs.sh"
	cp "$ROOT/design/PRICING-CANON.md" "$TMP/tree/design/"
	cp "$ROOT/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" "$TMP/tree/design/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c13-07-growth-slugs.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live HOLD is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i '/hold-growth-slug: retrieval-scan/d' \
	"$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: missing growth slug is FAIL"
else
	bad "missing growth slug should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i 's/hold-growth-slug: retrieval-scan/hold-growth-slug: not-a-growth-slug/' \
	"$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: invented growth slug is FAIL"
else
	bad "invented slug should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
s = p.read_text()
s = s.replace("| AR-2 |", "| XX-2 |", 1)
p.write_text(s)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: AR-2 row removed is FAIL"
else
	bad "AR-2 gone should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing HOLD doc is COULD NOT LOOK"
else
	bad "missing HOLD should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: restored live stays CLEAN"
else
	bad "restored live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo "check-c13-07-growth-slugs selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
