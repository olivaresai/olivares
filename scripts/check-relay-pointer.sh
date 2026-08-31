#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-relay-pointer.sh — el puntero de relevo vivo tiene que señalar a un fichero que EXISTE y que es
# el de numero MAS ALTO. Nada mas.
#
# ⛔ POR QUE EXISTE. El indice de relevos abre con una seccion que se declara a si misma
# «unica fuente — si la ignoras, empiezas con amnesia», y la regla escrita es «quien escriba un relevo
# nuevo cambia ESTA linea en el mismo commit». **Se incumplio DOS VECES SEGUIDAS**: la linea apunto a
# RELEVO19 durante TRES DIAS mientras ya existian el 21 y el 22, los dos del mismo dia.
#
# Y el propio texto que lo corrigio nombra la causa mejor de lo que yo podria: «el defecto no es el
# puntero viejo: es que actualizarlo depende de que quien escribe el relevo SE ACUERDE, y dos de dos no
# se acordaron». Un puesto con 19 ficheros fechados y ninguno que diga cual manda no se arregla
# pidiendo memoria: se arregla comparando.
#
# LO QUE NO HACE, dicho a proposito: no juzga el CONTENIDO del relevo ni si esta al dia. Solo responde
# a «¿el puntero nombra el fichero mas nuevo que existe?», que es un predicado total y barato. Un gate
# que intentara juzgar si un relevo esta «actualizado» seria criterio disfrazado de medida.
#
# TRES RESPUESTAS: 0 coherente · 1 el puntero no es el mas alto o no existe · 2 no he podido mirar.
set -uo pipefail
LC_ALL=C
export LC_ALL

RAIZ="${OLIVARES_RELAY_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
DIR="$RAIZ/sessions"

# ⛔ LOS PATRONES VIENEN DE UN FICHERO CURADO FUERA DE LA SUPERFICIE DE EXPORT, no de aqui: llevan
#    identificadores de carril y `scripts/` se exporta. Si ese fichero falta, esto REHUSA — no hay
#    respaldo embebido, que republicaria el literal por la puerta de atras.
. "$RAIZ/scripts/lib/lane-artefacts.sh"
if ! lane_artefacts_load "$RAIZ"; then
	echo "check-relay-pointer: NO HE PODIDO MIRAR — sin los patrones curados no se que relevo vigilar." >&2
	exit 2
fi
IDX="$DIR/$LA_RELAY_INDEX"

[ -f "$IDX" ] || { echo "check-relay-pointer: ⛔ NO HE PODIDO MIRAR: no encuentro $IDX" >&2; exit 2; }
[ -d "$DIR" ] || { echo "check-relay-pointer: ⛔ NO HE PODIDO MIRAR: no encuentro $DIR" >&2; exit 2; }

# El puntero: la PRIMERA ruta de relevo (patron curado) que aparece en el indice.
apuntado="$(grep -oE "$LA_RELAY_REF_REGEX" "$IDX" 2>/dev/null | head -1)"
[ -n "$apuntado" ] || { echo "check-relay-pointer: ⛔ NO HE PODIDO MIRAR: el indice no nombra ningun fichero de relevo con la forma esperada." >&2; exit 2; }

# CONTROL POSITIVO: cero relevos en disco no es «coherente», es que no estoy mirando donde creo.
n_disco="$(find "$DIR" -maxdepth 1 -name "$LA_RELAY_GLOB" 2>/dev/null | grep -c . || true)"
if [ "${n_disco:-0}" -eq 0 ]; then
	echo "check-relay-pointer: ⛔ NO HE PODIDO MIRAR: cero ficheros de relevo en $DIR." >&2
	exit 2
fi

mayor_n=0
mayor_f=""
while IFS= read -r f; do
	[ -n "$f" ] || continue
	n="$(printf '%s' "$f" | sed -n 's/.*RELEVO\([0-9]\{1,\}\)\.md$/\1/p')"
	[ -n "$n" ] || continue
	if [ "$n" -gt "$mayor_n" ]; then mayor_n="$n"; mayor_f="$(basename "$f")"; fi
done <<EOF_LISTA
$(find "$DIR" -maxdepth 1 -name "$LA_RELAY_GLOB" 2>/dev/null)
EOF_LISTA

[ -n "$mayor_f" ] || { echo "check-relay-pointer: ⛔ NO HE PODIDO MIRAR: no he podido extraer numero de ningun relevo." >&2; exit 2; }

apuntado_base="$(basename "$apuntado")"
apuntado_n="$(printf '%s' "$apuntado_base" | sed -n 's/.*RELEVO\([0-9]\{1,\}\)\.md$/\1/p')"

echo "check-relay-pointer: relevos en disco=$n_disco · el mas alto=$mayor_f (RELEVO$mayor_n) · el indice apunta a $apuntado_base"

if [ ! -f "$DIR/$apuntado_base" ]; then
	echo "check-relay-pointer: ⛔ el puntero nombra un fichero que NO EXISTE: $apuntado_base" >&2
	echo "  repair: apunta a sessions/$mayor_f, que es el mas alto que hay en disco." >&2
	exit 1
fi

if [ "${apuntado_n:-0}" -ne "$mayor_n" ]; then
	echo "check-relay-pointer: ⛔ el puntero NO es el relevo mas alto: apunta a RELEVO$apuntado_n y existe RELEVO$mayor_n." >&2
	echo "  La seccion que lo contiene se declara «unica fuente»; apuntando a un relevo viejo, quien" >&2
	echo "  abra sesion empieza con la foto de otro dia y no tiene forma de notarlo." >&2
	echo "  repair: cambia esa linea a sessions/$mayor_f en el MISMO commit que escribe el relevo." >&2
	exit 1
fi

echo "check-relay-pointer: OK — el puntero nombra el relevo mas alto que existe."
exit 0
