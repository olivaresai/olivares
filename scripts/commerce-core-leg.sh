#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
#
# commerce-core-leg.sh — la suite de commerce, con la TERCERA respuesta que le faltaba.
#
# ⛔ POR QUE EXISTE. El Taskfile viaja al export VERBATIM y `test:commerce-core` llevaba
# `dir: commercial/commerce`. El export CURA `commercial/` fuera (scripts/export-public.sh),
# asi que en el arbol publicado `task` fallaba antes de correr un solo comando, con el error
# de un directorio que no existe — la misma forma de fallo que hub-leg.sh existe para cortar:
# una pata que muere por estar en el arbol equivocado, no por lo que mide.
#
# Tres respuestas distinguibles, como en check-int-12-no-land.sh:37 y check-gate-parity.sh:346:
#
#   1. el modulo esta                    -> corre la suite y propaga SU codigo exacto
#   2. falta + el arbol clasifica public -> SCOPED, lo dice con su motivo, y sale 0
#   3. falta + cualquier otra cosa       -> sale 2: «no he podido mirar», nunca un verde
#
# El clasificador es el de hub-leg.sh —firma del generador MAS ausencia de todo camino
# hub-only—, no un fichero-marcador suelto: un marcador a pelo es una contraseña que
# cualquier copia teclea.
#
# Uso: commerce-core-leg.sh [--race]
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)

# export-closure: hub-only commercial/commerce/go.mod — el modulo comercial no viaja en el
# arbol publicado; este guion comprueba su AUSENCIA para clasificar, y nunca lo importa.
# ⛔ Se declara el FICHERO y no el directorio: el verificador de cierre resuelve la ruta
# declarada contra el hub y un directorio le da «no such path exists» — medido, rc=1.
MOD="$ROOT/commercial/commerce"

if [ ! -d "$MOD" ]; then
	if [ "$(bash "$ROOT/scripts/hub-leg.sh" --classify --root "$ROOT" 2>/dev/null)" = "public" ]; then
		printf '%s\n' "commerce-core: SCOPED — public export; commercial/commerce is curated out."
		printf '%s\n' "  The commercial module is excluded, so this suite does not apply to this checkout."
		exit 0
	fi
	printf '%s\n' "commerce-core: COULD NOT LOOK — $MOD is missing and this tree is NOT a stamped export." >&2
	printf '%s\n' "  A missing module outside the export indicates a broken tree; it cannot be reported as passing." >&2
	exit 2
fi

RACE=""
[ "${1:-}" = "--race" ] && RACE="-race"
cd "$MOD" || exit 2
# GOWORK=off: el modulo comercial no esta en el go.work publico.
GOWORK=off exec bash "$ROOT/scripts/with-pg-env.sh" go test ${RACE} -count=1 -timeout 10m ./...
