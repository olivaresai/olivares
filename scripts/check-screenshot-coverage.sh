#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-screenshot-coverage.sh — cuántas rutas de la consola NO tienen captura publicada.
#
# ⛔ POR QUÉ NO BASTA `check-screenshot-freshness.sh`, y no es un solape: son preguntas distintas y
#    sólo una de ellas se estaba haciendo.
#
#      frescura  → «¿lo que TENEMOS está rancio?»   · hoy: 0 por detrás, verde
#      cobertura → «¿FALTA algo?»                   · hoy: 40 de 52 rutas sin ninguna captura
#
#    Medido el 2026-08-18: el gate de frescura sale VERDE con 40 rutas sin captura, porque una ruta
#    que no tiene captura **no está obsoleta: está ausente**, y un gate que recorre lo que existe no
#    puede ver lo que falta. Es la misma clase que el trinquete de phone-home, que por construcción
#    no detecta una divulgación AUSENTE porque busca frases prohibidas.
#
# ⭐ UN TECHO ES EL INSTRUMENTO CORRECTO AQUÍ, y conviene decir por qué, porque esta misma campaña
#    RETIRÓ un techo de la frescura por oscilar. La diferencia es el mecanismo: una captura envejece
#    SOLA, con cada commit de UI, así que su cuenta sube sin que nadie decida nada. Una ruta sin
#    captura sólo aparece cuando **alguien añade una ruta**, que es un acto deliberado. No oscila.
#
#    Prueba de que muerde: al añadir `/tenants` (C07-02) esta cuenta subió de 39 a 40, y este gate
#    la habría cazado en el mismo push. Se declara, no se asume.
#
# ⚠ LA RUTA MANDA, NO EL NOMBRE DE LA VISTA. Emparejar por el `id` del registro (`accessMap`) contra
#    el fichero (`access-map-drift.png`) cruza dos convenciones y da un falso ausente: medido al
#    escribirlo — un primer conteo por nombre dijo 29 sin captura y se equivocaba en las dos
#    direcciones. Se empareja por el `path`, que es lo que el navegador visita.
#
# Salida: la lista de rutas sin captura, siempre · rc 0 verde · 1 subió · 2 NO HE PODIDO MIRAR.
set -uo pipefail
LC_ALL=C
export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ" 2>/dev/null || {
	echo "check-screenshot-coverage: ⛔ NO HE PODIDO MIRAR: no existe $RAIZ" >&2
	exit 2
}

REG="web/src/features/registry.tsx"
CAPS="docs-site/public/console"
TECHO="${OLIVARES_SCREENSHOT_UNCOVERED_MAX:-40}"

[ -f "$REG" ] || {
	echo "check-screenshot-coverage: ⛔ NO HE PODIDO MIRAR: falta $REG" >&2
	exit 2
}
[ -d "$CAPS" ] || {
	echo "check-screenshot-coverage: ⛔ NO HE PODIDO MIRAR: falta $CAPS/" >&2
	exit 2
}

# ⛔ EL DENOMINADOR ES LA UNION DE LAS DOS FUENTES, Y ANTES ERA SOLO UNA.
# Este gate leia las rutas de `registry.tsx` (52) mientras `route-census.json` declara **58**.
# Las SEIS que faltaban no eran rutas menores: `/login`, `/setup`, `/accept-invite`, `/settings`,
# `/status-page` y `/session-viewer/$id` — el camino que recorre un cliente NUEVO antes de ver
# nada mas. Medido el 2026-08-18: ninguna de las seis tiene captura, y el gate cantaba
# «0 de 52 sin captura, techo 0», un pleno perfecto, porque no estaban en su cuenta.
#
# Un instrumento con un denominador mas corto que la realidad no es optimista: da por cubierto
# lo que ni siquiera mira. Ahora se unen las dos fuentes, de modo que ninguna puede esconder una
# ruta por si sola, y la deuda de seis queda DECLARADA en el techo en vez de invisible.
CENSO="${OLIVARES_ROUTE_CENSUS:-web/src/features/route-census.json}"
rutas_reg="$(grep -oE "^[[:space:]]+path: '(/[A-Za-z0-9/_-]*)'," "$REG" 2>/dev/null |
	sed -E "s/.*path: '([^']*)'.*/\1/")"
rutas_censo=""
if [ -r "$CENSO" ]; then
	rutas_censo="$(sed -n 's/.*"\(\/[A-Za-z0-9/_$-]*\)".*/\1/p' "$CENSO" 2>/dev/null)"
fi
rutas="$(printf '%s\n%s\n' "$rutas_reg" "$rutas_censo" | grep -v '^$' | sort -u)"
n_rutas="$(grep -c . <<<"$rutas" || true)"
# CONTROL POSITIVO en las DOS fuentes: cero rutas o cero capturas no es un verde, es no haber
# mirado. Un censo vacío aprueba cualquier cosa, que es el defecto que este gate viene a cerrar.
if [ "${n_rutas:-0}" -lt 10 ]; then
	echo "check-screenshot-coverage: ⛔ NO HE PODIDO MIRAR: el registro dio $n_rutas rutas (<10)." >&2
	exit 2
fi
n_caps="$(find "$CAPS" -name '*.png' -type f 2>/dev/null | wc -l | tr -d ' ')"
if [ "${n_caps:-0}" -eq 0 ]; then
	echo "check-screenshot-coverage: ⛔ NO HE PODIDO MIRAR: cero PNG en $CAPS/." >&2
	exit 2
fi
bases="$(find "$CAPS" -name '*.png' -type f -printf '%f\n' 2>/dev/null |
	sed -E 's/-(light|dark)\.png$//; s/\.png$//' | sort -u)"

sin=""
n_sin=0
while IFS= read -r r; do
	[ -n "$r" ] || continue
	# ⛔ LOS SEGMENTOS PARAMETRICOS SE CAEN DEL SLUG, y sin esto una ruta parametrica NO PUEDE
	# estar cubierta nunca. El arnes nombra el fichero por el `id` de la vista (`session-viewer`),
	# porque una captura concreta usa UN id de sesion y publicar `session-viewer-01a01f52-….png`
	# seria atar el nombre del artefacto publico al sembrado. El slug salia de la RUTA
	# (`/session-viewer/$id` -> `session-viewer-$id`) y no casaba con ninguno de los dos lados:
	# ni con el fichero, ni con la variante `^slug-` (donde ademas `$id` es literal, no ancla).
	#
	# Medido el 2026-08-20 con las capturas ya publicadas: sin esta linea el gate decia «1 de 58 sin
	# captura» nombrando `/session-viewer/$id` **con sus dos PNG delante**. Una captura de
	# `/session-viewer/<un id>` ES la captura de `/session-viewer/$id`: la pantalla es la misma.
	slug="$(printf '%s' "$r" | sed -E 's#/\$[A-Za-z0-9_]+##g; s#^/##; s#/#-#g')"
	[ -n "$slug" ] || slug="home"
	# Cuenta como cubierta la captura de la ruta pelada Y la de un ESTADO suyo
	# (`access-map-drift` cubre `/access-map`): una captura de estado sigue siendo esa pantalla.
	# ⛔ HERE-STRING, NO TUBERÍA. `printf … | grep -q` mata al escritor con SIGPIPE en cuanto `grep`
	#    encuentra y sale: con salida pequeña cabe en el buffer de 64 KiB y no se nota, y en cuanto
	#    la lista crece devuelve **141 EN ÉXITO**. Es decir: pasa hasta que hay bastantes capturas,
	#    que es justo cuando este gate empieza a servir para algo.
	#
	#    Lo introduje yo hoy, en este mismo guion, y lo cazó `lint:sigpipe-booleans` — un trinquete
	#    por FICHERO cuyo rechazo lo paga **el siguiente que empuje**, no quien lo introdujo.
	if grep -qxF "$slug" <<<"$bases" || grep -qE "^${slug}-" <<<"$bases"; then
		continue
	fi
	sin="$sin$r"$'\n'
	n_sin=$((n_sin + 1))
done <<EOF
$rutas
EOF

echo "check-screenshot-coverage: $n_sin de $n_rutas ruta(s) sin captura (techo $TECHO) · $n_caps PNG en $CAPS/"
if [ "$n_sin" -gt 0 ]; then
	echo "  sin captura:"
	printf '%s' "$sin" | sed 's/^/    /'
fi

if [ "$n_sin" -gt "$TECHO" ]; then
	echo "check-screenshot-coverage: ⛔ SUBIÓ: $n_sin sin captura frente al techo $TECHO." >&2
	echo "  Una ruta nueva sin captura es legítima; que la cifra suba sin decirlo, no. Captúrala, o" >&2
	echo "  sube OLIVARES_SCREENSHOT_UNCOVERED_MAX en el Taskfile en el MISMO commit y di cuál es." >&2
	# ⛔ CON EL TECHO EN 0 (desde 2026-08-20) ESTE MENSAJE ES LO PRIMERO QUE VE QUIEN AÑADE UNA RUTA,
	# y hasta hoy decía «captúrala» sin decir CÓMO. Un trinquete que muerde sin dar el comando manda
	# a quien lo encuentra a leerse el arnés entero, y la salida cómoda pasa a ser subir el techo.
	echo "  Capturarla son dos pasos y ninguno es heroico:" >&2
	echo "    1) añade la ruta a VIEWS en web/e2e/docs-captures.spec.ts CON su \`heading\` — el h1 real" >&2
	echo "       de la vista. Sin él se guarda una captura del esqueleto y pasa cualquier comprobación" >&2
	echo "       de que el fichero existe (registry.capture-coverage.test.ts lo exige y lo explica)." >&2
	echo "    2) PUBLICAR=1 bash scripts/docs-captures.sh   — arranca los motores, fotografía y publica." >&2
	echo "  Si la ruta NO debe fotografiarse, decláralo en SIN_CAPTURA con el motivo EN LA MISMA LÍNEA," >&2
	echo "  nunca en un comentario suelto; y si el motivo se puede quitar, quítalo en vez de declararlo." >&2
	exit 1
fi
if [ "$n_sin" -lt "$TECHO" ]; then
	echo "check-screenshot-coverage: ✔ bajó a $n_sin — baja el techo en el mismo commit."
fi
echo "check-screenshot-coverage: ✔ la deuda de cobertura no sube"
