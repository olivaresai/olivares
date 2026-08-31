#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c05-both-pdt-remeasure.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# export-closure: hub-only cloud/control-plane/internal/billing/dodo-cloud-product-map.json — el modulo cloud/ no viaja al export
# export-closure: hub-only cloud/control-plane/internal/tenant/manager.go — el modulo cloud/ no viaja al export
# export-closure: hub-only cloud/staging/docker-compose.yml — el modulo cloud/ no viaja al export
# Sin el modulo no hay sujeto que comprobar. La respuesta correcta es la TERCERA del
# canon —«no he podido mirar»—, no un verde y no el error crudo con el que muere hoy.
if [ ! -f "$ROOT"/cloud/control-plane/internal/billing/dodo-cloud-product-map.json ]; then
	printf '%s\n' "test-c05-both-pdt-remeasure: COULD NOT LOOK — cloud/control-plane/internal/billing/dodo-cloud-product-map.json is not in this tree" >&2
	exit 2
fi
if [ ! -f "$ROOT"/cloud/control-plane/internal/tenant/manager.go ]; then
	printf '%s\n' "test-c05-both-pdt-remeasure: COULD NOT LOOK — cloud/control-plane/internal/tenant/manager.go is not in this tree" >&2
	exit 2
fi
if [ ! -f "$ROOT"/cloud/staging/docker-compose.yml ]; then
	printf '%s\n' "test-c05-both-pdt-remeasure: COULD NOT LOOK — cloud/staging/docker-compose.yml is not in this tree" >&2
	exit 2
fi
CHECK="$ROOT/scripts/check-c05-both-pdt-remeasure.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c05r.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/cloud/control-plane/internal/billing" \
		"$TMP/tree/cloud/control-plane/internal/tenant" \
		"$TMP/tree/cloud/staging" \
		"$TMP/tree/commercial/license-worker"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c05-both-pdt-remeasure.sh"
	cp "$ROOT/design/c05-both-pdt-remeasure.json" "$TMP/tree/design/"
	cp "$ROOT/design/C05-BOTH-PDT-REMEDIDO-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/cloud/control-plane/internal/billing/dodo-cloud-product-map.json" \
		"$TMP/tree/cloud/control-plane/internal/billing/"
	cp "$ROOT/cloud/control-plane/internal/tenant/manager.go" \
		"$TMP/tree/cloud/control-plane/internal/tenant/"
	cp "$ROOT/cloud/staging/docker-compose.yml" "$TMP/tree/cloud/staging/"
	cp "$ROOT/commercial/license-worker/wrangler.jsonc" \
		"$TMP/tree/commercial/license-worker/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-c05-both-pdt-remeasure.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live both-pdt remasure is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/cloud/control-plane/internal/billing/dodo-cloud-product-map.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
del d["products"]["pdt_0NlE7ZtwL8GfOeYefL7M8"]
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: dropped yearly SKU is FAIL"
else
	bad "dropped yearly SKU should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c05-both-pdt-remeasure.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["production_fulfillment"] = True
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: production_fulfillment true is FAIL"
else
	bad "fulfillment flag should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'production fulfilment on' >>"$TMP/tree/design/C05-BOTH-PDT-REMEDIDO-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims go-live is FAIL"
else
	bad "go-live claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/C05-BOTH-PDT-REMEDIDO-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing remasure doc is COULD NOT LOOK"
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

echo "check-c05-both-pdt-remeasure selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
