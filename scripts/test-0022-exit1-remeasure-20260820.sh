#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-0022-exit1-remeasure-20260820.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-0022-exit1-remeasure-20260820.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0022r.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/commercial/license-worker/migrations"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-0022-exit1-remeasure-20260820.sh"
	cp "$ROOT/design/c0022-exit1-remeasure-20260820.json" "$TMP/tree/design/"
	cp "$ROOT/design/VEREDICTO-0022-SALIDA-1-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/commercial/license-worker/migrations/0016_dodo_cohort_barrier.sql" \
		"$TMP/tree/commercial/license-worker/migrations/"
	cp "$ROOT/commercial/license-worker/migrations/0018_dodo_atomic_issuance.sql" \
		"$TMP/tree/commercial/license-worker/migrations/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-0022-exit1-remeasure-20260820.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live exit-1 remasure is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
printf '%s\n' '-- probe' >"$TMP/tree/commercial/license-worker/migrations/0022_dodo_fulfillment.sql"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: planted 0022 sql is FAIL"
else
	bad "0022 sql should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c0022-exit1-remeasure-20260820.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["exit"] = 2
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: exit 2 is FAIL"
else
	bad "exit 2 should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo '0022 creates the tables' >>"$TMP/tree/design/VEREDICTO-0022-SALIDA-1-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims CREATE is FAIL"
else
	bad "CREATE claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/VEREDICTO-0022-SALIDA-1-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing verdict doc is COULD NOT LOOK"
else
	bad "missing doc should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: restored live stays CLEAN"
else
	bad "restored live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo "check-0022-exit1-remeasure selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
