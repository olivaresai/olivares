#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c05-03-seatcap-inert.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# export-closure: hub-only cloud/control-plane/internal/admin/api.go — el modulo cloud/ no viaja al export
# export-closure: hub-only cloud/control-plane/internal/billing/dodo-cloud-product-map.json — el modulo cloud/ no viaja al export
# export-closure: hub-only cloud/control-plane/internal/billing/entitlement.go — el modulo cloud/ no viaja al export
# export-closure: hub-only cloud/control-plane/internal/tenant/manager.go — el modulo cloud/ no viaja al export
# Sin el modulo no hay sujeto que comprobar. La respuesta correcta es la TERCERA del
# canon —«no he podido mirar»—, no un verde y no el error crudo con el que muere hoy.
if [ ! -f "$ROOT"/cloud/control-plane/internal/admin/api.go ]; then
	printf '%s\n' "test-c05-03-seatcap-inert: COULD NOT LOOK — cloud/control-plane/internal/admin/api.go is not in this tree" >&2
	exit 2
fi
if [ ! -f "$ROOT"/cloud/control-plane/internal/billing/dodo-cloud-product-map.json ]; then
	printf '%s\n' "test-c05-03-seatcap-inert: COULD NOT LOOK — cloud/control-plane/internal/billing/dodo-cloud-product-map.json is not in this tree" >&2
	exit 2
fi
if [ ! -f "$ROOT"/cloud/control-plane/internal/billing/entitlement.go ]; then
	printf '%s\n' "test-c05-03-seatcap-inert: COULD NOT LOOK — cloud/control-plane/internal/billing/entitlement.go is not in this tree" >&2
	exit 2
fi
if [ ! -f "$ROOT"/cloud/control-plane/internal/tenant/manager.go ]; then
	printf '%s\n' "test-c05-03-seatcap-inert: COULD NOT LOOK — cloud/control-plane/internal/tenant/manager.go is not in this tree" >&2
	exit 2
fi
CHECK="$ROOT/scripts/check-c05-03-seatcap-inert.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0503.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/cloud/control-plane/internal/billing" \
		"$TMP/tree/cloud/control-plane/internal/admin" \
		"$TMP/tree/cloud/control-plane/internal/tenant"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c05-03-seatcap-inert.sh"
	cp "$ROOT/design/c05-03-seatcap-inert.json" "$TMP/tree/design/"
	cp "$ROOT/design/C05-03-SEATCAP-INERT-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/cloud/control-plane/internal/billing/dodo-cloud-product-map.json" \
		"$TMP/tree/cloud/control-plane/internal/billing/"
	cp "$ROOT/cloud/control-plane/internal/billing/entitlement.go" \
		"$TMP/tree/cloud/control-plane/internal/billing/"
	cp "$ROOT/cloud/control-plane/internal/admin/api.go" \
		"$TMP/tree/cloud/control-plane/internal/admin/"
	cp "$ROOT/cloud/control-plane/internal/tenant/manager.go" \
		"$TMP/tree/cloud/control-plane/internal/tenant/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-c05-03-seatcap-inert.sh" \
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
python3 - "$TMP/tree/cloud/control-plane/internal/billing/dodo-cloud-product-map.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["products"]["pdt_0NlE7N9AZ9CV7wNAemXAO"]["max_seats"] = 5
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: monthly max_seats 5 is FAIL"
else
	bad "max_seats 5 should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/cloud/control-plane/internal/billing/dodo-cloud-product-map.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["products"]["pdt_0NlE7ZtwL8GfOeYefL7M8"]["features"] = ["sso"]
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: planted feature is FAIL"
else
	bad "planted feature should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c05-03-seatcap-inert.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["sixth_seat_refused"] = True
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: sixth_seat_refused true is FAIL"
else
	bad "false close should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/cloud/control-plane/internal/billing/entitlement.go" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
p.write_text(p.read_text().replace("maxSeats <= 0", "maxSeats < 0"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: unlimited branch dropped is FAIL"
else
	bad "dropped unlimited should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'C05-03 closed' >>"$TMP/tree/design/C05-03-SEATCAP-INERT-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims C05-03 closed is FAIL"
else
	bad "closed claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/c05-03-seatcap-inert.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing JSON is LOOK (2)"
else
	bad "missing JSON should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: restored live stays CLEAN"
else
	bad "restored live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo "check-c05-03-seatcap-inert selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
