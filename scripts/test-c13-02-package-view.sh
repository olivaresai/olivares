#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c13-02-package-view.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c13-02-package-view.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c1302v.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" "$TMP/tree/commercial"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c13-02-package-view.sh"
	cp "$ROOT/design/c13-02-package-view.json" "$TMP/tree/design/"
	cp "$ROOT/design/C13-02-PACKAGE-VIEW-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/commercial/module-slug-package.json" "$TMP/tree/commercial/"
	cp "$ROOT/commercial/module-package-slugs.json" "$TMP/tree/commercial/"
	# ⛔ Y EL DERIVADOR, que es quien decide desde que este gate le pregunta. Sin él el gate contesta
	# —con razón— «falta el envoltorio: un 127 no es un veredicto», y los casos medirían la ausencia
	# de una herramienta en vez de la guarda. Lo cazó el propio banco al ponerse rojo entero.
	cp "$ROOT/scripts/module-catalog-go.sh" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/module-catalog-go.sh"
	mkdir -p "$TMP/tree/commercial/license-worker/src/catalog" "$TMP/tree/commercial/license-worker/contracts"
	cp "$ROOT/design/PRICING-CANON.md" "$TMP/tree/design/"
	cp "$ROOT/commercial/license-worker/src/catalog/module-slug-package.json" \
		"$TMP/tree/commercial/license-worker/src/catalog/"
	cp -r "$ROOT/commercial/commerce-lint" "$TMP/tree/commercial/"
}

export GOWORK=off
MCBIN="$(mktemp -u "${TMPDIR:-/workspace/.olivares-tmptest}/pv-bin.XXXXXX")"
( cd "$ROOT/commercial/commerce-lint" && go build -o "$MCBIN" . ) >/dev/null 2>&1 || {
	echo "test-c13-02-package-view: NO PUDE MIRAR — el derivador no construye" >&2; exit 2; }
export OLIVARES_MODULE_CATALOG_BIN="$MCBIN"

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-c13-02-package-view.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live reverse view is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/commercial/module-package-slugs.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["packages"]["enterprise/federation"] = ["federation-multi-idp"]
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: dropped shared slug is FAIL"
else
	bad "dropped shared slug should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/commercial/module-package-slugs.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["packages"]["enterprise/bizpack"] = ["biz"]
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: planted pack slug is FAIL"
else
	bad "pack slug should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c13-02-package-view.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["observed_bijective"] = True
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: observed_bijective true is FAIL"
else
	bad "observed_bijective flag should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'FIRMA A claimed' >>"$TMP/tree/design/C13-02-PACKAGE-VIEW-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims FIRMA A is FAIL"
else
	bad "FIRMA A claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/commercial/module-package-slugs.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing view is LOOK (2)"
else
	bad "missing view should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: restored live stays CLEAN"
else
	bad "restored live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo "check-c13-02-package-view selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
