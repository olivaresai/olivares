#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# REL-56 — el suelo de disco del release, ejercitado con `df` señuelo.
#
# ⛔ EXTRAE EL BLOQUE DEL FICHERO COMMITEADO, NO LO RETECLEA. Un guion que copie la lógica
# probaría su propia copia: el día que release.yml cambie, la batería seguiría verde sobre
# una lógica que ya no se envía. Aquí el sujeto es el bloque `run:` que de verdad viaja.
#
# El defecto que fija (medido 2026-08-31): un `df` que SALE 0 y no trae dígitos dejaba
# `after` vacío; el pipeline sale 0, así que ni `pipefail` ni `set -e` lo ven, y
# `[ "" -lt 20480 ]` devuelve 2 — que en la CONDICIÓN de un `if` no dispara `set -e`, se
# toma el `else` y el suelo queda satisfecho por una medida que no existe.
set -euo pipefail
ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || { echo "test-release-disk-floor: COULD NOT LOOK — cannot enter $ROOT" >&2; exit 2; }
WF="${OLIVARES_RELEASE_WF:-.github/workflows/release.yml}"
[ -r "$WF" ] || { echo "test-release-disk-floor: COULD NOT LOOK — missing $WF" >&2; exit 2; }

pass=0; fail=0
ok()  { pass=$((pass+1)); printf '  ok   %s\n' "$1"; }
bad() { fail=$((fail+1)); printf '  FAIL %s\n     %s\n' "$1" "$2"; }

TMP="$(mktemp -d "${TMPDIR:-/tmp}/rdf.XXXXXX")" || exit 2
trap 'rm -rf "$TMP"' EXIT

# El bloque `run:` del paso «make room on the runner», tal cual viaja.
awk '
  /^      - name: make room on the runner/ { paso=1; next }
  paso && /^        run: \|/              { cuerpo=1; next }
  cuerpo && /^      - name:/              { exit }
  cuerpo                                  { sub(/^          /, ""); print }
' "$WF" > "$TMP/bloque.sh"
[ -s "$TMP/bloque.sh" ] || { echo "test-release-disk-floor: COULD NOT LOOK — no extraje el bloque de $WF" >&2; exit 2; }
grep -q '20480' "$TMP/bloque.sh" || { echo "test-release-disk-floor: COULD NOT LOOK — el bloque extraido no contiene el suelo" >&2; exit 2; }

# `sudo rm -rf` fuera: la batería mide el SUELO, no el borrado. Se sustituye por un no-op
# declarado en vez de correrlo, que en una caja de desarrollo sería destructivo.
sed -i 's/^          *sudo rm -rf.*/true/; s/^ *sudo rm -rf.*/true/; /^ *\/usr\/local\/.ghcup/d' "$TMP/bloque.sh"

corre() { # $1 = cuerpo de la funcion df señuelo
	{ printf 'df() { %s; }\n' "$1"; cat "$TMP/bloque.sh"; } > "$TMP/caso.sh"
	bash "$TMP/caso.sh" >"$TMP/out" 2>&1; echo $?
}

rc=$(corre "printf 'Avail\n40000M\n'")
[ "$rc" = "0" ] && ok "por encima del suelo pasa (rc 0)" || bad "por encima del suelo pasa" "rc=$rc"

rc=$(corre "printf 'Avail\n15000M\n'")
if [ "$rc" != "0" ] && grep -q 'not enough disk to build' "$TMP/out"; then
	ok "por debajo del suelo rehusa, y dice cuanto queda"
else bad "por debajo del suelo rehusa" "rc=$rc; salida: $(head -1 "$TMP/out")"; fi

# EL CASO DE REL-56.
rc=$(corre "printf 'Avail\n-\n'")
if [ "$rc" != "0" ] && grep -q 'could not read free disk' "$TMP/out"; then
	ok "un df que SALE 0 sin digitos rehusa, y lo llama por su nombre"
else bad "un df que SALE 0 sin digitos rehusa" "rc=$rc; salida: $(head -1 "$TMP/out")"; fi

rc=$(corre "return 1")
[ "$rc" != "0" ] && ok "un df que FALLA rehusa (pipefail + set -e)" || bad "un df que FALLA rehusa" "rc=$rc"

# MUTANTE: sin la guarda de forma, el caso de REL-56 vuelve a pasar. Si no vuelve a pasar,
# el caso de arriba no estaba midiendo la guarda y su verde no valia nada.
# ⛔ EL `\x7c` ES UN `|` LITERAL PARA PERL, Y ESTA ESCRITO ASI A PROPOSITO. Con el caracter
# crudo, este patron —que es el TEXTO QUE EL MUTANTE RETIRA, no una tuberia que se ejecute—
# lo contaba `check-sigpipe-booleans` como deuda nueva y dejaba `main` en rojo en el carril
# rapido: el literal de un mutante es una SEGUNDA COPIA del hecho que parchea, y las sondas de
# texto no distinguen una de otra. Perl casa exactamente lo mismo.
perl -0pi -e 's/if ! printf .%s. "\$after" \x7c grep -qE .\^\[0-9\]\+\$.; then\n.*?\n.*?\n *fi\n//s' "$TMP/bloque.sh"
if grep -q 'could not read free disk' "$TMP/bloque.sh"; then
	bad "MUTANTE: la guarda se retira" "sigue presente; el mutante no se aplico"
else
	rc=$(corre "printf 'Avail\n-\n'")
	[ "$rc" = "0" ] && ok "MUTANTE sin la guarda: el df vacio PASA (el caso mide la guarda)" \
	                || bad "MUTANTE sin la guarda: el df vacio PASA" "rc=$rc — el caso no mide la guarda"
fi

printf '\ntest-release-disk-floor: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
