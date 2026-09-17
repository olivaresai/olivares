#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c13-01-iso42001-remeasure.sh. Both firing directions.
#
# ⛔ QUE MIDE AHORA (2026-09-05). Se conserva el REGISTRO del 20/08 y se retira la comparacion
# con el catalogo vivo del overlay. Lo que NO se retira es el mapa vendido de ESTE
# repositorio: la observacion fue justamente que el mapa nombraba el slug y el catalogo no.
# Ver an internal design note (not shipped)

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c13-01-iso42001-remeasure.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c1301rem.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree" "$TMP/ent"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" "$TMP/tree/commercial" \
		"$TMP/ent/enterprise/activation"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c13-01-iso42001-remeasure.sh"
	cp "$ROOT/design/c13-01-iso42001-remeasure.json" "$TMP/tree/design/"
	cp "$ROOT/design/C13-01-ISO42001-REMEASURE-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/design/OVERLAY-FACT-GATES-ADJUDICATION-2026-09-05.md" "$TMP/tree/design/"
	cp "$ROOT/commercial/module-slug-package.json" "$TMP/tree/commercial/"
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
		bash "$TMP/tree/scripts/check-c13-01-iso42001-remeasure.sh" \
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
	python3 - "$TMP/tree/design/c13-01-iso42001-remeasure.json" "$1" "$2" <<'PY'
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
expect 1 "firing: iso42001_in_catalog flipped"

stage
acta_set iso42001_in_sold_map false
run
expect 1 "firing: iso42001_in_sold_map flipped"

stage
acta_set u_f '"KNOWN"'
run
expect 1 "firing: u_f left UNKNOWN"

stage
acta_set u_d '"0"'
run
expect 1 "firing: u_d left UNKNOWN"

stage
acta_set schema '"c13-01-iso42001-remeasure/v2"'
run
expect 1 "firing: the schema drifted"

stage
acta_set overlay '"8d1720414b1356aea958002ba30f30fe2664e041"'
run
expect 1 "firing: the overlay pin was replaced with another 40-hex"

stage
acta_set hub '"0000000000000000000000000000000000000000"'
run
expect 1 "firing: the hub pin was replaced"

stage
python3 - "$TMP/tree/commercial/module-slug-package.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["entries"] = [e for e in d["entries"] if e.get("slug") != "iso42001"]
json.dump(d, open(p, "w", encoding="utf-8"), indent=2)
PY
run
expect 1 "firing: this repository's sold map dropped the slug, so the record is incoherent"

stage
echo 'iso42001 catalog landed' >>"$TMP/tree/design/C13-01-ISO42001-REMEASURE-2026-08-20.md"
run
expect 1 "firing: the historical doc claims a close the lote did not have"

stage
python3 - "$TMP/tree/design/C13-01-ISO42001-REMEASURE-2026-08-20.md" <<'PY'
import sys
p = sys.argv[1]
open(p, "w", encoding="utf-8").write(
    open(p, encoding="utf-8").read().replace("bada7f7", "some overlay"))
PY
run
expect 1 "firing: the doc no longer names the overlay the re-measure was taken on"

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
rm -f "$TMP/tree/commercial/module-slug-package.json"
run
expect 2 "LOOK: missing sold slug map"

stage
rm -f "$TMP/tree/design/c13-01-iso42001-remeasure.json"
run
expect 2 "LOOK: missing acta"

stage
run
expect 0 "no-fire: restored record stays CLEAN"

echo "check-c13-01-iso42001-remeasure selftest: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
exit 0
