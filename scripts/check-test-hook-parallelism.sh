#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-test-hook-parallelism.sh — envoltorio del gate. La regla vive en scripts/hookpar (go/ast).
#
# ⛔ POR QUÉ, Y NO ES HIGIENE: un test que asigna una VARIABLE DE PAQUETE dentro de un ámbito
# paralelo no controla quién la dispara. Si otro test del mismo paquete corre a la vez y alcanza
# ese punto, escribe en la variable del primero, y su aserción juzga entonces el gasto, el commit
# o el reloj DE OTRO TEST. Un número ajeno se parece exactamente a uno propio.
#
# ⛔ POR QUÉ ESTO YA NO ES AWK, y se decidió midiendo, no por gusto.
#
# La implementación anterior reconstruía el léxico de Go a mano. Recibió TRES arreglos correctos
# seguidos —comentario de línea, bloque /* */, cadena raw multilínea— y cada uno cerró su caso sin
# cerrar la CLASE, porque los tres eran el mismo error: reimplementar un analizador. Y quedaban dos
# aproximaciones estructurales:
#
#   ÁMBITO PARALELO por SANGRÍA. Guardaba la indentación mínima a la que había visto un
#   `t.Parallel()` y marcaba las asignaciones a sangría >=. **Falso negativo MEDIDO el 2026-08-25**:
#
#       if testing.Short() {
#           t.Parallel()          <- sangria 2
#       }
#       ganchoObservado = "x"     <- sangria 1  =>  sang < sang_par  =>  NO lo marcaba
#
#   El test SÍ es paralelo a partir de esa llamada. Viejo rc=0, nuevo rc=1. Está fijado como
#   caso de regresión en scripts/hookpar/main_test.go.
#
#   VARIABLE DE PAQUETE por RESTA. Recogía las declaradas con `:=` en la función y restaba, lo que
#   confunde una local de un bloque interior, un parámetro y un named return. Ahora la pertenencia
#   sale de las declaraciones reales del paquete y `=` frente a `:=` es un TOKEN.
#
# Medido el mismo día sobre el árbol entero: los dos gates coinciden (300 paquetes, 1873 ficheros
# de test, 0 hallazgos) y el nuevo tarda **4 s contra 23 s**.
#
# Tres respuestas: 0 limpio · 1 hallazgo · 2 NO HE PODIDO MIRAR. Un árbol sin Go pasa y es correcto;
# lo que NO puede hacer es pasar sin haber podido leerlo.
set -euo pipefail

cannot() { echo "check-test-hook-parallelism: ⛔ NO HE PODIDO MIRAR: $*" >&2; exit 2; }

# ⛔ EL SUJETO Y LA HERRAMIENTA SON DOS RAÍCES DISTINTAS, y confundirlas fue un defecto real de
# la primera versión de este envoltorio: `OLIVARES_ROOT` es lo que la BATERÍA apunta a un árbol de
# fixtures, así que buscar ahí el analizador lo hacía «no he podido mirar» en cada caso. La fuente
# se resuelve SIEMPRE desde la ubicación de este guion; el sujeto, desde `OLIVARES_ROOT`.
AQUI="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)" || cannot "no resuelvo mi propia ubicación"
# El sujeto se acepta de TRES formas, y el orden importa: argumento posicional (es como lo llama
# scripts/test-test-hook-parallelism.sh, y omitirlo hizo que sus veinte casos midieran el repo real
# en vez del fixture — veinte verdes que no probaban nada), luego OLIVARES_ROOT, luego yo mismo.
RAIZ="${1:-${OLIVARES_ROOT:-$AQUI}}"
[ -d "$RAIZ" ] || cannot "no existe el sujeto $RAIZ"

command -v go >/dev/null || cannot "no hay toolchain de Go para el analizador"
FUENTE="$AQUI/scripts/hookpar"
[ -r "$FUENTE/go.mod" ] && [ -r "$FUENTE/main.go" ] || cannot "falta el analizador bajo scripts/hookpar"

_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base" || cannot "no puedo crear $_tmp_base"
_bin="$(mktemp "$_tmp_base/hookpar.XXXXXX")" || cannot "no puedo reservar el binario"
trap 'rm -f "$_bin"' EXIT

# GOWORK=off: el módulo es deliberadamente independiente del workspace —sólo stdlib— para que
# construirlo no arrastre el grafo del monorepo ni lo contamine. Mismo patrón que hcl-module-guard.
if ! (cd "$FUENTE" || exit 2; GOWORK=off go build -o "$_bin" .); then
	cannot "no puedo construir el analizador"
fi

rc=0
salida="$("$_bin" -raiz "$RAIZ" 2>&1)" || rc=$?
printf '%s\n' "$salida" >&2
case "$rc" in
0)
	# La palabra «limpio» va a propósito: es el patrón que busca la batería, y cambiarla
	# habría roto veinte casos de cobertura léxica heredada sin que nada lo dijera.
	echo "check-test-hook-parallelism: limpio — sin asignaciones a var de paquete en ámbito paralelo."
	exit 0
	;;
1 | 2) exit "$rc" ;;
*) cannot "el analizador salió con $rc, que no es ninguna de las tres respuestas" ;;
esac
