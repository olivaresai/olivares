#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-baseline-shrink.sh — si un push QUITA entradas de una linea base de trinquete, tiene que tocar
# tambien algo de lo que quita. Un commit que SOLO borra lineas es indistinguible de silenciar el gate.
#
# ⛔ POR QUE EXISTE, medido el 2026-08-18 y pagado con `main` rojo para los cinco carriles.
# `docs/translation-drift-baseline.txt` aterrizo con 75 entradas. Una rama commiteo despues
# «tighten the drift baseline from 75 to 35»: **UN solo fichero, 40 lineas menos, CERO traducciones
# tocadas**. Al integrarla, `main` perdio 40 entradas aceptadas y `lint:translation-drift` enrojecio
# **por ficheros que nadie habia tocado**. Verificado: las 40 seguian en deriva ese mismo dia.
#
# LO QUE HACE ESPECIALMENTE CARO A ESTE FALLO es CUANDO se nota: en la rama, el gate esta verde —la
# base y el arbol concuerdan ahi—. Enrojece **al aterrizar**, cuando ya bloquea a todos, y el ultimo
# que lo tocó no es quien lo paga. Por eso se comprueba en el push y no despues.
#
# QUE NO HACE, a proposito: no juzga si la entrada MERECIA salir — eso exige leer el sujeto de cada
# trinquete y es de quien lo posee. Solo exige que el commit que la quita **haya tocado algo de lo que
# quita**, que es la diferencia mecanica entre hacer el trabajo y borrar la linea.
#
# TRES RESPUESTAS: 0 sin encogimiento sospechoso · 1 lineas quitadas sin tocar su sujeto · 2 no pude mirar.
set -uo pipefail
LC_ALL=C
export LC_ALL

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || { echo "check-baseline-shrink: ⛔ NO HE PODIDO MIRAR: no puedo cargar $_olivares_git_env" >&2; exit 2; }
unset _olivares_git_env

command -v git >/dev/null 2>&1 || { echo "check-baseline-shrink: ⛔ NO HE PODIDO MIRAR: no hay git." >&2; exit 2; }
ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
[ -n "$ROOT" ] || { echo "check-baseline-shrink: ⛔ NO HE PODIDO MIRAR: fuera de un arbol git." >&2; exit 2; }
cd "$ROOT" || exit 2

TRUNK="${OLIVARES_BASELINE_TRUNK:-origin/main}"
git rev-parse --verify --quiet "$TRUNK" >/dev/null 2>&1 \
	|| { echo "check-baseline-shrink: ⛔ NO HE PODIDO MIRAR: no hay '$TRUNK' contra el que comparar." >&2; exit 2; }
BASE="$(git merge-base "$TRUNK" HEAD 2>/dev/null || true)"
[ -n "$BASE" ] || { echo "check-baseline-shrink: ⛔ NO HE PODIDO MIRAR: sin base comun con $TRUNK." >&2; exit 2; }

# Las lineas base VIGILADAS son listas de rutas, una por linea. Enumeradas a proposito: un fichero
# nuevo se anade aqui adrede, y un `find` amplio arrastraria allowlists YAML cuyo formato no es este.
BASELINES="${OLIVARES_BASELINES:-docs/translation-drift-baseline.txt web/format-ratchet-baseline.txt docs/phone-home-claims-baseline.txt docs/sigpipe-booleans-baseline.txt docs/list-truncation-baseline.txt}"

vistas=0
hallazgos=0
for b in $BASELINES; do
	git cat-file -e "$BASE:$b" 2>/dev/null || continue   # no existia en la base: nada que comparar
	[ -f "$b" ] || continue
	vistas=$((vistas + 1))
	antes="$(git show "$BASE:$b" 2>/dev/null | grep -v '^\s*#' | grep -c . || true)"
	ahora="$(grep -v '^\s*#' <"$b" | grep -c . || true)"
	[ "${ahora:-0}" -lt "${antes:-0}" ] || continue

	quitadas="$(comm -23 \
		<(git show "$BASE:$b" 2>/dev/null | grep -v '^\s*#' | grep . | LC_ALL=C sort -u) \
		<(grep -v '^\s*#' <"$b" | grep . | LC_ALL=C sort -u) 2>/dev/null)"
	n_quit="$(printf '%s\n' "$quitadas" | grep -c . || true)"
	[ "${n_quit:-0}" -gt 0 ] || continue

	# ¿Toca el push ALGO de lo que quita? Las entradas son rutas relativas a alguna raiz, asi que se
	# compara por SUFIJO: `de/index.mdx` casa con `docs-site/src/content/docs/de/index.mdx`.
	tocados="$(git diff --name-only "$BASE"...HEAD 2>/dev/null || true)"
	con_trabajo=0
	sin_trabajo=""
	while IFS= read -r q; do
		[ -n "$q" ] || continue
		# ⛔ SIN TUBERÍA, y no es estilo: `printf … | grep -q` sale **141 CUANDO ACIERTA** bajo
		# `pipefail` —`grep -q` cierra en la primera coincidencia y le manda SIGPIPE al `printf`—.
		# Aquí ese 141 se llevó por delante el carril rápido de los CINCO carriles durante dos horas,
		# y lo cazó `lint:sigpipe-booleans`, que existe exactamente para esto. Escribí el defecto que
		# mi propia memoria tiene anotado como regla. Un here-string usa los mismos bytes y no crea un
		# segundo proceso al que señalar.
		# ⛔ LA RUTA, NO LA LINEA. Una linea base no siempre es una lista de rutas peladas:
		# docs/sigpipe-booleans-baseline.txt lleva `<cuenta><TAB><ruta>`, y usar la linea
		# entera producia el regex `(^|/)3\tscripts/check-disk-headroom\.sh$`, que NO PUEDE
		# casar con ninguna salida de `git diff --name-only`. Consecuencia medida el
		# 2026-08-19: los dos trinquetes se volvian MUTUAMENTE INSATISFACIBLES —
		# `lint:sigpipe-booleans` exige «baja la linea base en el mismo commit» y este gate
		# rechazaba ese commit siempre, tocara lo que tocara. Y no se veia nunca, porque
		# mientras nadie quita una entrada `n_quit` es 0 y este bloque ni se ejecuta: el
		# defecto solo aparece en el unico momento en que el gate hace falta.
		_ruta="${q##*$'\t'}"
		_re="(^|/)$(printf '%s' "$_ruta" | sed 's/[][\.*^$+?(){}|\/]/\\&/g')$"
		_toca=0
		if grep -qE "$_re" <<<"$tocados"; then
			_toca=1
		else
			# ⛔ CUARTA FORMA DE LA INSATISFACIBILIDAD, y se cierra ANTES de que muerda porque la
			# cabecera ya ensena que este defecto solo aparece el dia en que el gate hace falta.
			# Una linea base no siempre lista RUTAS: `docs/list-truncation-baseline.txt` lista
			# NOMBRES DE FEATURE (`compliance`, `knowledge`), porque su censo es por feature. Con
			# el casamiento por sufijo de arriba, `(^|/)compliance$` NO PUEDE casar con
			# `web/src/features/compliance/api.ts` — ninguna salida de `git diff --name-only`
			# termina en el nombre de un directorio. Sin esta rama, quitar una entrada seria
			# SIEMPRE «sin trabajo detras» y este gate rechazaria la retirada legitima tocara lo
			# que tocara, que es exactamente lo que le paso a `sigpipe-booleans` dos veces.
			#
			# La pregunta que el gate hace es «¿toca el push algo de lo que quita?», y para un
			# nombre de feature la respuesta honesta es: tocar algo DENTRO de su directorio. No
			# afloja nada — sigue exigiendo trabajo en el sujeto retirado; solo sabe leer la
			# entrada. Se exige que el directorio EXISTA para no aceptar un nombre inventado.
			# ⛔ ACOTADA AL BASELINE QUE LA NECESITA, y no es celo: el bucle recorre CINCO
			# lineas base y sin este `case "$b"` cualquier entrada desnuda de otra adquiria
			# semantica de feature por el mero hecho de no llevar barra. Hoy no hay colision,
			# pero el parser aceptaba el estado peligroso. Lo nombro el contraste `sol max`
			# del 2026-08-27 (hallazgo E).
			#
			# Y la GRAMATICA CERRADA por lo mismo: el escapado de arriba ya cubre la sintaxis
			# ERE, pero un nombre que no case con esto no es un directorio de features nuestro
			# y no tiene por que llegar a construir un patron. Dos capas, y la barata primero.
			#
			# ⛔ EN UNA VARIABLE APARTE, y este descuido casi entra: `_ruta` se REUTILIZA mas
			# abajo para nombrar la entrada en `sin_trabajo`. Vaciarla aqui habria dejado el
			# informe de las otras cuatro lineas base con una linea en blanco donde va el
			# nombre — una permisividad invisible, que es justo lo que esta cabecera combate.
			_nombre=""
			case "$b" in
			*list-truncation-baseline.txt) _nombre="$_ruta" ;;
			esac
			case "$_nombre" in
			'' | */* | *[!a-z0-9-]* | -*) ;;
			*)
				if [ -d "web/src/features/$_nombre" ]; then
					_dir_re="(^|/)web/src/features/$(printf '%s' "$_nombre" | sed 's/[][\.*^$+?(){}|\/]/\\&/g')/"
					# Here-string, no tuberia: `grep -q` sale 141 CUANDO ACIERTA detras de un pipe.
					if grep -qE "$_dir_re" <<<"$tocados"; then _toca=1; fi
				fi
				;;
			esac
		fi
		if [ "$_toca" = "1" ]; then
			con_trabajo=$((con_trabajo + 1))
		else
			sin_trabajo="${sin_trabajo}${_ruta}
"
		fi
	done <<EOF_Q
$quitadas
EOF_Q

	echo "check-baseline-shrink: $b · antes=$antes ahora=$ahora · quitadas=$n_quit · con trabajo en el push=$con_trabajo"
	# El veredicto NO cambia: este gate exige, por diseno declarado en su cabecera, que el
	# commit toque ALGO de lo que quita, no todo. Pero hasta hoy las entradas SIN trabajo
	# detras no se nombraban, asi que un commit con una retirada legitima podia arrastrar
	# otras sin que nadie lo viera. Una permisividad elegida se defiende; una invisible se
	# sufre. Se nombran; quien lea decide.
	if [ -n "$sin_trabajo" ] && [ "$con_trabajo" -gt 0 ]; then
		echo "  nota: $b pierde entrada(s) que este push NO toca (permitido, pero visible):"
		printf '%s' "$sin_trabajo" | sed 's/^/    /'
	fi
	# ── TERCERA FORMA de la insatisfacibilidad, medida el 2026-08-20 ───────────────
	# Una entrada tambien desaparece cuando se arregla el DETECTOR que la producia, y
	# entonces el push NO toca la ruta listada: toca el checker. Caso real: el patron de
	# `check-sigpipe-booleans.sh` contaba un OR logico `grep -q A || grep -q B` como si
	# fuera una tuberia, por su SEGUNDA barra. Al anclarlo,
	# `scripts/check-screenshot-coverage.sh` salio de la linea base — un fichero que
	# nunca tuvo una tuberia y cuyo propio comentario ya decia «HERE-STRING, NO
	# TUBERIA». `lint:sigpipe-booleans` exigia bajar la base en el mismo commit y este
	# gate lo rechazaba: los dos trinquetes, otra vez, mutuamente insatisfacibles.
	#
	# La regla que lo resuelve sin abrir un agujero: si el push toca el CHECKER que
	# produce esa linea base, la retirada tiene trabajo detras — sólo que en otro
	# fichero. Se deriva por la convencion de nombres
	# (`docs/<x>-baseline.txt` ⇒ `scripts/check-<x>.sh`) y **se dice en voz alta**, con
	# el checker nombrado, porque una permisividad invisible se sufre.
	if [ "$con_trabajo" -eq 0 ]; then
		_stem="$(basename "$b")"; _stem="${_stem%-baseline.txt}"; _stem="${_stem%.txt}"
		_stem="${_stem%-baseline}"
		_checker="scripts/check-${_stem}.sh"
		if [ -f "$_checker" ] && grep -qE "(^|/)$(printf '%s' "$_checker" | sed 's/[][\.*^$+?(){}|\/]/\\&/g')$" <<<"$tocados"; then
			echo "check-baseline-shrink: $b pierde $n_quit entrada(s) y el push no toca sus rutas,"
			echo "  pero SI toca su productor: $_checker. Una entrada que cae porque el detector"
			echo "  dejo de inventarla tiene trabajo detras, en otro fichero. Aceptado y dicho."
			continue
		fi
	fi
	if [ "$con_trabajo" -eq 0 ]; then
		hallazgos=$((hallazgos + 1))
		echo "check-baseline-shrink: ⛔ $b PIERDE $n_quit entrada(s) y el push NO TOCA NINGUNA de ellas." >&2
		printf '%s\n' "$quitadas" | head -8 | sed 's/^/      /' >&2
		echo "  Un commit que solo borra lineas de una linea base es indistinguible de silenciar el gate." >&2
		echo "  Y el coste no lo paga quien lo escribe: en la rama el gate esta VERDE, y enrojece al" >&2
		echo "  aterrizar en el tronco, donde ya bloquea a todos los carriles." >&2
		echo "  repair: haz el trabajo que justifica quitarlas (y el push lo tocara), o dejalas." >&2
	fi
done

if [ "$vistas" -eq 0 ]; then
	echo "check-baseline-shrink: ninguna de las lineas base vigiladas existe en la base de comparacion; nada que juzgar."
	exit 0
fi
[ "$hallazgos" -eq 0 ] || exit 1
echo "check-baseline-shrink: OK — $vistas linea(s) base vigilada(s), ninguna encoge sin trabajo detras."
exit 0
