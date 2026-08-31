#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-ci-step-guard.sh — un job que puede tardar tiene que poder DECIR que se pasó.
#
# CENSUS-SUBJECT: internal
#   Su sujeto son los ficheros de `.github/workflows/`. Un árbol sin workflows pasaría, y es
#   correcto; lo que NO puede hacer es pasar sin haber podido leerlos.
#
# ES LA SEGUNDA INVARIANTE DE LOS TECHOS, y la hermana de `check-ci-timeout-arithmetic.sh`.
# Aquélla dice «el techo del JOB tiene que quedar por encima de la suma de los de sus PASOS».
# Ésta dice lo que faltaba: **que haya alguno**.
#
# ⛔ POR QUÉ, Y ES EL MISMO MECANISMO QUE COSTÓ CATORCE CORRIDAS SIN VEREDICTO.
#
# Un `timeout-minutes` de JOB **cancela los pasos que quedan**. El reportero de fallos
# (`.github/actions/pr-failure-report`) es un paso más, y va el último — así que cuando el techo
# del job es el que muerde, el reportero **no llega a correr** y el rojo sale MUDO: GitHub lo
# etiqueta `cancelled`, que además es la palabra de la supersesión, y desde fuera un techo agotado
# y una corrida superseded se escriben igual. Eso es exactamente lo que dejó a `control-plane`
# —contexto REQUERIDO por la protección de `main`— con CERO éxitos en catorce corridas sin que
# nadie pudiera leer por qué.
#
# Un `timeout-minutes` de PASO no tiene ese defecto: falla el paso, el job sigue, y el reportero
# publica. ⇒ **un job sin NI UN techo de paso sólo puede morir de la forma que no se sabe leer.**
#
# ⛔ EL UMBRAL NO ESTÁ INVENTADO: SALE DE MEDIR, y esta sección es la razón de que el gate
# existiera «pendiente de duraciones» desde el 2026-08-22. Medido el 2026-08-23 sobre corridas
# reales (`gh run view <id> --json jobs`, duraciones POR PASO, que es lo que el total del job no
# dice):
#
#     job              techo   real     paso más caro                    ¿guarda de paso?
#     classify             5    0,1     —                                 no  ← no le hace falta
#     race-hot             5    0,1     —                                 no  ← no le hace falta
#     license-worker      15    6,4     install go-task 5,0               no  ← no le hace falta
#     helm-render         30    1,6     install kubeconform 1,3           no  ← no le hace falta
#     ---------------------------------------------------------------------------------------
#     fuzz                45   20,2     fuzz smoke 15,1                   NO  ← puede morir mudo
#     sast                45   37,0     gosec 29,8                        NO  ← puede morir mudo
#     secrets             90   40,4     gitleaks 32,1                     NO  ← puede morir mudo
#     race-workspace      90   53,5     race workspace 53,0               NO  ← puede morir mudo
#     race-root          170  120,5     race root suite 120,2             NO  ← puede morir mudo
#
# La línea cae limpia en **30 minutos**: por debajo, la ventana muda es pequeña y ningún job
# medido se acerca a su techo; por encima, se pierde media hora o más sin diagnóstico. El umbral
# es configurable (`OLIVARES_STEP_GUARD_MIN`) pero su valor por defecto viene de esa tabla.
#
# ⚠ Y EL CENSO DESTAPÓ QUE LA DEUDA ESTABA INFRA-CONTADA. Se había escrito como «SIETE jobs son
# invisibles al gate» mirando sólo `mainline-ci.yml`. Contando los SEIS workflows: **trece** jobs
# sin ninguna guarda de paso, y los dos peores no estaban en la lista — `race-root` (techo 170,
# corre 120,5) y `race-workspace` (techo 90, corre 53,5), los dos en `race-full.yml`.
#
# LAS TRES RESPUESTAS
#   LIMPIO (0)             ningún job por encima del umbral sin guarda de paso
#   ROTO (1)               al menos uno — se NOMBRA, con su techo
#   NO_HE_PODIDO_MIRAR (2) no hay directorio de workflows, o uno no se deja leer
#
# USO
#   scripts/check-ci-step-guard.sh [directorio-de-workflows]

set -u -o pipefail
export LC_ALL=C

UMBRAL="${OLIVARES_STEP_GUARD_MIN:-30}"

# ⛔ LA RAIZ SE RESUELVE DESDE EL GUION, NO DESDE EL `cwd`, y me costo un push.
# La primera version ponia `DIR="${1:-.github/workflows}"`, o sea RELATIVO al directorio actual.
# El `pre-push` no corre siempre desde la raiz del repositorio —la bateria de
# `lint:prepush-refclass` lo ejercita contra repos sinteticos—, asi que alli el directorio no
# existia, este gate respondia 2 (NO PUDE MIRAR), `task` lo aplastaba a 201 y el hook RECHAZABA
# el push. Nueve casos de esa bateria en rojo, cinco de ellos ejecutando el hook de verdad.
#
# Y el fallo ruidoso era el caso AMABLE: desde un `cwd` que SI tuviera un `.github/workflows`
# distinto, la version vieja habria medido EL ARBOL EQUIVOCADO y dicho CLEAN. Un gate que se
# orienta por el `cwd` no mide lo que dice medir. El hermano ya lo hacia asi
# (`check-ci-timeout-arithmetic.sh:21`); yo no lo copie y la bateria lo caza.
RAIZ="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
DIR="${1:-$RAIZ/.github/workflows}"

case "$UMBRAL" in
'' | *[!0-9]*)
	printf 'check-ci-step-guard: ⛔ NO HE PODIDO MIRAR: OLIVARES_STEP_GUARD_MIN=%s no es un entero\n' "$UMBRAL" >&2
	exit 2
	;;
esac

if [ ! -d "$DIR" ]; then
	printf 'check-ci-step-guard: ⛔ NO HE PODIDO MIRAR: no existe el directorio %s\n' "$DIR" >&2
	exit 2
fi

# El recorrido va en awk sobre CADA fichero, y la razón de no usar un parser de YAML es la de
# siempre en este repo: no hay ninguno garantizado en las tres cajas ni en los runners, y un gate
# que depende de una herramienta que puede faltar es un gate que un día responde «no he podido
# mirar» sin que nadie lo note. La forma que sí se puede leer sin YAML es la sangría, y estos
# workflows la tienen fijada por `lint:ci-*` desde hace meses:
#
#     jobs:
#       <nombre>:                      2 espacios
#         timeout-minutes: N           4 espacios  -> techo de JOB
#         steps:
#           - name: ...
#             timeout-minutes: N       cualquier sangría MAYOR que 4 -> techo de PASO
#
# Si algún día un workflow deja de cumplirla, el fichero cae a NO_HE_PODIDO_MIRAR abajo en vez de
# pasar: la ambigüedad no se resuelve a favor del verde.
hallazgos=0
mirados=0
ilegibles=0

for wf in "$DIR"/*.yml "$DIR"/*.yaml; do
	[ -e "$wf" ] || continue
	if [ ! -r "$wf" ]; then
		printf 'check-ci-step-guard: ⛔ NO HE PODIDO MIRAR: no puedo leer %s\n' "$wf" >&2
		ilegibles=$((ilegibles + 1))
		continue
	fi
	mirados=$((mirados + 1))
	salida=$(
		awk -v umbral="$UMBRAL" -v fichero="$wf" '
			# Sólo dentro del bloque `jobs:` de primer nivel.
			/^jobs:[[:space:]]*$/ { enjobs = 1; next }
			/^[A-Za-z_.-]+:/      { if (enjobs) enjobs = 0 }
			!enjobs { next }

			# Un job nuevo: se emite el veredicto del anterior antes de olvidarlo.
			/^  [A-Za-z0-9_-]+:[[:space:]]*$/ {
				if (job != "" && techo > umbral && pasos == 0)
					printf "%s\t%s\t%d\n", fichero, job, techo
				job = $0
				sub(/^  /, "", job); sub(/:[[:space:]]*$/, "", job)
				techo = 0; pasos = 0
				next
			}
			job == "" { next }

			# Techo de JOB: exactamente cuatro espacios.
			/^    timeout-minutes:[[:space:]]*[0-9]+[[:space:]]*$/ {
				v = $0; sub(/^.*timeout-minutes:[[:space:]]*/, "", v); sub(/[[:space:]]*$/, "", v)
				techo = v + 0
				next
			}
			# Techo de PASO: cualquier sangría mayor.
			/^[[:space:]][[:space:]][[:space:]][[:space:]][[:space:]]+timeout-minutes:[[:space:]]*[0-9]+[[:space:]]*$/ {
				pasos++
				next
			}

			END {
				if (job != "" && techo > umbral && pasos == 0)
					printf "%s\t%s\t%d\n", fichero, job, techo
			}
		' "$wf"
	)
	rc=$?
	if [ "$rc" -ne 0 ]; then
		printf 'check-ci-step-guard: ⛔ NO HE PODIDO MIRAR: awk falló (rc=%s) sobre %s\n' "$rc" "$wf" >&2
		ilegibles=$((ilegibles + 1))
		continue
	fi
	[ -n "$salida" ] || continue
	while IFS=$'\t' read -r f j t; do
		[ -n "${j:-}" ] || continue
		hallazgos=$((hallazgos + 1))
		printf 'check-ci-step-guard: ⛔ %s: el job «%s» tiene techo %s min y NINGUNA guarda de paso.\n' "$f" "$j" "$t"
		printf '                       Un techo de JOB cancela los pasos que quedan, así que el reportero\n'
		printf '                       de fallos no llega a publicar y el rojo sale MUDO. Ponle\n'
		printf '                       `timeout-minutes:` al paso que puede tardar, por debajo de %s.\n' "$t"
	done <<EOF
$salida
EOF
done

if [ "$ilegibles" -gt 0 ]; then
	printf 'check-ci-step-guard: ⛔ NO HE PODIDO MIRAR %d fichero(s); no declaro limpio lo que no he leído.\n' "$ilegibles" >&2
	exit 2
fi

if [ "$mirados" -eq 0 ]; then
	printf 'check-ci-step-guard: ⛔ NO HE PODIDO MIRAR: cero workflows en %s\n' "$DIR" >&2
	exit 2
fi

if [ "$hallazgos" -gt 0 ]; then
	printf 'check-ci-step-guard: %d job(s) por encima de %s min sin guarda de paso.\n' "$hallazgos" "$UMBRAL" >&2
	exit 1
fi

printf 'check-ci-step-guard: CLEAN — %d workflow(s); ningún job por encima de %s min sin guarda de paso.\n' "$mirados" "$UMBRAL"
exit 0
