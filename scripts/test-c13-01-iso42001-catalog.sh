#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c13-01-iso42001-catalog.sh. Both firing directions.
#
# ⛔ QUE MIDE AHORA (2026-09-05). El guion conserva el REGISTRO del 20/08 y ya no compara esa
# ausencia con el catalogo vivo, asi que el mutante «plantar iso42001 en el catalogo» ya no
# habla de el. Su sustituto es la direccion inversa: un checkout que YA tiene la fila no
# mueve el veredicto. El resto es integridad —banderas, PINES EXACTOS, la adjudicacion—.
# Ver an internal design note (not shipped)

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c13-01-iso42001-catalog.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c1301cat.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree" "$TMP/ent"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" "$TMP/ent/enterprise/activation"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c13-01-iso42001-catalog.sh"
	cp "$ROOT/design/c13-01-iso42001-catalog.json" "$TMP/tree/design/"
	cp "$ROOT/design/C13-01-ISO42001-CATALOG-HOLD-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/design/OVERLAY-FACT-GATES-ADJUDICATION-2026-09-05.md" "$TMP/tree/design/"
	# Un catalogo que YA TIENE la fila: el sujeto de la comparacion retirada.
	cat >"$TMP/ent/enterprise/activation/catalog.go" <<'EOF'
package activation

var catalog = []AddonSpec{
	{
		Key: "iso42001", Pack: PackCompliancePacks,
		Title: "ISO/IEC 42001 AIMS packs", Kind: KindWired, Disp: DispActive,
	},
}
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" OLIVARES_ENT_DIR="$TMP/ent" \
		bash "$TMP/tree/scripts/check-c13-01-iso42001-catalog.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

expect() {
	if [ "$(cat "$TMP/rc")" = "$1" ]; then
		ok "$2"
	else
		bad "$2 — got $(cat "$TMP/rc"), wanted $1 [$(tail -1 "$TMP/err")]"
	fi
}

acta_set() {
	python3 - "$TMP/tree/design/c13-01-iso42001-catalog.json" "$1" "$2" <<'PY'
import json, sys
p, k, v = sys.argv[1], sys.argv[2], sys.argv[3]
d = json.load(open(p, encoding="utf-8"))
d[k] = json.loads(v)
json.dump(d, open(p, "w", encoding="utf-8"), indent=2)
PY
}

stage
run
expect 0 "no-fire: the historical record is intact"
if grep -q 'HISTORICAL RECORD PRESERVED' "$TMP/out" && grep -q 'NOT evidence about current main' "$TMP/out"; then
	ok "the verdict says, in words, that it does not attest current main"
else
	bad "the CLEAN message must name itself historical ($(cat "$TMP/out"))"
fi

stage
run
rc_with=$(cat "$TMP/rc")
rm -rf "$TMP/ent"
run
if [ "$rc_with" = 0 ] && [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: a catalog that ALREADY lists iso42001 does not move a historical verdict"
else
	bad "the verdict depended on the neighbouring tree ($rc_with vs $(cat "$TMP/rc"))"
fi

stage
acta_set iso42001_in_catalog true
run
expect 1 "firing: iso42001_in_catalog flipped — that would rewrite what was observed"

stage
acta_set panel_executed true
run
expect 1 "firing: panel_executed flipped"

stage
acta_set overlay '"8d1720414b1356aea958002ba30f30fe2664e041"'
run
expect 1 "firing: the overlay pin was replaced with another 40-hex"

stage
acta_set hub '"0000000000000000000000000000000000000000"'
run
expect 1 "firing: the hub pin was replaced"

stage
acta_set lote '"C99-99"'
run
expect 1 "firing: the acta no longer says which lote it belongs to"

stage
echo 'iso42001 catalog landed' >>"$TMP/tree/design/C13-01-ISO42001-CATALOG-HOLD-2026-08-20.md"
run
expect 1 "firing: the historical doc claims a close the lote did not have"

stage
python3 - "$TMP/tree/design/C13-01-ISO42001-CATALOG-HOLD-2026-08-20.md" <<'PY'
import sys
p = sys.argv[1]
open(p, "w", encoding="utf-8").write(
    open(p, encoding="utf-8").read().replace("Catalog not on overlay main", ""))
PY
run
expect 1 "firing: the historical doc lost catalog-absent"

stage
python3 - "$TMP/tree/design/C13-01-ISO42001-CATALOG-HOLD-2026-08-20.md" <<'PY'
import sys
p = sys.argv[1]
open(p, "w", encoding="utf-8").write(
    open(p, encoding="utf-8").read().replace("bada7f7", "some overlay"))
PY
run
expect 1 "firing: the doc no longer names the overlay the observation was taken on"

stage
python3 - "$TMP/tree/design/OVERLAY-FACT-GATES-ADJUDICATION-2026-09-05.md" <<'PY'
import sys
p = sys.argv[1]
open(p, "w", encoding="utf-8").write(
    open(p, encoding="utf-8").read().replace(
        "daa083e56f331af6158475fc304fee633acbfc2b", "an integrated commit"))
PY
run
expect 1 "firing: the adjudication stopped naming the commit that changed the fact"

stage
rm -f "$TMP/tree/design/OVERLAY-FACT-GATES-ADJUDICATION-2026-09-05.md"
run
expect 2 "LOOK: the adjudication that supersedes the record is missing"

stage
rm -f "$TMP/tree/design/c13-01-iso42001-catalog.json"
run
expect 2 "LOOK: missing acta"

stage
run
expect 0 "no-fire: restored record stays CLEAN"

echo "check-c13-01-iso42001-catalog selftest: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
exit 0
