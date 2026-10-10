#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Banco de check-sigpipe-booleans.sh.
#
# ⛔ ESTE TRINQUETE NO TENIA BANCO, y llevaba meses decidiendo pushes. Se cablea uno el 2026-08-30
# al ensanchar su sonda, porque un cambio de sonda es exactamente lo que un trinquete no puede
# verificar solo: si el patron se rompe, el censo baja, `NUEVOS` sale 0 y el gate dice **OK**. Un
# detector roto y un arbol limpio producen la misma salida, y el unico control positivo del guion
# —«cero coincidencias Y sin linea base» → 2— solo salta cuando el fichero de linea base FALTA.
# Hoy la deuda de 30 entradas hace de testigo por accidente; el dia que alguien la salde, ese
# testigo desaparece. Este banco ocupa ese sitio desde ya: planta tuberias de verdad en copias
# desechables y exige que las CACE, por nombre y en las tres formas.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GUION="$ROOT/scripts/check-sigpipe-booleans.sh"
[ -x "$GUION" ] || [ -r "$GUION" ] || { echo "CANNOT INSPECT: missing $GUION"; exit 2; }

_base="${TMPDIR:-/tmp}"
case "$_base" in "$ROOT" | "$ROOT"/*) _base=/tmp ;; esac
W="$(mktemp -d "$_base/sigb.XXXXXX")" || { echo "CANNOT INSPECT: mktemp"; exit 2; }
trap 'rm -rf "$W"' EXIT

pass=0; fail=0
ok() { printf 'ok    %-58s %s\n' "$1" "${2:-}"; pass=$((pass+1)); }
no() { printf 'FAIL  %-58s %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
dice() { case "$1" in *"$2"*) return 0 ;; esac; return 1; }   # sin tuberia: predicamos con el ejemplo

# ⛔ LAS TUBERIAS QUE ESTE BANCO PLANTA SE COMPONEN EN EJECUCION, y no es rebuscamiento: escritas
# literales, el trinquete caza a SU PROPIO BANCO y lo cuenta como deuda nueva. Es exactamente el
# defecto que su cabecera documenta para los comentarios —«el gate se caza a si mismo por
# documentarse»— cometido un nivel mas arriba, en el fichero que lo prueba. Medido al cablearlo:
# con los literales dentro, `check-sigpipe-booleans` salia rc=1 acusando a este fichero.
Q="grep -${_q:-q}"                       # nunca aparece pegado a una barra en el fuente
TUB='lista |'                            # ni la barra pegada a un grep
SH=$'#!/usr/bin/env bash\nset -uo pipefail\n'
H="he""ad"                               # la palabra, sin escribirla pegada a una barra
R="re""ad"


# Un arbol de juguete: `scripts/` con lo que le pongamos y una linea base vacia.
monta() { # $1... = lineas del guion sembrado (o ninguna)
	rm -rf "$W/t"; mkdir -p "$W/t/scripts" "$W/t/.githooks" "$W/t/docs"
	: > "$W/t/docs/sigpipe-booleans-baseline.txt"
	if [ "$#" -gt 0 ]; then
		{ echo '#!/usr/bin/env bash'; echo 'set -uo pipefail'; printf '%s\n' "$@"; } > "$W/t/scripts/sujeto.sh"
	fi
}
corre() { OUT="$(OLIVARES_CLONE="$W/t" bash "$GUION" 2>&1)"; RC=$?; }

# 1 · NO-FIRE. Sin tuberias, verde. Sin este caso, los rojos de abajo no prueban nada.
monta 'echo hola'
corre
{ [ "$RC" = 0 ] && dice "$OUT" 'the backlog has not increased'; } &&
	ok "no-fire: clean tree -> green, and REPORTS it" "rc=$RC" ||
	no "no-fire: expected 0 reporting «debt does not increase»" "rc=$RC — $OUT"

# 2 · la forma DESNUDA: la que la sonda original ya veia.
monta "$TUB $Q algo && echo si"
corre
{ [ "$RC" = 1 ] && dice "$OUT" 'backlog increased' && dice "$OUT" 'sujeto.sh: 0 -> 1'; } &&
	ok "detects the BARE form and reports the exact increase (0 -> 1)" "rc=$RC" ||
	no "bare form: expected 1 with «debt INCREASES» and «0 -> 1»" "rc=$RC — $OUT"

# 3 · ⛔ LA FORMA A LA QUE EL TRINQUETE DEL HUB ES CIEGO. Este caso es el que justifica el ensanche:
#     con el patron original salia 0 y aqui debe salir 1. En ESTE repo escondia una tuberia real en
#     `fips-verify-enterprise.sh`, que habria sobrevivido a una linea base sembrada a cero.
monta "$TUB command $Q algo && echo si"
corre
{ [ "$RC" = 1 ] && dice "$OUT" 'backlog increased' && dice "$OUT" 'sujeto.sh: 0 -> 1'; } &&
	ok "detects the form with \`command\` and reports the exact increase" "rc=$RC" ||
	no "\`command grep\`: expected 1 with «debt INCREASES» and «0 -> 1»" "rc=$RC — $OUT"
# y el CONTROL de que el ensanche sostiene peso: la sonda VIEJA no lo ve.
v="$(sed 's/^[[:space:]]*#.*$//' "$W/t/scripts/sujeto.sh" | grep -cE '(^|[^|])\| *grep +-[a-zA-Z]*q')"
[ "${v:-0}" = 0 ] && ok "control: the ORIGINAL probe did not detect it (hence its expansion)" "detected=$v" ||
	no "control: the original probe already detected it -> the expansion establishes nothing" "detected=$v"

# 4 · prefijo de entorno, la otra mitad de la ceguera.
monta "$TUB LC_ALL=C $Q algo && echo si"
corre
{ [ "$RC" = 1 ] && dice "$OUT" 'backlog increased' && dice "$OUT" 'sujeto.sh'; } &&
	ok "detects the environment-prefix form and NAMES the file" "rc=$RC" ||
	no "environment prefix: expected 1 reporting «debt INCREASES» and the file" "rc=$RC — $OUT"

# 5 · SUJETO: sin `pipefail` no hay 141 que propagar, y acusarlo seria inflar el censo.
rm -rf "$W/t"; mkdir -p "$W/t/scripts" "$W/t/.githooks" "$W/t/docs"
: > "$W/t/docs/sigpipe-booleans-baseline.txt"
{ echo '#!/usr/bin/env bash'; echo "$TUB $Q algo && echo si"; } > "$W/t/scripts/sujeto.sh"
corre
{ [ "$RC" = 0 ] && dice "$OUT" 'the backlog has not increased' && ! dice "$OUT" 'sujeto.sh'; } &&
	ok "without pipefail, NOT flagged or named" "rc=$RC" ||
	no "flags a file without pipefail (or does not report it)" "rc=$RC — $OUT"

# 6 · un COMENTARIO que documenta el defecto no es deuda. El gate se cazaba a si mismo por
#     documentarse; la leccion esta en su propia cabecera y aqui se fija como fila.
monta "# ojo: \`$TUB $Q X\` bajo pipefail devuelve 141" 'echo hola'
corre
{ [ "$RC" = 0 ] && dice "$OUT" 'the backlog has not increased' && ! dice "$OUT" 'sujeto.sh'; } &&
	ok "a comment quoting the form is NOT counted or named" "rc=$RC" ||
	no "counts a comment as a pipeline (or does not report it)" "rc=$RC — $OUT"

# 7 · un OR logico no es una tuberia: `grep -q A || grep -q B` casaba por su SEGUNDA barra.
monta "$Q A f || $Q B f"
corre
{ [ "$RC" = 0 ] && dice "$OUT" 'the backlog has not increased' && ! dice "$OUT" 'sujeto.sh'; } &&
	ok "a logical OR is NOT a pipeline and is not named" "rc=$RC" ||
	no "false positive on a logical OR (or does not report it)" "rc=$RC — $OUT"

# 8 · linea base AUSENTE con deuda REAL -> 2. Ojo: el sujeto tiene DOS caminos al 2 y hay que
#     elegir cual se prueba. Con el arbol limpio salta antes el control positivo («cero
#     coincidencias y sin linea base»), que es otra guarda; para probar ESTA hace falta que la sonda
#     encuentre algo. Un rc correcto por el camino equivocado no acredita la guarda que se nombra.
monta "$TUB $Q algo && echo si"
rm -f "$W/t/docs/sigpipe-booleans-baseline.txt"
corre
{ [ "$RC" = 2 ] && dice "$OUT" 'cannot read baseline'; } &&
	ok "missing baseline with debt -> 2 through ITS path" "rc=$RC" ||
	no "missing baseline: expected 2 reporting «cannot read»" "rc=$RC — $OUT"

# 8-bis · y el OTRO camino al 2, con su propio caso: arbol limpio Y sin linea base. Sin separarlos,
#     retirar una de las dos guardas dejaria que la otra cazara el caso y el hueco pasaria.
monta 'echo hola'
rm -f "$W/t/docs/sigpipe-booleans-baseline.txt"
corre
{ [ "$RC" = 2 ] && dice "$OUT" 'no matches'; } &&
	ok "clean tree without a baseline -> 2 through the positive control" "rc=$RC" ||
	no "positive control: expected 2 reporting «zero matches»" "rc=$RC — $OUT"

# 9 · y la GRIETA que este banco cubre, fijada como fila para que no se olvide: con la linea base
#     VACIA y cero coincidencias, el guion dice OK — que es correcto para un arbol limpio e
#     indistinguible de una sonda rota. El control positivo del guion no salta porque el fichero SI
#     existe. Por eso los casos 2, 3 y 4 son obligatorios: son el control positivo de verdad.
monta 'echo hola'
corre
{ [ "$RC" = 0 ] && dice "$OUT" 'baseline 0'; } &&
	ok "empty baseline + clean tree -> OK (declared gap)" "rc=$RC" ||
	no "the gap no longer behaves as documented" "rc=$RC — $OUT"

# ══════════════════════════════════════════════════════════════════════════════════════════════
# LA LINEA BASE SE VALIDA POR FORMA — y el momento en que esto importa es el de la INTEGRACION.
#
# ⛔ Medido el 2026-08-30 antes de curarlo: con el fichero en CONFLICTO el parser trataba
# `<<<<<<<` y `=======` como filas de datos y el gate salia **rc 0 diciendo que la deuda BAJABA**,
# con una entrada cuyo nombre era la cadena vacia. Y ese estado no es raro: es exactamente el que
# tiene el integrador **mientras resuelve el conflicto de este mismo fichero**, que es cuando el
# gate mas falta hace. Cada fila exige el rc Y el mensaje: un 2 por el motivo equivocado es
# indistinguible de uno correcto.
base_mala() { # $1 = contenido de la linea base (con \n) · deja OUT y RC
	rm -rf "$W/t"; mkdir -p "$W/t/scripts" "$W/t/.githooks" "$W/t/docs"
	{ echo '#!/usr/bin/env bash'; echo 'set -uo pipefail'; echo 'echo hola'; } > "$W/t/scripts/sujeto.sh"
	printf '%b' "$1" > "$W/t/docs/sigpipe-booleans-baseline.txt"
	OUT="$(OLIVARES_CLONE="$W/t" bash "$GUION" 2>&1)"; RC=$?
}
fila_mala() { # $1 etiqueta · $2 contenido · $3 trozo que debe aparecer en el mensaje
	base_mala "$2"
	if [ "$RC" != 2 ]; then
		no "$1: expected 2, got $RC — $(printf '%s' "$OUT" | tail -1)"
	elif ! dice "$OUT" "$3"; then
		no "$1: rc 2 but the message does not name «$3» — $(printf '%s' "$OUT" | tail -1)"
	elif dice "$OUT" 'backlog decreased' || dice "$OUT" 'the backlog has not increased'; then
		no "$1: rc 2 but ALSO gives a debt verdict — cannot judge what it has not read"
	else
		ok "$1" "rc=2"
	fi
}
# el testigo que motiva todo: el fichero a medio resolver
fila_mala "baseline IN CONFLICT -> 2, does NOT claim that debt decreases" \
	'1\tscripts/otro.sh\n<<<<<<< HEAD\n4\tscripts/sujeto.sh\n=======\n3\tscripts/sujeto.sh\n>>>>>>> rama\n' \
	'<<<<<<< HEAD'
# y las otras formas de basura, cada una nombrada en el mensaje
fila_mala "EMPTY path -> 2"                 '0\t\n'                      '0	'
fila_mala "NON-integer count -> 2"           'abc\tscripts/dos.sh\n'      'abc'
fila_mala "space instead of tab -> 2" '1 scripts/dos.sh\n'         '1 scripts/dos.sh'
fila_mala "three fields -> 2"                '1\tscripts/a.sh\tsobra\n'  'sobra'
# ⛔ NO-FIRE de esta guarda: una linea base VALIDA no se rechaza. Sin esta fila, un validador que
# rechazara TODO pasaria las cinco de arriba.
base_mala '2\tscripts/uno.sh\n1\tscripts/dos.sh\n'
{ [ "$RC" = 0 ] && dice "$OUT" 'the backlog has not increased'; } &&
	ok "no-fire: a VALID baseline is not rejected" "rc=$RC" ||
	no "validation no-fire: expected 0 saying «the debt does not increase»" "rc=$RC — $OUT"

# Permanent regressions for form 2 and comparison direction (the reviewer A-01/m2 and A-02).
# The three mutants returning red for 5 → 4, counting no m2 findings, and dropping
# --quiet all passed the old 11/0 suite. These cases distinguish those exact variants.
sujeto_m2() { # $1 = cuerpo del guion sembrado
	rm -rf "$W/t"; mkdir -p "$W/t/scripts" "$W/t/.githooks" "$W/t/docs"
	: > "$W/t/docs/sigpipe-booleans-baseline.txt"
	{ echo '#!/usr/bin/env bash'; echo 'set -uo pipefail'; printf '%b\n' "$1"; } > "$W/t/scripts/sujeto.sh"
	OUT="$(OLIVARES_CLONE="$W/t" bash "$GUION" 2>&1)"; RC=$?
}
# POSITIVA: la asignacion cuyo rc SE PRUEBA, con el consumidor justo tras la barra.
# ⛔ El consumidor de esta fila es `head`, NO `grep -q`, y es deliberado: `grep -q` casa TAMBIEN la
# forma 1, asi que una siembra con el cuenta DOS y no aisla lo que dice aislar (medido: «0 -> 2»).
# Un fixture que dispara dos formas no acredita ninguna de las dos.
sujeto_m2 'Z="$(cat f | '"$H"' -1)" || return 1'
{ [ "$RC" = 1 ] && dice "$OUT" 'sujeto.sh: 0 -> 1'; } &&
	ok "m2 POSITIVE: assignment with a consumer after the pipe is counted" "rc=$RC" ||
	no "m2 positive: expected 1 with «0 -> 1»" "rc=$RC — $OUT"
# NEGATIVAS: lectores COMPLETOS que llevan la palabra dentro de sus comillas. No cierran pronto y
# no deben contar. Sin estas dos filas, delimitar el comando no queda acreditado.
sujeto_m2 'X="$(cat f | sed "s/'"$H"'/x/")" || return 1'
{ [ "$RC" = 0 ] && dice "$OUT" 'the backlog has not increased'; } &&
	ok "m2 NEGATIVE: \`sed\` containing the word is NOT counted" "rc=$RC" ||
	no "m2 negative (sed): expected 0 — the word inside quotes is not a command" "rc=$RC — $OUT"
sujeto_m2 'Y="$(cat f | awk "/'"$R"'/{print}")" || true'
{ [ "$RC" = 0 ] && dice "$OUT" 'the backlog has not increased'; } &&
	ok "m2 NEGATIVE: \`awk\` containing the word is NOT counted" "rc=$RC" ||
	no "m2 negative (awk): expected 0" "rc=$RC — $OUT"
# la forma LARGA del consumidor booleano, que hoy no usa nadie y por eso necesita testigo
sujeto_m2 "$TUB grep --quiet algo && echo si"
{ [ "$RC" = 1 ] && dice "$OUT" 'sujeto.sh: 0 -> 1'; } &&
	ok "\`--quiet\` is counted the same as \`-q\`" "rc=$RC" ||
	no "--quiet: expected 1 with «0 -> 1»" "rc=$RC — $OUT"

# ⛔ EL SENTIDO DE LA COMPARACION, sobre la MISMA ruta y en las dos direcciones. Sin estas filas un
# trinquete que pusiera rojo a quien reduce deuda —el defecto que A-02 vino a cerrar— pasaria.
base_y_censo() { # $1 = numero en la linea base · el arbol siembra SIEMPRE 2 tuberias
	rm -rf "$W/t"; mkdir -p "$W/t/scripts" "$W/t/.githooks" "$W/t/docs"
	printf '%s\tscripts/sujeto.sh\n' "$1" > "$W/t/docs/sigpipe-booleans-baseline.txt"
	{ echo '#!/usr/bin/env bash'; echo 'set -uo pipefail'
	  printf '%s algo && echo a\n' "$TUB $Q"; printf '%s otro && echo b\n' "$TUB $Q"; } > "$W/t/scripts/sujeto.sh"
	OUT="$(OLIVARES_CLONE="$W/t" bash "$GUION" 2>&1)"; RC=$?
}
base_y_censo 5
{ [ "$RC" = 0 ] && dice "$OUT" 'backlog decreased' && dice "$OUT" 'sujeto.sh: 5 -> 2'; } &&
	ok "SAME path 5 -> 2: DECREASES and is GREEN" "rc=$RC" ||
	no "5 -> 2: expected 0 reporting «debt DECREASES … 5 -> 2»" "rc=$RC — $OUT"
base_y_censo 1
{ [ "$RC" = 1 ] && dice "$OUT" 'backlog increased' && dice "$OUT" 'sujeto.sh: 1 -> 2'; } &&
	ok "SAME path 1 -> 2: INCREASES and is RED" "rc=$RC" ||
	no "1 -> 2: expected 1 reporting «debt INCREASES … 1 -> 2»" "rc=$RC — $OUT"

# ⛔ Y LA DESAPARICION DE LA ULTIMA RUTA, que es adonde este repo quiere llegar: sin la guarda de la
# linea vacia se contaban DOS bajas y se imprimia una fila fantasma con la ruta en blanco.
rm -rf "$W/t"; mkdir -p "$W/t/scripts" "$W/t/.githooks" "$W/t/docs"
printf '1\tscripts/sujeto.sh\n' > "$W/t/docs/sigpipe-booleans-baseline.txt"
{ echo '#!/usr/bin/env bash'; echo 'set -uo pipefail'; echo 'echo hola'; } > "$W/t/scripts/sujeto.sh"
OUT="$(OLIVARES_CLONE="$W/t" bash "$GUION" 2>&1)"; RC=$?
{ [ "$RC" = 0 ] && dice "$OUT" 'decreased by 1 entry' && dice "$OUT" 'sujeto.sh: 1 -> 0' \
	&& ! dice "$OUT" ': 0 -> 0'; } &&
	ok "LAST path disappears: 1 decrease, no phantom row" "rc=$RC" ||
	no "disappearance: expected 0 with ONE decrease and without «: 0 -> 0»" "rc=$RC — $OUT"

# ══════════════════════════════════════════════════════════════════════════════════════════════
# A-05 y A-06 · el consumidor es un COMANDO, y las comillas mandan.
#
# ⛔ Los dos casos son de the reviewer y los dos rompian un patron por linea, que no distingue un comando
# de la misma palabra dentro de unas comillas. El banco anterior solo negaba la palabra dentro de
# los ARGUMENTOS de `sed`/`awk`, asi que 24/0 no discriminaba ninguno de estos dos.
#   A-05 (falso POSITIVO): una tuberia escrita como TEXTO no ejecuta nada y no debe contar.
#   A-06 (falso NEGATIVO): `builtin read` SI ejecuta la tuberia — la forma 1 admitia `builtin` y la
#                          forma 2 no: dos ramas del mismo guion con reglas distintas.
sujeto_m2 'A="$(printf '"'"'%s\\n'"'"' '"'"'cat f | '"$H"' -1'"'"')" || return 1'
{ [ "$RC" = 0 ] && dice "$OUT" 'the backlog has not increased'; } &&
	ok "A-05: a pipeline in SINGLE quotes is text and is not counted" "rc=$RC" ||
	no "A-05: expected 0 — quoted text executes nothing" "rc=$RC — $OUT"
sujeto_m2 'D="$(printf "%s" "cat f | '"$H"' -1")" || return 1'
{ [ "$RC" = 0 ] && dice "$OUT" 'the backlog has not increased'; } &&
	ok "A-05: DOUBLE quotes also do not count" "rc=$RC" ||
	no "A-05 (double quotes): expected 0" "rc=$RC — $OUT"
sujeto_m2 'B="$(yes x | builtin '"$R"' -r y)" || return 1'
{ [ "$RC" = 1 ] && dice "$OUT" 'sujeto.sh: 0 -> 1'; } &&
	ok "A-06: \`builtin read\` DOES count (form 1 already accepted it)" "rc=$RC" ||
	no "A-06: expected 1 with «0 -> 1» — builtin is a prefix, not another command" "rc=$RC — $OUT"
# ⛔ Y el `||` NO es una tuberia, aunque lleve el consumidor detras: sin esta fila, un analizador
# que tratara `||` como barra contaria de mas y las tres de arriba seguirian pasando.
sujeto_m2 'E="$(cat f || '"$H"' -1)" || return 1'
{ [ "$RC" = 0 ] && dice "$OUT" 'the backlog has not increased'; } &&
	ok "\`||\` followed by a consumer is NOT a pipeline" "rc=$RC" ||
	no "OR was counted as a pipeline" "rc=$RC — $OUT"

printf '\ntest-sigpipe-booleans: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" = 0 ] || exit 1
