#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ¿Siguen SERVIDOS los ADR en el sitio publico de documentacion?
#
# ⛔ POR QUE EXISTE, y no lo cubre ningun gate de arbol. El 2026-08-25 dos carriles cortamos los
# ADR: uno retiro las paginas de la FUENTE, el otro los saco del EXPORT. Los dos gates dieron
# verde — «0 de 205 ADR en el arbol exportado»— y el registro entero seguia devolviendo 200 en el
# sitio vivo, los seis idiomas y la instantanea archivada incluidos.
#
# ⇒ *Lo que deja de exportarse no es lo que deja de estar servido.* Un sitio publicado tiene su
#   propia vida: hasta que alguien despliega, lo ya desplegado sigue ahi. La UNICA medida valida de
#   «ya no es publico» es una peticion al sitio.
#
# ⛔ NO SE CABLEA EN EL HOOK NI EN EL CI. Un gate de push que sale a la red es un gate que falla
# por causas que no son del cambio. Esto se corre A MANO, o desde un job propio con su cadencia.
#
# LAS TRES RESPUESTAS, y la tercera es la razon de ser del guion:
#   0  ninguna ruta ADR responde 200         -> la exposicion esta cerrada
#   1  al menos una responde 200             -> SIGUE PUBLICO, con la lista
#   2  no he podido mirar                    -> sin red, sin herramienta, o el CONTROL POSITIVO cayo
#
# ⛔⛔ EL CONTROL POSITIVO ES LO QUE HACE QUE ESTO VALGA. Si el sitio entero esta caido, TODAS las
# rutas dan error y un guion ingenuo cantaria «cerrado» — el veredicto comodo, y falso. Por eso se
# pide primero una ruta que TIENE que responder 200; si esa no responde, no se mira nada mas y se
# devuelve 2. Sin este control, «todo 404» no distingue «los quitamos» de «no llego al sitio».
set -uo pipefail

SITIO="${OLIVARES_DOCS_SITE:-https://docs.olivares.ai}"
CONTROL="${OLIVARES_DOCS_CONTROL:-/}"

say() { printf '%s\n' "$*"; }
no_puedo() { say "check-adr-not-served: ⛔ NO HE PODIDO MIRAR — $*" >&2; exit 2; }

command -v curl >/dev/null || no_puedo "no hay curl en esta caja"

# codigo(): imprime SOLO el codigo HTTP, o la cadena vacia si ni siquiera hubo respuesta.
# --max-time acota; -o /dev/null descarta el cuerpo; -s -S deja pasar el error a stderr.
codigo() {
	local url="$1" c
	c="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 20 -L "$url" 2>/dev/null)" || return 1
	printf '%s' "$c"
}

# --- CONTROL POSITIVO, antes que nada ---
_ctrl="$(codigo "${SITIO}${CONTROL}")" || _ctrl=""
case "$_ctrl" in
	200) : ;;
	"")  no_puedo "el control positivo ${SITIO}${CONTROL} no dio ni respuesta (sin red, DNS, o sitio caido)" ;;
	*)   no_puedo "el control positivo ${SITIO}${CONTROL} devolvio ${_ctrl}, no 200: no puedo distinguir «retirado» de «caido»" ;;
esac

# Rutas del registro ADR. Las seis raices de idioma mas la raiz y la instantanea archivada:
# es la poblacion que se midio servida el 2026-08-25.
RUTAS=(
	/explanation/adr/
	/de/explanation/adr/
	/es/explanation/adr/
	/fr/explanation/adr/
	/ja/explanation/adr/
	/ru/explanation/adr/
	/zh/explanation/adr/
	/2026-06/explanation/adr/
)

servidas=()
ilegibles=()
for r in "${RUTAS[@]}"; do
	c="$(codigo "${SITIO}${r}")" || c=""
	case "$c" in
		200)         servidas+=("${r} 200") ;;
		404|410)     : ;;
		"")          ilegibles+=("${r} sin-respuesta") ;;
		*)           ilegibles+=("${r} ${c}") ;;
	esac
done

# ⛔ Una ruta que no se pudo leer NO cuenta como retirada. Si queda alguna, el veredicto es 2
# aunque las demas esten limpias: un censo con un hueco no es un censo.
if [ "${#ilegibles[@]}" -gt 0 ]; then
	say "check-adr-not-served: ⛔ NO HE PODIDO MIRAR — ${#ilegibles[@]} ruta(s) sin veredicto:" >&2
	printf '             %s\n' "${ilegibles[@]}" >&2
	say "             El control positivo SI respondio 200, asi que el sitio esta en pie." >&2
	exit 2
fi

if [ "${#servidas[@]}" -gt 0 ]; then
	say "check-adr-not-served: ⛔ ${#servidas[@]} de ${#RUTAS[@]} rutas ADR SIGUEN SERVIDAS en ${SITIO}:" >&2
	printf '             %s\n' "${servidas[@]}" >&2
	say "             Cortar la fuente no retira lo ya desplegado: hace falta un DESPLIEGUE." >&2
	exit 1
fi

say "check-adr-not-served: OK — 0 de ${#RUTAS[@]} rutas ADR servidas en ${SITIO} (control positivo ${CONTROL} = 200)."
exit 0
