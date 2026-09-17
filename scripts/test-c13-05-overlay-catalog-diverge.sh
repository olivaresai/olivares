#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c13-05-overlay-catalog-diverge.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c13-05-overlay-catalog-diverge.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c1305.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree" "$TMP/ent"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/commercial" \
		"$TMP/ent/enterprise/activation"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c13-05-overlay-catalog-diverge.sh"
	cp "$ROOT/design/c13-05-overlay-catalog-diverge.json" "$TMP/tree/design/"
	cp "$ROOT/design/C13-05-OVERLAY-CATALOG-DIVERGE-2026-08-20.md" \
		"$TMP/tree/design/"
	cp "$ROOT/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" \
		"$TMP/tree/design/"
	cp "$ROOT/commercial/module-slug-package.json" "$TMP/tree/commercial/"
	# ⛔ EL FIXTURE MONTA EL ESTADO CURADO, no el defecto. Antes eran dos claves sueltas
	# (`reporting` y `credential-minter`) porque el gate EXIGÍA divergencia; con el gate exigiendo
	# igualdad, ese fixture es una divergencia real y el caso «vivo» saldría rojo con razón. El
	# catálogo se genera desde el mapa vendido para que los dos lados digan lo mismo por
	# construcción, y son los MUTANTES los que rompen esa igualdad.
	python3 - "$TMP/tree/commercial/module-slug-package.json" \
		"$TMP/ent/enterprise/activation/catalog.go" <<'PY'
import json, sys
from pathlib import Path
sold = json.load(open(sys.argv[1], encoding="utf-8"))
keys = sorted({e["slug"] for e in sold["entries"]})
assert keys, "control: el mapa vendido no trajo slugs"
body = "package activation\n\nvar Catalog = []struct{ Key string }{\n" + "".join(
    '\t{Key: "%s"},\n' % k for k in keys
) + "}\n"
Path(sys.argv[2]).write_text(body)
PY
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" OLIVARES_ENT_DIR="$TMP/ent" \
		bash "$TMP/tree/scripts/check-c13-05-overlay-catalog-diverge.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live overlay diverge HOLD is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# ⛔ EL CASO QUE HABÍA AQUÍ EXIGÍA QUE UN CATÁLOGO IGUAL AL MAPA FUERA UN FALLO. Era el estado
# fijado, no la propiedad, y con la divergencia curada convertía la cura en regresión. Se sustituye
# por las DOS direcciones de la desigualdad, que es lo que de verdad hay que cazar.

stage
# Dirección A: el overlay pierde un módulo que el mapa vende. Un comprador paga por bytes que su
# artefacto no activa.
python3 - "$TMP/ent/enterprise/activation/catalog.go" <<'PY'
import sys
from pathlib import Path
p = Path(sys.argv[1]); t = p.read_text()
assert '{Key: "iso42001"},' in t, "control de mutacion: iso42001 no estaba en el catalogo"
p.write_text(t.replace('\t{Key: "iso42001"},\n', ""))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	grep -q 'iso42001' "$TMP/err" \
		&& ok "firing: a sold module missing from the overlay catalog is FAIL, BY NAME" \
		|| bad "killed, but the message does not name iso42001 ($(cat "$TMP/err"))"
else
	bad "missing overlay key should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
# Dirección B: el overlay activa un módulo que nadie vende.
python3 - "$TMP/ent/enterprise/activation/catalog.go" <<'PY'
import sys
from pathlib import Path
p = Path(sys.argv[1]); t = p.read_text()
assert 'ghost-module' not in t, "control de mutacion: ghost-module ya estaba"
p.write_text(t.replace("}\n", '\t{Key: "ghost-module"},\n}\n'))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	grep -q 'ghost-module' "$TMP/err" \
		&& ok "firing: an overlay module nobody sells is FAIL, BY NAME" \
		|| bad "killed, but the message does not name ghost-module ($(cat "$TMP/err"))"
else
	bad "surplus overlay key should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
# Y un HOLD de empaquetado cuya condición está curada sigue escrito: teatro, y el gate lo dice.
printf '\nhold-slug: content-firewall\n' >>"$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	grep -q 'content-firewall' "$TMP/err" \
		&& ok "firing: a cured HOLD still written is FAIL, BY NAME" \
		|| bad "killed, but the message does not name content-firewall ($(cat "$TMP/err"))"
else
	bad "cured HOLD should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c13-05-overlay-catalog-diverge.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
assert d["overlay_matches_sold"] is True, "control de mutacion: ya estaba en false"
d["overlay_matches_sold"] = False
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: a record that still claims the divergence is FAIL"
else
	bad "stale divergence record should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
# El documento pierde la frase que dice QUÉ LADO iba por detrás. Sin ella la divergencia curada
# vuelve a leerse como folclore sobre «el overlay equivocado», que es justo lo que no era.
python3 - "$TMP/tree/design/C13-05-OVERLAY-CATALOG-DIVERGE-2026-08-20.md" <<'PY'
import sys
from pathlib import Path
p = Path(sys.argv[1]); t = p.read_text()
anchor = "**El lado que iba por detrás era el hub, no el overlay.**"
assert anchor in t, "control de mutacion: la frase no estaba"
p.write_text(t.replace(anchor, ""))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: the doc losing the sentence that names which side was behind is FAIL"
else
	bad "lost sentence should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/C13-05-OVERLAY-CATALOG-DIVERGE-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing HOLD doc is COULD NOT LOOK"
else
	bad "missing doc should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
unset OLIVARES_ENT_DIR || true
rc=0
OLIVARES_ROOT="$TMP/tree" \
	bash "$TMP/tree/scripts/check-c13-05-overlay-catalog-diverge.sh" \
	>"$TMP/out" 2>"$TMP/err" || rc=$?
if [ "$rc" = 2 ]; then
	ok "unset overlay dir is COULD NOT LOOK"
else
	bad "unset overlay dir should be 2 ($rc $(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: restored live stays CLEAN"
else
	bad "restored live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo "check-c13-05-overlay-catalog-diverge selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
