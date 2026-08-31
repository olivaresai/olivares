#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# run-fastlane.sh — corre EXACTAMENTE el carril rápido del hook, sin el gate pesado y sin el mutex.
#
# ⛔ POR QUÉ EXISTE, con el incidente que lo motiva medido. El gate pesado en `main` es aritmética
# imposible —el 2026-08-18 se midió EN VIVO: 63 min de gate y `main` ya iba dos commits por delante,
# así que el push estaba condenado desde el minuto dos— y el remedio sancionado es `--no-verify`
# declarado. Pero ese bypass NO salta sólo el gate pesado: **salta también el carril rápido**, que
# cuesta ~10 min y es el que caza los defectos baratos.
#
# Y eso tuvo coste el mismo día: dos baterías con `printf … | grep -q` —141 EN ÉXITO bajo pipefail—
# aterrizaron en `main` y **rechazaron el push de los cinco carriles**. `lint:sigpipe-booleans` las
# habría nombrado en el carril rápido. Saltó en el de los demás en vez de en el mío.
#
# ⛔ LA LISTA NO SE ESCRIBE AQUÍ: SE DERIVA DEL HOOK. Una cuarta copia de la lista del carril rápido
# sería exactamente el defecto que este proyecto lleva todo el día encontrando —un hecho en varios
# sitios y sólo uno actualizado—. El corte es el mismo marcador que usan `check-taskfile-graph.sh` y
# la batería del clasificador.
#
# Salidas: 0 = todo verde · 1 = alguna tarea roja (las nombra TODAS, no sólo la primera) ·
# 2 = NO HE PODIDO MIRAR (sin hook, sin marcador o sin `task`).
set -euo pipefail

# ⛔ INSTANTÁNEA, POR LA MISMA RAZÓN QUE EL HOOK — y aquí está medido sobre ESTE fichero.
# bash lee un script por DESPLAZAMIENTO DE BYTES mientras lo ejecuta. Este guion corre ~15 min a
# propósito, así que la ventana para que alguien lo edite en vuelo es enorme: la primera corrida
# completó las 84 tareas —todas OK— y murió con `run-fastlane.sh: line 64: ido.: command not found`,
# un trozo de la prosa que yo mismo le acababa de insertar, ejecutado como comando. Es EXACTAMENTE
# el fallo que este árbol corrigió esta misma mañana en `.githooks/pre-push` («line 543: the:
# command not found»), reproducido por mí en la herramienta escrita para prevenir el anterior.
#
# Mismo remedio: correr desde una copia y BORRARLA acto seguido. El descriptor sigue abierto, el
# inodo vive hasta que el proceso acaba y el contenido pierde su nombre: no hay nada que editar.
if [ -z "${OLIVARES_FASTLANE_SNAPSHOT:-}" ]; then
	_snap="$(mktemp "${TMPDIR:-/tmp}/olivares-fastlane.XXXXXX" 2>/dev/null || mktemp "/tmp/olivares-fastlane.XXXXXX")" || {
		echo "run-fastlane: ⛔ NO HE PODIDO MIRAR: no puedo crear la instantánea." >&2
		exit 2
	}
	if ! cat -- "${BASH_SOURCE[0]}" >"$_snap"; then
		rm -f -- "$_snap"
		echo "run-fastlane: ⛔ NO HE PODIDO MIRAR: no puedo leerme para la instantánea." >&2
		exit 2
	fi
	OLIVARES_FASTLANE_SNAPSHOT="$_snap" exec bash "$_snap" "$@"
fi
rm -f -- "$OLIVARES_FASTLANE_SNAPSHOT"


RAIZ="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || echo "")}"
[ -n "$RAIZ" ] || { echo "run-fastlane: ⛔ NO HE PODIDO MIRAR: no estoy en un repositorio." >&2; exit 2; }
HOOK="${OLIVARES_HOOK:-$RAIZ/.githooks/pre-push}"
[ -r "$HOOK" ] || { echo "run-fastlane: ⛔ NO HE PODIDO MIRAR: no puedo leer $HOOK." >&2; exit 2; }
command -v task >/dev/null 2>&1 || { echo "run-fastlane: ⛔ NO HE PODIDO MIRAR: no hay 'task' en el PATH." >&2; exit 2; }

CORTE='task lint:prepush-refclass'
n_corte="$(grep -n "^${CORTE}\$" "$HOOK" | head -1 | cut -d: -f1)"
[ -n "$n_corte" ] || {
	echo "run-fastlane: ⛔ NO HE PODIDO MIRAR: no encuentro '${CORTE}' en el hook." >&2
	echo "              Sin ese marcador no sé dónde acaba el carril rápido, y adivinarlo" >&2
	echo "              correría el gate pesado por descuido." >&2
	exit 2
}

# Misma extracción que check-taskfile-graph.sh: pela sangrado y prefijos de asignación, de modo que
# una llamada indentada dentro de un condicional (hay una) o con `VAR=x task …` no se caiga.
extraer() {
	sed -e 's/^[[:space:]]*//' \
	    -e ':a' -e 's/^[A-Za-z_][A-Za-z0-9_]*=\("[^"]*"\|'"'"'[^'"'"']*'"'"'\|[^[:space:]]*\)[[:space:]]\+//; ta' \
	  | sed -n 's/^task[[:space:]]\+\([a-z0-9:._-]*\).*/\1/p'
}
# ⛔ Y LAS TOLERADAS SE MARCAN, PORQUE EL HOOK LAS TOLERA. `lint:unpublished-work` se invoca
# `task … || true`: es un AVISO, no un gate — se retiró como trinquete el 2026-08-10 porque en un
# clon compartido cobraba al que empuja por trabajo ajeno. Un guion que dice correr «exactamente el
# carril rápido» y la cuenta como roja NO corre el carril rápido: corre otro conjunto, y eso es el
# defecto que este árbol lleva todo el día encontrando. Medido en su PRIMERA corrida: dio 1 roja
# donde el hook habría seguido.
mapfile -t TAREAS < <(head -n "$n_corte" "$HOOK" | extraer)
TOLERADAS=" $(head -n "$n_corte" "$HOOK" | sed -n 's/^[[:space:]]*task[[:space:]]\+\([a-z0-9:._-]*\)[[:space:]]*||[[:space:]]*true.*/\1/p' | tr '\n' ' ')"
[ "${#TAREAS[@]}" -gt 0 ] || { echo "run-fastlane: ⛔ NO HE PODIDO MIRAR: cero tareas extraídas del hook." >&2; exit 2; }

echo "run-fastlane: ${#TAREAS[@]} tarea(s) del carril rápido, derivadas de $HOOK"
rojas=(); i=0
for t in "${TAREAS[@]}"; do
	i=$((i + 1))
	printf '[%2d/%2d] %-38s' "$i" "${#TAREAS[@]}" "$t"
	if out="$(task "$t" 2>&1)"; then printf 'OK\n'
	elif case "$TOLERADAS" in *" $t "*) true;; *) false;; esac; then
		printf 'aviso (el hook la llama con || true)\n'
		printf '%s\n' "$out" | tail -3 | sed 's/^/        /'
	else
		printf '⛔\n'
		rojas+=("$t")
		printf '%s\n' "$out" | tail -6 | sed 's/^/        /'
	fi
done

if [ "${#rojas[@]}" -gt 0 ]; then
	# Se nombran TODAS, no sólo la primera: parar en la primera obliga a N pasadas de 10 min.
	echo "run-fastlane: ⛔ ${#rojas[@]} tarea(s) roja(s): ${rojas[*]}" >&2
	exit 1
fi
echo "run-fastlane: ✔ carril rápido completo en verde."
exit 0
