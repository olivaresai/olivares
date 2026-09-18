#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c13-02-worker-map.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c13-02-worker-map.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c1302w.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

# Built once and handed in: six throwaway trees would otherwise mean six `go build`s, which is
# contention in a box shared by six contributors rather than caution.
export GOWORK=off
MCBIN="$(mktemp -u "${TMPDIR:-/workspace/.olivares-tmptest}/mc-bin.XXXXXX")"
( cd "$ROOT/commercial/commerce-lint" && go build -o "$MCBIN" . ) >/dev/null 2>&1 || {
	echo "no pude construir el derivador: la bateria mediria NOT APPLICABLE" >&2; exit 2; }
export OLIVARES_MODULE_CATALOG_BIN="$MCBIN"

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/commercial/license-worker/src/catalog"
	cp "$CHECK" "$TMP/tree/scripts/check-c13-02-worker-map.sh"
	chmod +x "$TMP/tree/scripts/check-c13-02-worker-map.sh"
	# ⛔ EL FIXTURE ES AHORA EL MAPA REAL, y no veinte filas inventadas. Las de antes no llevaban
	# `schema`, `source`, `pack` ni `canon_sha256`, así que sólo podían ejercitar la comparación
	# entre copias — la comparación que estuvo VERDE sobre dos copias igualmente rancias. Con el
	# mapa real y el canon montados, el caso mide la comparación que de verdad decide.
	cp "$ROOT/commercial/module-slug-package.json" "$TMP/tree/commercial/"
	cp "$ROOT/commercial/license-worker/src/catalog/module-slug-package.json" \
		"$TMP/tree/commercial/license-worker/src/catalog/"
	cp "$ROOT/commercial/license-worker/src/catalog/slug-package.ts" \
		"$TMP/tree/commercial/license-worker/src/catalog/"
	mkdir -p "$TMP/tree/design"
	cp "$ROOT/design/PRICING-CANON.md" "$TMP/tree/design/"
	cp "$ROOT/design/c13-02-package-view.json" "$TMP/tree/design/"
	cp "$ROOT/commercial/module-package-slugs.json" "$TMP/tree/commercial/"
	cp -r "$ROOT/commercial/commerce-lint" "$TMP/tree/commercial/"
	cp "$ROOT/scripts/module-catalog-go.sh" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/module-catalog-go.sh"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c13-02-worker-map.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: identical maps is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/catalog/module-slug-package.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
assert d["entries"][0]["package"] != "enterprise/WRONG", "control de mutacion: ya estaba mutado"
d["entries"][0]["package"] = "enterprise/WRONG"
json.dump(d, open(sys.argv[1], "w", encoding="utf-8"), indent=2)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: drifted Worker copy is FAIL"
else
	bad "drift should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# ⭐ EL CASO QUE EL GATE VIEJO NO PODÍA VER, y es el defecto que se midió el 2026-09-03: las DOS
# copias envejecidas igual. Comparadas entre sí coinciden perfectamente; sólo la derivación del
# canon las desmiente. Sin este caso, la reparación no está probada.
stage
python3 - "$TMP/tree/commercial/module-slug-package.json" \
	"$TMP/tree/commercial/license-worker/src/catalog/module-slug-package.json" <<'PY'
import json, sys
for path in sys.argv[1:]:
	d = json.load(open(path, encoding="utf-8"))
	before = len(d["entries"])
	d["entries"] = [e for e in d["entries"] if e["slug"] != "caeptransmit"]
	assert len(d["entries"]) == before - 1, "control de mutacion: caeptransmit no estaba"
	json.dump(d, open(path, "w", encoding="utf-8"), indent=2)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	grep -q 'differs from the canon derivation' "$TMP/err" \
		&& ok "firing: BOTH copies stale in the same way is FAIL (the case the old gate could not see)" \
		|| bad "killed, but not by the canon derivation ($(cat "$TMP/err"))"
else
	bad "two equally stale copies stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/commercial/module-slug-package.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing source is LOOK (2)"
else
	bad "missing source should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo
echo "test-c13-02-worker-map: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
