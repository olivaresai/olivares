#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-eco-13-evidence-absent.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-eco-13-evidence-absent.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/eco13a.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-eco-13-evidence-absent.sh"
	cp "$ROOT/design/eco-13-scale-gates.json" "$TMP/tree/design/"
	cp "$ROOT/design/HOLD-SCALE-GATES-2026-08-19.md" "$TMP/tree/design/"
	cp "$ROOT/design/ECO-13-EVIDENCE-ABSENT-2026-08-20.md" "$TMP/tree/design/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" OLIVARES_ENT_DIR="" \
		bash "$TMP/tree/scripts/check-eco-13-evidence-absent.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live absence pin is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-13-scale-gates.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["gates"][0]["race_test"] = True
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: race_test true is FAIL"
else
	bad "race_test true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
mkdir -p "$TMP/tree/pkg"
printf '%s\n' 'package pkg' 'func TestReportingCrossTenantRace(t *testing.T) {}' \
	>"$TMP/tree/pkg/race_test.go"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: planted close-test is FAIL"
else
	bad "planted TestReportingCrossTenantRace should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-13-scale-gates.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["hub"] = "not-a-sha"
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: truncated hub SHA is FAIL"
else
	bad "bad hub SHA should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'implemented the seven' >>"$TMP/tree/design/ECO-13-EVIDENCE-ABSENT-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims implemented is FAIL"
else
	bad "implemented claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/ECO-13-EVIDENCE-ABSENT-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing evidence-absent doc is COULD NOT LOOK"
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

echo "check-eco-13-evidence-absent selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
