#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# test-fast-lane-ceiling.sh — la batería de check-fast-lane-ceiling.sh.
#
# ⛔ SEÑUELOS, NUNCA LOS PROCESOS VIVOS. Las propiedades que hay que probar son las ROJAS —el techo
#    alcanzado, el censo que se cuenta a sí mismo— y probarlas contra el `/proc` real haría que la
#    batería dependiera de lo que otros carriles estén haciendo en ese segundo: verde a las 3:00 y
#    rojo a las 3:01, sin que nadie tocara nada. El gate acepta `OLIVARES_FAST_LANE_PROC`,
#    `OLIVARES_FAST_LANE_SELF` y `OLIVARES_FAST_LANE_CEILING` justamente para que exista este banco.
#
# ⚠ Y EL SEÑUELO LLEVA LO QUE EL SUJETO LEE: `comm`, `cmdline` y un `cwd` que es un ENLACE. Un
#   fixture con `cwd` como fichero normal no prueba nada, porque el gate usa `readlink`.
#
# ⛔ ESTA PATA ES ADVISORY, ASÍ QUE LA BATERÍA ASERTA LA SALIDA Y NO SÓLO EL CÓDIGO. Un gate que
#    siempre sale 0 tiene una batería que siempre pasa si sólo mira el rc: no probaría NADA, y el
#    día que se promueva a rechazar nadie sabría si detectaba. Cada caso dice qué cuenta espera ver
#    NOMBRADA en la salida, que es lo que el gate de verdad afirma.
#
# Salida: 0 todas pasan · 1 alguna falla · 2 no se pudo montar el banco.
set -uo pipefail
LC_ALL=C
export LC_ALL

RAIZ="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)"
GATE="$RAIZ/scripts/check-fast-lane-ceiling.sh"
[ -r "$GATE" ] || {
	echo "test-fast-lane-ceiling: ⛔ NO HE PODIDO MIRAR: no existe $GATE" >&2
	exit 2
}
BANCO="$(mktemp -d "${TMPDIR:-/tmp}/flc-XXXXXX")" || exit 2
trap 'rm -rf "$BANCO"' EXIT

pasan=0
fallan=0
comprobar() {
	if [ "$3" -eq "$2" ]; then
		printf '  ok    %-58s rc=%s\n' "$1" "$3"
		pasan=$((pasan + 1))
	else
		printf '  FALLA %-58s rc=%s (quiere %s)\n' "$1" "$3" "$2"
		fallan=$((fallan + 1))
	fi
}

# cuenta <rótulo> <ajenos esperados>  — lo que el gate AFIRMA, que es lo que hay que probar en una
# pata advisory. Ancla en la cifra Y en el sustantivo: un grep de un número solo casaría el techo.
cuenta() {
	if grep -qE "(AVISO — $2 carriles|OK — $2 carriles)" "$BANCO/out.log"; then
		printf '  ok    %-58s dice %s ajenos\n' "$1" "$2"
		pasan=$((pasan + 1))
	else
		printf '  FALLA %-58s no dice %s: %s\n' "$1" "$2" "$(head -1 "$BANCO/out.log")"
		fallan=$((fallan + 1))
	fi
}

# proceso <raiz-proc> <pid> <comm> <cmdline> <cwd>
proceso() {
	d="$1/$2"
	mkdir -p "$d"
	printf '%s\n' "$3" >"$d/comm"
	printf '%s\0' $4 >"$d/cmdline"
	mkdir -p "$5"
	ln -sfn "$5" "$d/cwd"
}

nuevo_proc() {
	rm -rf "$BANCO/proc"
	mkdir -p "$BANCO/proc"
	printf '%s' "$BANCO/proc"
}

correr() {
	OLIVARES_FAST_LANE_PROC="$1" \
		OLIVARES_FAST_LANE_SELF="$2" \
		OLIVARES_FAST_LANE_CEILING="$3" \
		bash "$GATE" >"$BANCO/out.log" 2>&1
}

MIO="$BANCO/mi-arbol"

# ── 1 · SUELO: caja vacía ⇒ pasa ──────────────────────────────────────────────────────────────
P="$(nuevo_proc)"
correr "$P" "$MIO" 3
comprobar "una caja sin carriles ajenos pasa" 0 "$?"
cuenta "y lo dice: cero ajenos" 0

# ── 2 · POR DEBAJO DEL TECHO ──────────────────────────────────────────────────────────────────
P="$(nuevo_proc)"
proceso "$P" 101 task "task lint:export" "$BANCO/carril-a"
proceso "$P" 102 task "task lint:spdx" "$BANCO/carril-b"
correr "$P" "$MIO" 3
comprobar "dos carriles ajenos con techo 3 pasan" 0 "$?"

# ── 3 · EN EL TECHO: se rechaza AL LLEGAR, no al pasarse ──────────────────────────────────────
proceso "$P" 103 task "task lint:nul" "$BANCO/carril-c"
correr "$P" "$MIO" 3
comprobar "tres carriles ajenos: advisory, no para el push" 0 "$?"
cuenta "y los NOMBRA: tres ajenos" 3

# ── 4 · EL MUTANTE QUE TRAJO ESTE GATE: el censo NO se cuenta a sí mismo ───────────────────────
# Si el gate contara su propio carril, el PRIMER push de una caja vacía se rechazaría a sí mismo.
P="$(nuevo_proc)"
proceso "$P" 201 task "task lint:export" "$MIO"
proceso "$P" 202 task "task lint:spdx" "$MIO"
proceso "$P" 203 task "task lint:nul" "$MIO/subdir"
correr "$P" "$MIO" 1
cuenta "tres procesos MÍOS no cuentan: cero ajenos" 0

# ── 5 · LA TRAMPA DEL PREFIJO ─────────────────────────────────────────────────────────────────
# una raiz es prefijo del nombre de OTRO carril que empieza igual.
# Sin la barra en la comparación, el gate se tragaría un carril ajeno como propio.
P="$(nuevo_proc)"
proceso "$P" 301 task "task lint:export" "${MIO}-otro"
correr "$P" "$MIO" 1
cuenta "un carril cuyo nombre EMPIEZA por el mío SÍ cuenta" 1

# ── 6 · LA UNIDAD ES EL CARRIL, NO EL PROCESO ─────────────────────────────────────────────────
# Un gancho lanza varias tareas a la vez; contarlas por separado daría el doble y rechazaría a
# quien no toca.
P="$(nuevo_proc)"
mkdir -p "$BANCO/carril-a/sub"
git -C "$BANCO/carril-a" init -q 2>/dev/null || true
proceso "$P" 401 task "task lint:export" "$BANCO/carril-a"
proceso "$P" 402 task "task lint:export:legs" "$BANCO/carril-a"
proceso "$P" 403 task "task lint:spdx" "$BANCO/carril-a/sub"
correr "$P" "$MIO" 2
cuenta "tres procesos del MISMO carril ajeno cuentan como UNO" 1

# ── 7 · EL MUTANTE DE LA AUTO-COINCIDENCIA ────────────────────────────────────────────────────
# La shell que censa lleva el patrón en su PROPIA cmdline. Con una sonda de una etapa se contaría
# a sí misma: 9 procesos frente a 5 reales el 2026-09-01. El filtro por `comm` es lo que lo corta.
P="$(nuevo_proc)"
proceso "$P" 501 zsh "grep task lint: /proc" "$BANCO/carril-z"
proceso "$P" 502 bash "bash scripts/check-x.sh task lint:algo" "$BANCO/carril-y"
correr "$P" "$MIO" 1
cuenta "una shell con el patrón en su cmdline NO es un carril" 0

# ── 8 · UN `task` QUE NO ES UN GANCHO ─────────────────────────────────────────────────────────
P="$(nuevo_proc)"
proceso "$P" 601 task "task build:go" "$BANCO/carril-w"
correr "$P" "$MIO" 1
cuenta "un task que no corre lint: no es un carril rápido" 0

# ── 9 · TECHO 0: rechaza siempre, y es la forma de apagar la puerta a propósito ────────────────
P="$(nuevo_proc)"
correr "$P" "$MIO" 0
cuenta "referencia 0 sobre caja vacía sigue diciendo cero ajenos" 0

# ── 10 · LAS TRES RESPUESTAS: lo que no se puede mirar NO es verde ─────────────────────────────
correr "$P" "$MIO" "tres"
comprobar "un techo que no es un entero es NO HE PODIDO MIRAR" 2 "$?"

correr "$BANCO/proc-que-no-existe" "$MIO" 3
comprobar "un /proc ausente es NO HE PODIDO MIRAR, nunca limpio" 2 "$?"

# Sin raíz propia el gate no puede excluirse a sí mismo. Contestar 0 ahí sería un verde a ciegas y
# contestar 1 rechazaría a todo el mundo: es una medición que no se ha podido hacer.
(
	cd "$BANCO" || exit 2
	OLIVARES_FAST_LANE_PROC="$BANCO/proc" OLIVARES_FAST_LANE_SELF="" OLIVARES_FAST_LANE_CEILING=3 \
		GIT_CEILING_DIRECTORIES="$BANCO" bash "$GATE" >"$BANCO/out.log" 2>&1
)
comprobar "sin saber cuál es mi árbol es NO HE PODIDO MIRAR" 2 "$?"

echo "test-fast-lane-ceiling: $pasan pasan, $fallan fallan"
[ "$fallan" -eq 0 ] || exit 1
exit 0
