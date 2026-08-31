#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# CENSO de números de sesión duplicados en `origin/main` — el hueco que un gate de DIFF
# no puede cerrar nunca.
#
# `lint:session-numbers` pregunta *«¿este cambio reclama un número ya tomado?»*, y ésa es
# la pregunta correcta para un push. Pero cuando los dos lados ya han aterrizado **no hay
# cambio que mirar**, y ninguna corrida vuelve a nombrarlos. Medido el 2026-08-20 con los
# dos ficheros de un numero de sesion dentro de `origin/main`: el gate no está roto, es que su pregunta
# ya no aplica. Lo dijo otro carril y tiene razón: hace falta un censo sobre el ÁRBOL.
#
# ⛔ LO QUE ESTE GUION NO HACE, Y ES DELIBERADO: NO decide si un duplicado es una colisión.
#
# No puede, y el intento está medido. De los 52 números con más de un fichero, colapsar
# «anexos» por prefijo de slug y por subdirectorio `-encargos/` deja 37 — y entre esos 37
# hay sesiones que sencillamente escribieron seis documentos (un numero de sesion-contraste-fable5-*`)
# junto a colisiones de verdad (un numero de sesion, un numero de sesion). **El nombre del fichero no distingue un
# anexo de una colisión**, y toda regla adicional —`-INFORME`, `-RESULT`, `-mutantes`,
# `-ADJUDICACION`…— sería un vocabulario que yo me invento: un censo que sólo cuenta lo
# que le nombras no es un censo.
#
# ⇒ Por eso la pregunta de ESTE gate es otra, y sí es contestable con exactitud:
#
#        «¿ha entrado en `main` un número que ANTES no era ambiguo?»
#
# La línea base congela los que ya lo eran.
#
# ⛔⛔ Y POR ESO ESTE GUION **NO ESTÁ EN EL CARRIL RÁPIDO**, que es la decisión que más me
#     costó y la que hay que leer antes de «arreglarla» cableándolo:
#
#   1. `lint:session-numbers` ya rechazó la regla global, y su razón está escrita en su
#      `desc:` del Taskfile: *«a global uniqueness rule reports forty non-collisions and
#      gets ignored»*. Lo he vuelto a medir hoy y sale igual: **52 números con más de un
#      fichero, 37 tras colapsar anexos, y entre esos 37 hay sesiones con seis documentos
#      legítimos.** Un gate que grita cuarenta veces enseña a ignorarlo.
#   2. Y tiene un filo propio que la línea base NO cura: **una sesión que aterriza su brief
#      y su INFORME en el mismo push** hace que su número pase de cero ficheros a dos, entra
#      como «nuevo duplicado» y **enrojece un push correcto**. Un gate que se pone rojo
#      cuando haces lo normal es peor que no tenerlo.
#
#   ⇒ Esto es un **CENSO que se corre**, no una puerta que bloquea: `task census:session-
#     duplicates`. Es lo que pidió otro carril —*«un censo periódico sobre main, no un
#     gate de diff»*— y es lo único que la medida sostiene. Si algún día alguien encuentra
#     el discriminador que separa un anexo de una colisión **sin un vocabulario inventado**,
#     entonces sí se puede cablear; hasta entonces, cablearlo sería cambiar un hueco por
#     ruido, y el ruido también se ignora. Un número nuevo en la lista es un hecho, no un
# juicio: ayer ese número tenía un fichero y hoy tiene dos. Eso es exactamente lo que pasó
# con un numero de sesion y lo que nadie vio.
set -uo pipefail

BASE_REF="${OLIVARES_DUP_BASE_REF:-origin/main}"
BASELINE="${OLIVARES_DUP_BASELINE:-docs/session-number-duplicates-baseline.txt}"

die_unreadable() {
	echo "check-session-duplicates: ⛔ NO HE PODIDO MIRAR: $1" >&2
	echo "                          Un censo que no ha podido leer el árbol no es «cero" >&2
	echo "                          duplicados»: es no haber mirado." >&2
	exit 2
}

for _a in "$@"; do
	case "$_a" in
	--list) LIST=1 ;;
	*) die_unreadable "argumento desconocido: $_a" ;;
	esac
done

command -v git >/dev/null 2>&1 || die_unreadable "no encuentro git"
git rev-parse --git-dir >/dev/null 2>&1 || die_unreadable "no estoy dentro de un repositorio"
git rev-parse --verify --quiet "$BASE_REF" >/dev/null 2>&1 ||
	die_unreadable "no resuelvo '${BASE_REF}' — un ref ausente no es un árbol limpio"

RUTAS="$(git ls-tree -r --name-only "$BASE_REF" -- sessions/ 2>/dev/null)" ||
	die_unreadable "git ls-tree sobre ${BASE_REF} falló"

# CONTROL POSITIVO, y hace falta: «0 duplicados de 0 ficheros» y «0 duplicados de 749» son
# la misma frase con distinto significado, y la primera no es un verde. Si el barrido deja
# de alcanzar `sessions/` —otra ruta, otro layout— este guion tiene que decirlo, no aprobar.
TOTAL="$(printf '%s\n' "$RUTAS" | grep -c '^sessions/S[0-9]' || true)"
if [ "${TOTAL:-0}" -lt 100 ]; then
	die_unreadable "sólo ${TOTAL:-0} fichero(s) de sesión en ${BASE_REF} (esperaba cientos)"
fi

# Un número está DUPLICADO cuando dos rutas distintas lo llevan. Sin colapsar nada: ver la
# cabecera. `[a-z]?` porque existen sufijos como `S123b`.
ACTUAL="$(printf '%s\n' "$RUTAS" \
	| sed -n 's|^sessions/\(S[0-9]\{2,4\}[a-z]\?\)[-/].*|\1|p' \
	| LC_ALL=C sort | uniq -d)"

if [ "${LIST:-0}" = "1" ]; then
	printf '%s\n' "$ACTUAL"
	exit 0
fi

[ -r "$BASELINE" ] || die_unreadable "no leo la línea base ${BASELINE}"

NUEVOS="$(printf '%s\n' "$ACTUAL" | grep -vxF -f "$BASELINE" 2>/dev/null | grep . || true)"
IDOS="$(grep -vxF -f <(printf '%s\n' "$ACTUAL") "$BASELINE" 2>/dev/null | grep . || true)"
n_act="$(printf '%s\n' "$ACTUAL" | grep -c . || true)"
n_base="$(grep -c . "$BASELINE" || true)"

echo "check-session-duplicates: ${n_act} número(s) con más de un fichero en ${BASE_REF}, de ${TOTAL} fichero(s) · línea base ${n_base}"

if [ -n "$NUEVOS" ]; then
	echo "check-session-duplicates: ⛔ UN NÚMERO QUE ANTES NO ERA AMBIGUO LO ES AHORA:" >&2
	printf '%s\n' "$NUEVOS" | while IFS= read -r _n; do
		[ -n "$_n" ] || continue
		echo "  ${_n}" >&2
		printf '%s\n' "$RUTAS" | grep "^sessions/${_n}[-/]" | sed 's/^/      /' >&2
	done
	echo "check-session-duplicates:" >&2
	echo "  Si son DOS SESIONES distintas, renombra la que llegó después y arréglalo aquí," >&2
	echo "  no en main. Si es un anexo de la misma sesión, añade el número a ${BASELINE}" >&2
	echo "  EN ESTE MISMO COMMIT y di en el mensaje por qué no es una colisión." >&2
	exit 1
fi

if [ -n "$IDOS" ]; then
	echo "check-session-duplicates: ✔ $(printf '%s\n' "$IDOS" | grep -c .) resuelto(s) — baja la línea base en el mismo commit:"
	printf '%s\n' "$IDOS" | sed 's/^/    /'
	echo "  Una línea base que no baja cuando el duplicado se arregla convierte el trinquete en un techo."
fi

echo "check-session-duplicates: OK — ningún número nuevo se vuelve ambiguo."
