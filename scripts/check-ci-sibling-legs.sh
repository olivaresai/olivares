#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-ci-sibling-legs.sh — una pata independiente no puede colgar del veredicto de su vecina.
#
# CENSUS-SUBJECT: internal
#   Su sujeto son los ficheros de `.github/workflows/`. Un árbol sin workflows pasa, y es correcto;
#   lo que NO puede hacer es pasar sin haber podido leerlos.
#
# ES LA TERCERA INVARIANTE DE LOS TECHOS, hermana de `check-ci-timeout-arithmetic.sh` («el techo del
# JOB por encima de la suma de los de sus PASOS») y de `check-ci-step-guard.sh` («que haya alguno»).
# Aquéllas miran los TECHOS. Ésta mira la DEPENDENCIA: que una pata no se salte por lo que le pasó
# a otra que no es su sujeto.
#
# ⛔ POR QUÉ, Y ESTÁ MEDIDO EN DOS CORRIDAS DEL MISMO DÍA, NO DEDUCIDO.
#
# `race-rest` corre CINCO patas independientes. La primera tiene sujeto propio; las otras cuatro
# —subpaquetes de cmd/olivares, el manifiesto hot, el plano de control cloud y el dominio de
# comercio— no llevaban `if:`, así que **un rojo en la primera se las saltaba TODAS**. Medido el
# 2026-08-24 en `32741025007` y `32753234715`: paso 10 en rojo en las dos, y pasos 11-14 `skipped`
# en las dos.
#
# Entre esos cuatro está **`race (commerce domain)`, que es el ÚNICO sitio donde `commerce-core`
# corre bajo `-race`**. ⇒ cada vez que la primera pata enrojece, comercio pierde su única cobertura
# de carreras. Y no se nota: el job ya está rojo por arriba, así que nadie lee más abajo. Es la
# forma exacta de «un módulo cubierto sólo por un gate que nadie ejecuta se declara cubierto», con
# el agravante de que aquí el gate EXISTE y se salta callando.
#
# ⛔ EL ALCANCE ES DELIBERADAMENTE ESTRECHO, y esto es una decisión, no un descuido.
#
# La invariante general —«ningún paso de verificación independiente debería saltarse porque otro
# falló»— es CIERTA y marcaría decenas de pasos de `control-plane` (lo vimos el mismo día: el paso
# 46 falla y los pasos 47 y 49, que son drift de codegen, salen `skipped`). Pero adoptarla es un
# cambio de POLÍTICA: alarga jobs, cambia lo que significa un rojo y es de quien manda, no de un
# gate que aterriza de tapadillo. Así que este control cubre sólo lo medido y no discutido: los
# jobs `race*`, cuyas patas son independientes por construcción.
#
# Censo del 2026-08-24 sobre los diecinueve workflows, para que se vea que no marca de más:
#
#     fichero            job              patas   guardadas
#     mainline-ci.yml    race-modules       1       —          (una sola: nada que colgar)
#     mainline-ci.yml    race-rest          5       4 de 4     ← el sujeto de este gate
#     mainline-ci.yml    race-hot           0       —          (agrega veredictos, no corre nada)
#     race-full.yml      race-workspace     1       —
#     race-full.yml      race-root          1       —
#
# Una «pata» es un paso con `run:` Y `timeout-minutes:`. Se exige la guarda **de la segunda en
# adelante**: la primera no tiene ninguna pata delante de la que colgar.
#
# LA GUARDA QUE SE EXIGE es `!cancelled()`, no `always()`: una corrida cancelada tiene que seguir
# cancelada. Lo que este gate NO comprueba es el resto de la condición (que la cadena de
# herramientas exista), porque eso varía por job y ya lo documenta el propio fichero en :1903.
#
# LAS TRES RESPUESTAS
#   LIMPIO (0)             ninguna pata hermana sin guarda
#   ROTO (1)               al menos una — se NOMBRA, con su job y su fichero
#   NO_HE_PODIDO_MIRAR (2) no hay directorio de workflows, o uno no se deja leer
#
# USO
#   scripts/check-ci-sibling-legs.sh [directorio-de-workflows]

set -u -o pipefail
export LC_ALL=C

# La raíz se resuelve desde el GUION, no desde el `cwd`. El hermano `check-ci-step-guard.sh`
# documenta lo que cuesta no hacerlo: la batería de `lint:prepush-refclass` ejercita el hook contra
# repos sintéticos, así que un camino relativo mide el árbol equivocado o no mide nada.
RAIZ="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
DIR="${1:-$RAIZ/.github/workflows}"

if [ ! -d "$DIR" ]; then
	printf 'check-ci-sibling-legs: ⛔ NO HE PODIDO MIRAR: no existe el directorio %s\n' "$DIR" >&2
	exit 2
fi

hallazgos=0
mirados=0
ilegibles=0

for wf in "$DIR"/*.yml "$DIR"/*.yaml; do
	[ -e "$wf" ] || continue
	if [ ! -r "$wf" ]; then
		printf 'check-ci-sibling-legs: ⛔ NO HE PODIDO MIRAR: no puedo leer %s\n' "$wf" >&2
		ilegibles=$((ilegibles + 1))
		continue
	fi
	mirados=$((mirados + 1))

	salida=$(awk -v fichero="${wf##*/}" '
		function cerrar_paso() {
			if (paso_abierto && tiene_run && tiene_techo) {
				patas++
				if (patas > 1 && !tiene_guarda)
					printf "%s\t%s\t%s\n", fichero, job, nombre
			}
			paso_abierto = 0; tiene_run = 0; tiene_techo = 0; tiene_guarda = 0; nombre = ""
		}
		# job nuevo: dos espacios de sangría
		/^  [A-Za-z0-9_-]+:[[:space:]]*$/ {
			cerrar_paso()
			job = $0; sub(/^  /, "", job); sub(/:[[:space:]]*$/, "", job)
			patas = 0
			en_race = (job ~ /^race/)
			next
		}
		# cualquier clave de nivel superior cierra el job en curso
		/^[A-Za-z]/ { cerrar_paso(); en_race = 0; next }
		en_race == 0 { next }
		/^      - name:/ {
			cerrar_paso()
			paso_abierto = 1
			nombre = $0; sub(/^      - name:[[:space:]]*/, "", nombre)
			next
		}
		paso_abierto == 0 { next }
		/^        run:/          { tiene_run = 1 }
		/^        timeout-minutes:/ { tiene_techo = 1 }
		/^        if:/           { if (index($0, "!cancelled()") > 0) tiene_guarda = 1 }
		END { cerrar_paso() }
	' "$wf")

	if [ -n "$salida" ]; then
		while IFS=$'\t' read -r f j n; do
			[ -n "$f" ] || continue
			hallazgos=$((hallazgos + 1))
			printf 'check-ci-sibling-legs: ⛔ %s · job %s · «%s» es una pata hermana SIN `!cancelled()`:\n' "$f" "$j" "$n"
			printf '    un rojo en la pata anterior la salta en silencio, y el job ya está rojo por arriba.\n'
		done <<-EOF
			$salida
		EOF
	fi
done

if [ "$ilegibles" -gt 0 ]; then
	printf 'check-ci-sibling-legs: ⛔ NO HE PODIDO MIRAR: %s fichero(s) ilegibles de %s\n' "$ilegibles" "$((mirados + ilegibles))" >&2
	exit 2
fi

if [ "$hallazgos" -gt 0 ]; then
	printf 'check-ci-sibling-legs: %s pata(s) hermana(s) sin guarda en %s workflow(s).\n' "$hallazgos" "$mirados" >&2
	exit 1
fi

printf 'check-ci-sibling-legs: limpio — %s workflow(s) mirados, ninguna pata hermana sin guarda.\n' "$mirados"
exit 0
