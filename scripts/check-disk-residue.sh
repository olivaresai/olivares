#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-disk-residue.sh - senala familias de temporales que MERECEN QUE SU DUENIO LAS MIRE.
#
# CENSUS-SUBJECT: external
#   Su sujeto es LA CAJA (los TMPDIR), no el repositorio. Por eso NO es pata del gancho: pasar
#   sobre un arbol limpio no dice nada de una caja sucia. Lo corre cada carril al cerrar.
#
# ⛔ EL VEREDICTO SE LLAMA SOSPECHA Y NO FUGA, Y ESO NO ES PRUDENCIA: ES LO QUE EL PREDICADO MIDE.
# La version anterior afirmaba "no tiene falsos positivos por construccion". El contraste
# `the model` max del 2026-09-02 lo REFUTO POR EJECUCION
# (an internal design note (not shipped)) reproduciendo las dos direcciones del error:
# acuso a un productor vivo que no tenia descriptores abiertos en el instante de la foto, acuso a
# 120 entradas que ya habian sido BORRADAS afirmando que "siguen ahi", y absolvio a 119 huerfanas
# porque UNA hermana estaba viva. Su veredicto fue NO-LAND y tenia razon.
#
# QUE SE ARREGLO, punto por punto, y que NO:
#   . raices canonicalizadas y DEDUPLICADAS  -> `TMPDIR=/tmp` ya no contaba `/tmp` dos veces
#   . DOS fotos de /proc, antes y despues, y se toma la UNION -> cierra medio intervalo de carrera
#   . se censa tambien /proc/PID/exe, y una ruta viva cubre a la entrada que la CONTIENE
#   . se excluye la ENTRADA viva, no la familia entera; n y bytes se recalculan sin ella
#   . se revalida la existencia al decidir: lo que desaparecio no cuenta ni se afirma que sigue
#   . los bytes deduplican por (st_dev, st_ino): un hard link no se cuenta dos veces
#   . lo ilegible NO se convierte en cero: degrada a la tercera respuesta
#   . la familia lleva su RAIZ en la clave: dos raices no se suman en una familia falsa
#   . el censo de PRIMER NIVEL se cuenta POR RAIZ y no con un booleano; cada `find` escribe en SU
#     temporal y solo se adjunta si termina 0; cualquier raiz sin enumerar entera termina en 2
#     ANTES del analisis. Lo anadio el 2026-09-05 la revision independiente `the model`, que
#     reprodujo el defecto contrario: una raiz sana borraba el fallo de otra y un listado parcial
#     se analizaba como completo, o sea CLEAN despues de omitir una raiz que se pidio.
#   . NO se arregla, y se dice: sin registro de actividad de gates (PID + raiz) o sin una ventana
#     de antiguedad, cardinalidad y tamano no pueden dar un veredicto categorico. De ahi SOSPECHA.
#
# ⚠ Y UN LIMITE DEL CONSUMIDOR, medido por el mismo contraste: `task disk:residue` APLASTA 1 y 2
# a 201. Quien necesite distinguir "hallazgo" de "no pude mirar" tiene que invocar este guion
# directamente, no la tarea.
#
#   0  CLEAN          ninguna familia pasa los umbrales sobre lo que se pudo leer
#   1  SOSPECHA       una familia merece que su duenio la mire
#   2  NO PUDE MIRAR  cualquier raiz solicitada sin enumerar entera, ninguna raiz enumerada,
#                     umbral no numerico, o lectura parcial de los descendientes
set -u

N_MIN="${OLIVARES_RESIDUE_MIN_COUNT:-20}"
MIB_MIN="${OLIVARES_RESIDUE_MIN_MIB:-50}"
N_SOLO="${OLIVARES_RESIDUE_COUNT_ONLY:-100}"
RAIZ="${OLIVARES_CLONE:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"

if [ "${1:-}" = "--selftest" ]; then
	exec bash "$(dirname "${BASH_SOURCE[0]}")/test-disk-residue.sh"
fi

lista="$(mktemp "${TMPDIR:-/tmp}/residue-list.XXXXXX" 2>/dev/null)" || {
	echo "check-disk-residue: NO PUDE MIRAR - no puedo crear mi propio temporal." >&2
	exit 2
}
vivos="$(mktemp "${TMPDIR:-/tmp}/residue-live.XXXXXX" 2>/dev/null)" || {
	rm -f "$lista"
	echo "check-disk-residue: NO PUDE MIRAR - no puedo crear mi propio temporal." >&2
	exit 2
}
# UN solo trap con TODOS los ficheros y armado aqui: `trap` sustituye, no acumula. Es la regla que
# este guion existe para vigilar, aplicada a si mismo. `parcial` es el temporal POR RAIZ del censo
# de abajo: se declara vacio para que el trap lo cubra desde ya, incluso antes de que exista.
parcial=""
trap 'rm -f "$lista" "$vivos" "${parcial:-}"' EXIT

foto_vivos() {
	{
		find /proc -maxdepth 3 -path '/proc/[0-9]*/fd/*' -type l -printf '%l\n' 2>/dev/null
		find /proc -maxdepth 2 -path '/proc/[0-9]*/cwd' -type l -printf '%l\n' 2>/dev/null
		find /proc -maxdepth 2 -path '/proc/[0-9]*/exe' -type l -printf '%l\n' 2>/dev/null
	} 2>/dev/null
}

# FOTO 1, antes de censar.
foto_vivos >"$vivos"

# --- CENSO DE PRIMER NIVEL. El contrato, y NO es el mismo para las dos fuentes de raices:
#
#   . EXPLICITAS (`OLIVARES_RESIDUE_DIRS`): cada una es una OBLIGACION de esta invocacion. Si no
#     se puede enumerar ENTERA -no existe, no es un directorio, no se deja leer, no canonicaliza,
#     o `find` no termina 0-, la inspeccion es PARCIAL y el veredicto global es 2. No se finge
#     haber mirado una raiz que no esta: se dice que se pidio y no se pudo.
#   . POR DEFECTO (`TMPDIR` y `/tmp`): son CANDIDATOS de esta caja, no peticiones. Uno que NO
#     EXISTE no es un fallo -sencillamente no es una raiz de esta caja- y se NOMBRA en el
#     diagnostico en vez de desaparecer. Uno que EXISTE y no se deja enumerar SI es un fallo: ahi
#     hay un punto ciego real. Y si no queda ninguna raiz enumerada, no hay veredicto que dar.
#
# ⛔ ESTO ERA UN SOLO BOOLEANO `mirado`, Y LO REFUTO POR EJECUCION la revision independiente
# `the model` del 2026-09-05 sobre `0525eed8e1` (assessments/implementation/disk-residue-ci/
# review/first-level-enumeration-followup.md). CUALQUIER raiz que terminara 0 lo ponia a 1, asi
# que UNA RAIZ SANA BORRABA EL FALLO DE OTRA. Reproducido por dos mecanismos distintos:
#
#   . raiz solicitada con `chmod 000`  -> rc 2 ella sola; con una raiz vacia al lado, rc 0 CLEAN
#   . `find` que emite registros validos y termina 1 -> rc 2 el solo; con una raiz sana al lado,
#     rc 0 CLEAN **y ademas analizando el listado parcial**, que ya se habia anadido a `lista`
#
# Un CLEAN categorico despues de omitir una raiz que se pidio es exactamente lo que este guion
# existe para no hacer. Tres cosas lo cierran: se cuenta POR RAIZ y no con un booleano; cada
# `find` escribe en SU temporal y solo se adjunta si termina 0; y cualquier fallo termina en 2
# ANTES de llamar al analisis, porque una sospecha sobre un censo con agujeros no es un veredicto.
if [ -n "${OLIVARES_RESIDUE_DIRS:-}" ]; then
	# Con raices explicitas manda la lista dada: las de por defecto no se anaden.
	fuente=explicitas
	crudas="$(printf '%s\n' "$OLIVARES_RESIDUE_DIRS" | tr ' ' '\n' | sed '/^$/d' | sort -u)"
else
	fuente=defecto
	crudas="$(printf '%s\n%s\n' "${TMPDIR:-/tmp}" '/tmp' | sed '/^$/d' | sort -u)"
fi

# `sort -u` sobre las rutas CRUDAS y otro sobre las CANONICAS: repetir la misma raiz -con la misma
# grafia o con otra- no crea una segunda obligacion ni duplica el censo. Eso no cambia.
incompleto=0    # raices que NO quedaron enumeradas enteras; >0 obliga a 2
ausentes=0      # candidatos por defecto que no existen: no son fallo, pero se nombran
motivos=""      # diagnostico agregado: rutas y motivos, NUNCA contenido de los temporales
canonicas=""

anota() { motivos="$motivos    $1"$'\n'; }

while IFS= read -r d; do
	[ -n "$d" ] || continue
	if [ ! -e "$d" ]; then
		if [ "$fuente" = defecto ]; then
			ausentes=$((ausentes + 1))
			anota "$d: no existe. Candidato por defecto, no una raiz de esta caja."
		else
			incompleto=$((incompleto + 1))
			anota "$d: SOLICITADA y no existe. No se puede inspeccionar lo que no esta."
		fi
		continue
	fi
	if [ ! -d "$d" ]; then
		incompleto=$((incompleto + 1))
		anota "$d: existe y NO es un directorio."
		continue
	fi
	if [ ! -r "$d" ]; then
		incompleto=$((incompleto + 1))
		anota "$d: existe y este lector NO la puede leer."
		continue
	fi
	c="$(cd "$d" 2>/dev/null && pwd -P)" || c=""
	if [ -z "$c" ]; then
		incompleto=$((incompleto + 1))
		anota "$d: no pude canonicalizarla (cd/pwd -P)."
		continue
	fi
	canonicas="$canonicas$c"$'\n'
done <<<"$crudas"
canonicas="$(printf '%s' "$canonicas" | sed '/^$/d' | sort -u)"

enumeradas=0
while IFS= read -r d; do
	[ -n "$d" ] || continue
	# UN temporal POR RAIZ. Antes `find` escribia directo en `lista`, asi que un fallo a media
	# enumeracion dejaba dentro los registros que hubiera alcanzado a emitir y el analisis los
	# tomaba por un censo completo. Que haya producido lineas NO significa que enumerara entera.
	parcial="$(mktemp "${TMPDIR:-/tmp}/residue-root.XXXXXX" 2>/dev/null)" || {
		parcial=""
		incompleto=$((incompleto + 1))
		anota "$d: no pude crear el temporal de su censo."
		continue
	}
	if find "$d" -maxdepth 1 -mindepth 1 -printf "$d\t%p\n" >"$parcial" 2>/dev/null; then
		if cat "$parcial" >>"$lista"; then
			enumeradas=$((enumeradas + 1))
		else
			incompleto=$((incompleto + 1))
			anota "$d: no pude incorporar completo el temporal de su censo."
		fi
	else
		incompleto=$((incompleto + 1))
		anota "$d: \`find\` no termino 0. Su salida parcial se DESCARTA sin analizarla."
	fi
	rm -f "$parcial"
	parcial=""
done <<<"$canonicas"

if [ "$incompleto" -ne 0 ] || [ "$enumeradas" -eq 0 ]; then
	{
		if [ "$enumeradas" -eq 0 ]; then
			echo "check-disk-residue: NO PUDE MIRAR - ninguna raiz enumerada entera."
		else
			echo "check-disk-residue: NO PUDE MIRAR - inspeccion PARCIAL:" \
				"$enumeradas entera(s), $incompleto sin enumerar."
		fi
		printf '%s' "$motivos"
		echo "  Una raiz sana NO cubre a otra que no se pudo mirar. Un veredicto aqui"
		echo "  seria categorico sobre un censo con agujeros, asi que sale 2."
	} >&2
	exit 2
fi
if [ "$ausentes" -ne 0 ]; then
	# No es un fallo, y por eso no cambia el codigo de salida; pero desaparecer en silencio es
	# justo el defecto que este bloque corrige, asi que se nombra.
	{
		echo "check-disk-residue: $ausentes candidato(s) por defecto no existe(n):"
		printf '%s' "$motivos"
	} >&2
fi

# FOTO 2, despues de censar: se toma la UNION. Un proceso que abrio entre medias aparece aqui, y
# una sola foto lo habria dejado fuera. No cierra la carrera entera -eso exige registro de
# actividad-, pero cierra la mitad que se puede cerrar sin cambiar el modelo.
foto_vivos >>"$vivos"

RAIZ="$RAIZ" \
	OLIVARES_RESIDUE_MIN_COUNT="$N_MIN" \
	OLIVARES_RESIDUE_MIN_MIB="$MIB_MIN" \
	OLIVARES_RESIDUE_COUNT_ONLY="$N_SOLO" \
	python3 "$(dirname "${BASH_SOURCE[0]}")/lib/disk-residue.py" "$lista" "$vivos"
