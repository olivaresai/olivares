#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-eco-11-reserve-funded.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-eco-11-reserve-funded.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/eco11.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design/preparado"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-eco-11-reserve-funded.sh"
	cp "$ROOT/design/ECO-11-RESERVE-FUNDED-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/design/eco-11-reserve-funded.json" "$TMP/tree/design/"
	cp "$ROOT/design/preparado/AB-01-RESERVA-HOJA-DE-DECISION-2026-08-01.md" \
		"$TMP/tree/design/preparado/"
	cp "$ROOT/design/PRICING-CANON.md" "$TMP/tree/design/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-eco-11-reserve-funded.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live package is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/PRICING-CANON.md" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
p.write_text(p.read_text().replace(
    "selected_rate_row: UNKNOWN",
    "selected_rate_row: S-B",
    1,
))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: AF-02 decided in canon is FAIL"
else
	bad "decided row should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/PRICING-CANON.md" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
p.write_text(p.read_text().replace(
    "funded_amount: UNKNOWN",
    "funded_amount: 10000",
    1,
))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: invented funded_amount is FAIL"
else
	bad "invented amount should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/PRICING-CANON.md" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
p.write_text(p.read_text().replace(
    "refund_fee_adder: UNKNOWN",
    "refund_fee_adder: 0",
))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: U_f set to zero is FAIL"
else
	bad "U_f zero should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/preparado/AB-01-RESERVA-HOJA-DE-DECISION-2026-08-01.md" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
lines = [ln for ln in p.read_text().splitlines(True) if "[ ] ADOPTO S-B" not in ln]
p.write_text("".join(lines))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: AB-01 lost S-B box is FAIL"
else
	bad "lost S-B box should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'selected_rate_row: S-B' >>"$TMP/tree/design/ECO-11-RESERVE-FUNDED-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: package claims selected S-B is FAIL"
else
	bad "package dictamen should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
printf '\nfunded_amount: $25000\n' \
	>>"$TMP/tree/design/ECO-11-RESERVE-FUNDED-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: package smuggles an amount is FAIL"
else
	bad "smuggled amount should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-11-reserve-funded.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["decided"] = True
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: decided true is FAIL"
else
	bad "decided flag should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/ECO-11-RESERVE-FUNDED-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing package is COULD NOT LOOK"
else
	bad "missing package should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: restored live stays CLEAN"
else
	bad "restored live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo "check-eco-11-reserve-funded selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
