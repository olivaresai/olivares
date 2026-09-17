#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# Bateria de scan-box-secrets.sh. El GUION lee el disco de una caja y por eso no se gatea;
# ESTA bateria es determinista —fabrica su propio arbol— y por eso si puede gatearse.
set -uo pipefail
export LC_ALL=C
ROOT="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
SUT="$ROOT/scripts/scan-box-secrets.sh"
W="$(mktemp -d)"
trap 'chmod -R u+rwX "$W" 2>/dev/null; rm -rf -- "$W"' EXIT
PASS=0; FAIL=0
ok()  { PASS=$((PASS+1)); printf 'ok   %-58s %s\n' "$1" "${2:-}"; }
bad() { FAIL=$((FAIL+1)); printf 'FAIL %-58s %s\n' "$1" "${2:-}"; }

# El valor plantado NO es un secreto real y aun asi no se imprime nunca: la propiedad que se
# prueba es justo que el guion no lo saque.
PLANTADO='AKIAEXAMPLE0000TESTVALUE1234567890abcd'

mkdir -p "$W/limpio" "$W/sucio" "$W/cache/go-build/aa"
printf 'nada que ver aqui\n' > "$W/limpio/a.txt"
printf 'basura\nCLOUDFLARE_API_TOKEN=%s\nmas basura\n' "$PLANTADO" > "$W/sucio/volcado.dmp"
printf 'CLOUDFLARE_API_TOKEN=%s\n' "$PLANTADO" > "$W/cache/go-build/aa/binario"

run() { OLIVARES_SCAN_ROOTS="$1" bash "$SUT" ${2:-} >"$W/out" 2>"$W/err"; echo $?; }

# (1) limpio -> rc 0
rc=$(run "$W/limpio")
[ "$rc" = 0 ] && ok "un arbol limpio da rc 0" || bad "un arbol limpio da rc 0" "rc=$rc"

# (2) y su informe LISTA lo que miro: un cero sin la lista es indistinguible de no mirar
grep -q "$W/limpio" "$W/out" && ok "el informe del verde NOMBRA la raiz revisada" \
	|| bad "el informe del verde NOMBRA la raiz revisada"

# (3) con secreto -> rc 1 y nombra el fichero
rc=$(run "$W/sucio")
{ [ "$rc" = 1 ] && grep -q 'volcado.dmp' "$W/out"; } \
	&& ok "un secreto plantado da rc 1 y nombra el fichero" \
	|| bad "un secreto plantado da rc 1 y nombra el fichero" "rc=$rc"

# (4) ⛔ LA PROPIEDAD QUE MAS IMPORTA: el valor NO aparece en ninguna salida.
if grep -q "$PLANTADO" "$W/out" "$W/err"; then
	bad "el valor NUNCA se imprime" "el guion filtro el secreto en su propio informe"
else
	ok "el valor NUNCA se imprime"
fi

# (5) una raiz que no existe se DICE, no se calla
rc=$(run "$W/no-existe")
{ [ "$rc" = 2 ] && grep -q 'NO EXISTE' "$W/out"; } \
	&& ok "una raiz ausente se nombra y no hay verde" \
	|| bad "una raiz ausente se nombra y no hay verde" "rc=$rc"

# (6) las cachés se excluyen POR DEFECTO y la exclusion sale impresa
rc=$(run "$W/cache")
{ [ "$rc" = 0 ] && grep -q 'exclusiones por ruta' "$W/out"; } \
	&& ok "go-build excluido por defecto Y la exclusion sale impresa" \
	|| bad "go-build excluido por defecto Y la exclusion sale impresa" "rc=$rc"

# (7) ...y --incluir-caches la levanta: si no, la exclusion seria una pared
rc=$(run "$W/cache" --incluir-caches)
[ "$rc" = 1 ] && ok "--incluir-caches levanta la exclusion" || bad "--incluir-caches levanta la exclusion" "rc=$rc"

# (8) una raiz ILEGIBLE no es un verde.
# ⛔ SE SALTA COMO ROOT, Y SE DICE: root ignora los bits de permiso, asi que `chmod 000` no
# hace ilegible nada para el. Sin esta guarda el caso se volteria en cualquier runner que
# corra como root y la pata mataria el push de TODAS las cajas por una diferencia del
# entorno, no del arbol. Un caso que no puede montar su condicion se DECLARA saltado; no se
# deja pasar como verde, que es la misma mentira por el otro lado.
mkdir -p "$W/cerrado/dentro"; printf 'x\n' > "$W/cerrado/dentro/f"; chmod 000 "$W/cerrado"
if [ "$(id -u)" -eq 0 ]; then
	printf 'skip %-58s %s\n' "una raiz ilegible no da verde" "corriendo como root: chmod no restringe"
elif [ -r "$W/cerrado" ]; then
	printf 'skip %-58s %s\n' "una raiz ilegible no da verde" "el sistema de ficheros no aplico chmod"
else
	rc=$(run "$W/cerrado")
	[ "$rc" != 0 ] && ok "una raiz ilegible no da verde" "rc=$rc" || bad "una raiz ilegible no da verde" "rc=$rc"
fi
chmod 755 "$W/cerrado" 2>/dev/null

# ⛔ (9-11) LA ETIQUETA DE VALOR. Nace de un error medido: sin ella, dos valores DISTINTOS del
# mismo nombre se leen como uno y el informe dice «N ficheros con el token» cuando son dos
# tokens — y eso cambia QUE HAY QUE ROTAR, que es la unica decision que el informe alimenta.
OTRO='BKIAEXAMPLE9999OTROVALOR0987654321zyxw'
mkdir -p "$W/dos"
printf 'CLOUDFLARE_API_TOKEN=%s\n' "$PLANTADO" > "$W/dos/a.dmp"
printf 'CLOUDFLARE_API_TOKEN=%s\n' "$PLANTADO" > "$W/dos/b.dmp"
printf 'CLOUDFLARE_API_TOKEN=%s\n' "$OTRO"     > "$W/dos/c.dmp"
run "$W/dos" >/dev/null
et_a=$(grep 'a.dmp' "$W/out" | sed -n 's/.*\[\([0-9a-f]\{12\}\)\].*/\1/p')
et_b=$(grep 'b.dmp' "$W/out" | sed -n 's/.*\[\([0-9a-f]\{12\}\)\].*/\1/p')
et_c=$(grep 'c.dmp' "$W/out" | sed -n 's/.*\[\([0-9a-f]\{12\}\)\].*/\1/p')
[ -n "$et_a" ] && ok "cada hallazgo lleva etiqueta de valor" "[$et_a]" || bad "cada hallazgo lleva etiqueta de valor"
[ -n "$et_a" ] && [ "$et_a" = "$et_b" ] && ok "el MISMO valor comparte etiqueta" \
	|| bad "el MISMO valor comparte etiqueta" "a=$et_a b=$et_b"
[ -n "$et_c" ] && [ "$et_a" != "$et_c" ] && ok "valores DISTINTOS tienen etiquetas distintas" \
	|| bad "valores DISTINTOS tienen etiquetas distintas" "a=$et_a c=$et_c"

# (12) y la etiqueta NO puede ser el valor: es sha256 truncado, no se invierte
if grep -q "$OTRO" "$W/out" "$W/err"; then
	bad "la etiqueta no revela el valor" "el segundo valor aparecio en el informe"
else
	ok "la etiqueta no revela el valor"
fi

printf '\nscan-box-secrets battery: %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
