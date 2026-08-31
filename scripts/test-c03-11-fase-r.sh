#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for C03-11. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c03-11-fase-r.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0311.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" \
		"$TMP/tree/design" \
		"$TMP/tree/core/auth" \
		"$TMP/tree/commercial/license-worker"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c03-11-fase-r.sh"
	cp "$ROOT/design/C03-11-FASE-R-HOLD-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/core/auth/seatcap.go" "$TMP/tree/core/auth/"
	cp "$ROOT/commercial/license-worker/wrangler.jsonc" \
		"$TMP/tree/commercial/license-worker/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c03-11-fase-r.sh" \
		>/dev/null 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return "$rc"
}

stage
if run; then ok "live HOLD is CLEAN"
else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
sed -i 's/HOLD/FASE R complete — binaries replaced/' \
	"$TMP/tree/design/C03-11-FASE-R-HOLD-2026-08-20.md"
if run; then bad "completed-substitution doc stayed CLEAN"
else ok "mutant (doc claims binaries replaced) is killed"; fi

stage
python3 - "$TMP/tree/core/auth/seatcap.go" <<'PY'
import sys
from pathlib import Path
p = Path(sys.argv[1])
s = p.read_text()
old = """func (a *Authenticator) enforceSeatCapTx(_ context.Context, _ store.AuthScope) error {
	return nil
}"""
new = """func (a *Authenticator) enforceSeatCapTx(_ context.Context, _ store.AuthScope) error {
	_ = a.countSeats
	return errSeatLimit
}"""
if old not in s:
    raise SystemExit('seatcap body not found')
p.write_text(s.replace(old, new, 1))
PY
if run; then bad "capping seat seam stayed CLEAN"
else ok "mutant (enforceSeatCapTx no longer no-op) is killed"; fi

stage
python3 - "$TMP/tree/commercial/license-worker/wrangler.jsonc" <<'PY'
import sys
from pathlib import Path
p = Path(sys.argv[1])
s = p.read_text()
# Flip only the production block's fulfillment flag.
idx = s.find('"production"')
if idx < 0:
    raise SystemExit('production block missing')
head, tail = s[:idx], s[idx:]
tail = tail.replace('"FULFILLMENT_ENABLED": "false"', '"FULFILLMENT_ENABLED": "true"', 1)
p.write_text(head + tail)
PY
if run; then bad "production fulfillment on stayed CLEAN"
else ok "mutant (production fulfillment on) is killed"; fi

stage
rm -f "$TMP/tree/design/C03-11-FASE-R-HOLD-2026-08-20.md"
if run; then bad "missing HOLD doc stayed CLEAN"
else
	if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing HOLD doc is COULD NOT LOOK"
	else bad "missing doc should be 2 ($(cat "$TMP/err"))"; fi
fi

# ── LOS DOS CASOS DEL RANGO ──────────────────────────────────────────────────────
# El terminador viejo era `/^[ ]{4}\},$/`, un INTERVALO que este awk no interpreta
# (control positivo: `printf 'aaaa\n' | awk '/^a{4}$/{print}'` no imprime nada, y
# `printf 'a{4}\n'` si). El rango se abria en production y corria HASTA EL FINAL.
#
# ⚠ Un fixture con el wrangler REAL no distingue el check viejo del nuevo: el bloque
# de production tiene 56 lineas y el `head -n 40` tapaba el arrastre por casualidad.
# Lo comprobe corriendo este banco CONTRA EL CHECK VIEJO — pasaba entero. Por eso el
# fixture es COMPACTO: el entorno vecino cae dentro de las 40 lineas y el fail-open
# se ve. Un caso que no distingue no es un caso.
mini_wrangler() {   # $1 = fichero · $2 = indentacion del cierre de production
	printf '%s\n' \
		'{' \
		'  "env": {' \
		'    "sandbox": {' \
		'      "vars": {' \
		'        "FULFILLMENT_ENABLED": "true"' \
		'      }' \
		'    },' \
		'    "production": {' \
		'      "vars": {' \
		'        "FULFILLMENT_ENABLED": "true"' \
		'      }' \
		"${2}}," \
		'    "staging": {' \
		'      "vars": {' \
		'        "FULFILLMENT_ENABLED": "false"' \
		'      }' \
		'    }' \
		'  }' \
		'}' > "$1"
}

# (i) production con su cierre BIEN indentado: el rango acota, el bloque NO trae
#     staging, y como production esta en "true" el check tiene que enrojecer POR ESO
#     y no por el vecino. Con el check viejo salia CLEAN: el `false` del vecino
#     satisfacia el grep. Eso es el fail-open, y este caso es el que lo nombra.
stage
mini_wrangler "$TMP/tree/commercial/license-worker/wrangler.jsonc" "    "
if run; then
	bad "FAIL-OPEN: production en true paso porque el false era del entorno VECINO"
else
	if grep -q "production fulfillment is not the explicit off" "$TMP/err"; then
		ok "el bloque extraido NO contiene la seccion siguiente (production juzgado solo)"
	else bad "murio, pero por otro motivo: $(head -1 "$TMP/err")"; fi
fi

# (ii) y si alguien vuelve a romper el rango —aqui, indentando el cierre con SEIS
#      espacios— el check no puede quedarse callado: lo dice con su nombre.
stage
mini_wrangler "$TMP/tree/commercial/license-worker/wrangler.jsonc" "      "
if run; then bad "el rango dejo de acotar y el check salio CLEAN"
else
	if grep -q "arrastra otra seccion" "$TMP/err"; then
		ok "un cierre con otra indentacion se nombra: el rango no acota"
	else bad "no nombro el arrastre: $(head -1 "$TMP/err")"; fi
fi
stage
if run; then ok "no-fire: live HOLD stays CLEAN"
else bad "restored live should be CLEAN ($(cat "$TMP/err"))"; fi

echo "check-c03-11-fase-r selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
