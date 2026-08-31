#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Banco de `check-hook-leg-wiring.sh`.
#
# ⛔ DOS DE ESTOS CASOS SON FALSOS POSITIVOS MIOS, y estan aqui a proposito. Al construir la sonda
# marque como ausentes dos patas que SI estaban, y las dos veces la culpa era de la FORMA del
# extractor, no del arbol:
#
#   · exigir ' + ' con espacios para reconocer una linea del banner — **una linea del banner acaba
#     en '+' sin espacio detras**, asi que `channel-parity` salia ausente estando en la 465;
#   · comparar solo contra FAST_LINTS — las patas del GATE PESADO se declaran en HEAVY_TASKS, asi
#     que `format-ratchet`, `guide-docs` y `raw-palette` salian ausentes estando declaradas.
#
# En un canario un falso positivo cuesta tanto como un falso negativo: apunta a un carril ajeno. Por
# eso los dos tienen caso propio, y por eso el banco lleva ademas un MUTANTE: si alguien neutraliza
# la comparacion, esto tiene que morir.
set -uo pipefail

GATE="$(cd "$(dirname "$0")" && pwd)/check-hook-leg-wiring.sh"
OK=0
MAL=0

paso() {
	OK=$((OK + 1))
	printf '  ok    %s\n' "$1"
}
malo() {
	MAL=$((MAL + 1))
	printf '  FAIL  %s\n' "$1"
}

T="$(mktemp -d)" || { echo "test-hook-leg-wiring: NO HE PODIDO MIRAR: sin mktemp" >&2; exit 2; }
trap 'rm -rf "$T"' EXIT

# --- fixtures -----------------------------------------------------------------------------------
# Un gancho de mentira con tres patas, y un banco que declara las mismas. La ultima linea del banner
# acaba en '+' SIN espacio detras, que es la forma que me engaño en el arbol real.
gancho() {
	cat >"$T/gancho" <<EOF
#!/usr/bin/env bash
# task lint:esto-es-prosa-y-no-cuenta
echo "pre-push: FAST lints (uno + dos +"
echo "pre-push:   tres +"
gate_heavy_list="pesada +"
task lint:uno
OLIVARES_X=1 task lint:dos
task lint:tres
${1:-}
task lint:pesada
EOF
}
banco() {
	cat >"$T/banco" <<EOF
FAST_LINTS=(
	lint:uno lint:dos lint:tres ${1:-}
)
HEAVY_TASKS=(
	lint:pesada
)
EOF
}
corre() { OLIVARES_WIRING_HOOK="$T/gancho" OLIVARES_WIRING_BENCH="$T/banco" bash "$GATE" 2>&1; }

# --- 1 · el caso limpio -------------------------------------------------------------------------
gancho; banco
SAL="$(corre)"; RC=$?
[ "$RC" = 0 ] && case "$SAL" in *limpio*) paso "arbol coherente: sale 0 y lo dice" ;; *) malo "arbol coherente: rc 0 pero no dice limpio" ;; esac
[ "$RC" = 0 ] || malo "arbol coherente: esperaba rc 0 y salio $RC"

# --- 2 · el falso positivo del BANNER que acaba en '+' sin espacio ------------------------------
case "$SAL" in
*lint:tres*) malo "REGRESION: 'tres' esta en un banner que acaba en '+' sin espacio y lo da por ausente" ;;
*) paso "una linea de banner que acaba en '+' SIN espacio detras cuenta como anuncio" ;;
esac

# --- 3 · el falso positivo de HEAVY_TASKS -------------------------------------------------------
case "$SAL" in
*lint:pesada*) malo "REGRESION: 'pesada' se declara en HEAVY_TASKS y la da por ausente" ;;
*) paso "una pata declarada en HEAVY_TASKS cuenta como declarada" ;;
esac

# --- 4 · falta en la LISTA del banco ------------------------------------------------------------
gancho "task lint:nueva"
banco
cat >>"$T/gancho" <<'EOF'
EOF
# la anuncio en el banner pero NO la declaro en el banco
sed -i 's/^echo "pre-push:   tres +"$/echo "pre-push:   tres + nueva +"/' "$T/gancho"
SAL="$(corre)"; RC=$?
[ "$RC" = 1 ] || malo "falta en la lista: esperaba rc 1 y salio $RC"
case "$SAL" in
*"lint:nueva"*"lista declarada"*) paso "falta en la lista del banco: la NOMBRA y dice que sitio le falta" ;;
*) malo "falta en la lista del banco: no la nombra o no dice donde" ;;
esac
case "$SAL" in
*"el banner del gancho"*) malo "falta SOLO en la lista y ademas acusa al banner, donde si esta" ;;
*) paso "no acusa al banner, donde la pata SI esta anunciada" ;;
esac

# --- 5 · falta en el BANNER ---------------------------------------------------------------------
gancho "task lint:nueva"
banco "lint:nueva"
SAL="$(corre)"; RC=$?
[ "$RC" = 1 ] || malo "falta en el banner: esperaba rc 1 y salio $RC"
case "$SAL" in
*"lint:nueva"*"banner"*) paso "falta en el banner: la NOMBRA y dice que sitio le falta" ;;
*) malo "falta en el banner: no la nombra o no dice donde" ;;
esac

# --- 6 · falta en LOS DOS, que es el defecto real de 8f2edda97 ----------------------------------
gancho "task lint:nueva"
banco
SAL="$(corre)"; RC=$?
[ "$RC" = 1 ] || malo "falta en los dos: esperaba rc 1 y salio $RC"
case "$SAL" in
*"lint:nueva"*"banner"*"y en"*"lista declarada"*) paso "falta en los DOS sitios: los nombra los dos" ;;
*) malo "falta en los dos: no nombra ambos sitios" ;;
esac

# --- 6-bis · una pata citada SOLO en un COMENTARIO de la lista NO cuenta como declarada -------
# Esto es un falso NEGATIVO que tuve y arregle: al leer el bloque entero como texto, los comentarios
# de dentro metian su prosa en el universo, asi que un nombre mencionado de pasada valia por una
# declaracion. Un falso negativo en un gate es peor que un falso positivo: no se ve.
gancho "task lint:nueva"
cat >"$T/banco" <<'EOF'
FAST_LINTS=(
	# lint:nueva la cite aqui de pasada, en prosa, y eso NO es declararla
	lint:uno lint:dos lint:tres
)
HEAVY_TASKS=(lint:pesada)
EOF
sed -i 's/^echo "pre-push:   tres +"$/echo "pre-push:   tres + nueva +"/' "$T/gancho"
SAL="$(corre)"; RC=$?
[ "$RC" = 1 ] || malo "pata citada en un comentario: esperaba rc 1 y salio $RC"
case "$SAL" in
*"lint:nueva"*"lista declarada"*) paso "una pata citada solo en un COMENTARIO no cuenta como declarada" ;;
*) malo "REGRESION: cuenta como declarada una pata que solo aparece en un comentario" ;;
esac

# --- 6-ter · un array de UNA linea se lee entero ------------------------------------------------
# El otro filo del mismo arreglo: leer solo desde la linea siguiente a `X=(` se saltaba HEAVY_TASKS
# entera, y entonces las patas pesadas salian ausentes estando declaradas.
gancho; banco
case "$(corre)" in
*lint:pesada*) malo "REGRESION: no lee un array declarado en UNA sola linea" ;;
*) paso "un array escrito en una sola linea se lee entero" ;;
esac

# --- 7 · fail-closed: fichero ilegible ----------------------------------------------------------
SAL="$(OLIVARES_WIRING_HOOK="$T/no-existe" OLIVARES_WIRING_BENCH="$T/banco" bash "$GATE" 2>&1)"; RC=$?
[ "$RC" = 2 ] || malo "gancho ilegible: esperaba rc 2 y salio $RC"
case "$SAL" in
*"NO HE PODIDO MIRAR"*) paso "gancho ilegible: NO HE PODIDO MIRAR y rc 2, no un verde" ;;
*) malo "gancho ilegible: no dice NO HE PODIDO MIRAR" ;;
esac

# --- 8 · fail-closed: los extractores no casan nada ---------------------------------------------
# Este es el caso que separa «no hay incumplidores» de «no he sabido mirar»: un gancho reformateado
# daria CERO hallazgos y saldria verde si esta guarda no estuviera.
printf 'nada que se parezca a una llamada\n' >"$T/gancho"
SAL="$(corre)"; RC=$?
[ "$RC" = 2 ] || malo "extractores en vacio: esperaba rc 2 y salio $RC"
case "$SAL" in
*"NO HE PODIDO MIRAR"*"llamadas=0"*) paso "cero llamadas casadas: NO HE PODIDO MIRAR con el conteo, no verde" ;;
*) malo "cero llamadas casadas: no lo declara como no-he-podido-mirar" ;;
esac

# --- 9 · MUTANTE: si se neutraliza la comparacion, esto tiene que morir --------------------------
# Se corre una COPIA con la comparacion del banner desactivada; el caso 5 debe dejar de cazarse.
MUT="$T/mutante.sh"
sed 's/^sin_banner = .*/sin_banner = []/' "$GATE" >"$MUT"
if ! cmp -s "$GATE" "$MUT"; then
	gancho "task lint:nueva"
	banco "lint:nueva"
	if OLIVARES_WIRING_HOOK="$T/gancho" OLIVARES_WIRING_BENCH="$T/banco" bash "$MUT" >/dev/null 2>&1; then
		paso "MUTANTE 'sin_banner vacio' deja pasar el caso 5 ⇒ ese caso ACREDITA la comparacion del banner"
	else
		malo "MUTANTE 'sin_banner vacio' sigue cazando: el caso 5 no acredita lo que dice acreditar"
	fi
else
	malo "NO HE PODIDO MIRAR: el mutante salio identico al original — el sed ya no casa"
fi

printf 'hook-leg-wiring selftest: %d passed, %d failed\n' "$OK" "$MAL"
[ "$MAL" = 0 ]
