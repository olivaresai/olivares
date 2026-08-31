#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-connector-onboard.sh — un conector cableado está OFRECIDO y aparece en la página pública.
#
# ⛔ POR QUÉ EXISTE, con el caso que lo trajo y la aritmética que lo justifica. El 2026-08-18
#    aterrizó `connectors/grok` con su código, sus celdas, su registro en `sources.go` y **los tres
#    gates de Go en verde** (`lint:connectors`, `lint:boundary`, `lint:spdx`). Traía DOS defectos que
#    ninguno de ellos mira:
#
#      · cableado en `buildInProcSource` y **NO ofrecido** en `inProcConnectorKinds` — se podía
#        construir y no se ofrecía nunca;
#      · **ausente** de `docs-site/…/reference/connectors.md` — la página pública anunciaba todos los
#        tipos cableados menos ése.
#
#    Los dos vivieron en `main` hasta que el carril de integración corrió el gate COMPLETO sobre un árbol
#    anclado. Las pruebas que los cazan —`TestConnectorCatalogCoversSwitch` y
#    `TestConnectorDocsListEveryWiredKind`— existen y son correctas; el problema es **cuándo** corren:
#    sólo dentro de `go test ./...` de `cmd/olivares`, que en esa corrida costó **4 484 s**.
#
# ⇒ Este gate NO reimplementa nada. Corre EXACTAMENTE esas dos pruebas, que siguen siendo la
#   autoridad, y cuesta **13 s** medidos (la compilación; las pruebas en sí, 0,034 s). Trece segundos
#   en el carril rápido contra hora y cuarto en el pesado, para la clase de defecto que sólo aparece
#   cuando alguien añade un conector — es decir, exactamente cuando el carril rápido es lo único que
#   se corre.
#
# ⛔⛔ Y EL CONTROL POSITIVO ES LO QUE HACE QUE ESTO NO SEA DECORATIVO. `go test -run <regex>` con un
#     patrón que no casa NADA sale **0**: si alguien renombra una de las dos pruebas, este gate
#     seguiría verde para siempre sin ejecutar una sola aserción — la sonda que contesta lo mismo
#     para cualquier entrada. Por eso se corre con `-v` y se EXIGE ver el `--- PASS:` de cada una por
#     su nombre. Sin las dos líneas, el veredicto es 2, no 0.
#
# Salida: 0 las dos pruebas corrieron y pasaron · 1 alguna falló · 2 NO HE PODIDO MIRAR.
set -uo pipefail
LC_ALL=C
export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ/cmd/olivares" 2>/dev/null || {
	echo "check-connector-onboard: ⛔ NO HE PODIDO MIRAR: no existe $RAIZ/cmd/olivares" >&2
	exit 2
}

PRUEBAS="TestConnectorCatalogCoversSwitch|TestConnectorDocsListEveryWiredKind"
salida="$(go test -run "$PRUEBAS" -count=1 -v . 2>&1)"
rc=$?

# ── El control positivo, ANTES de mirar el rc ────────────────────────────────────────────
# Se comprueba que CADA prueba se ejecutó, por su nombre. Un `-run` que no casa nada, un paquete
# que no compila con `-run` filtrado, o una prueba renombrada, salen todos por aquí y NUNCA por 0.
faltan=""
for t in TestConnectorCatalogCoversSwitch TestConnectorDocsListEveryWiredKind; do
	# ⛔ AQUI HABIA UNA TUBERIA, y bajo `pipefail` devuelve 141 EN EXITO: `grep -q` cierra su
	# entrada al primer acierto, `printf` recibe SIGPIPE, y el codigo de la tuberia pasa a ser
	# 141 — es decir, el caso que ACIERTA se lee como fallo. Es la trampa que `lint:sigpipe-booleans`
	# vigila, y que le costo una jornada a `check-egress-claims.sh`. La forma sin tuberia es una
	# here-string.
	grep -qE "^(--- )?(PASS|FAIL): +${t}\b" <<<"$salida" || faltan="${faltan} ${t}"
done
if [ -n "$faltan" ]; then
	echo "check-connector-onboard: ⛔ NO HE PODIDO MIRAR: estas pruebas no llegaron a ejecutarse:${faltan}" >&2
	echo "                        Un 'go test -run' que no casa nada sale 0, así que esto NO es un pase." >&2
	echo "                        ¿Las han renombrado o movido de paquete? Actualiza PRUEBAS aquí." >&2
	printf '%s\n' "$salida" | tail -12 | sed 's/^/  /' >&2
	exit 2
fi

if [ "$rc" -ne 0 ]; then
	echo "check-connector-onboard: ⛔ un conector cableado NO está ofrecido o NO está en la página pública:" >&2
	printf '%s\n' "$salida" | grep -E "^\s+--- FAIL|_test\.go:|cableado|wired" | head -12 | sed 's/^/  /' >&2
	echo "                        Ofrecer: inProcConnectorKinds en cmd/olivares. Publicar:" >&2
	echo "                        docs-site/src/content/docs/reference/connectors.md" >&2
	exit 1
fi

echo "check-connector-onboard: OK — los conectores cableados están ofrecidos y publicados (2 prueba(s) ejecutadas)."
exit 0
