#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md.
#
# check-docs-redirects.sh — puerta de TRES RESPUESTAS sobre las redirecciones de cortesia de
# docs-site: 0 limpio · 1 hallazgo · 2 NO HE PODIDO MIRAR.
#
# ⛔ QUE PROPIEDAD CIERRA, y por que no es cosmetica. Una redireccion de cortesia existe para
#    convertir un 404 en una pagina util. Si su DESTINO no existe, no arregla nada: cambia un 404
#    por otro 404, y encima lo hace en silencio — el visitante ve una redireccion que "funciona"
#    y aterriza en el 404-page igual. Peor aun, el destino puede dejar de existir DESPUES: basta
#    con que alguien renombre o mueva una pagina y la cortesia se vuelve una trampa sin que nada
#    lo diga.
#
# ⛔ Y POR QUE MIRA EL FUENTE Y NO `dist/`. Construir docs-site cuesta minutos y necesita pnpm;
#    este gate corre en el carril rapido. Mira `src/content/docs`, que es de donde sale `dist`.
#    LIMITACION DECLARADA: si una pagina existe en el fuente pero el build la excluye, este gate
#    dice CLEAN y el destino seguiria roto. No cubre esa clase; la cubre el despliegue.
set -uo pipefail

ROOT="${OLIVARES_ROOT:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)}"
RED="${OLIVARES_DOCS_REDIRECTS:-$ROOT/docs-site/public/_redirects}"
DOCS="${OLIVARES_DOCS_CONTENT:-$ROOT/docs-site/src/content/docs}"

no_puedo() { printf 'check-docs-redirects: NO HE PODIDO MIRAR — %s\n' "$1" >&2; exit 2; }

[ -r "$RED" ]  || no_puedo "no puedo leer $RED"
[ -d "$DOCS" ] || no_puedo "no existe el arbol de contenido $DOCS"

# Limites de la plataforma (Workers static assets), citados donde se aplican.
MAX_REGLAS=2000
MAX_CHARS=1000

fallos=0
reglas=0
while IFS= read -r linea; do
	case "$linea" in ''|'#'*) continue ;; esac
	case "$linea" in /*) : ;; *) continue ;; esac
	reglas=$((reglas + 1))

	if [ "${#linea}" -gt "$MAX_CHARS" ]; then
		printf 'check-docs-redirects: FAIL — regla de %s caracteres, el limite es %s:\n  %s\n' \
			"${#linea}" "$MAX_CHARS" "${linea:0:80}..." >&2
		fallos=$((fallos + 1))
		continue
	fi

	# campo 2 = destino. `read` con IFS por defecto parte por espacios, que es lo que usa el formato.
	destino="$(printf '%s\n' "$linea" | awk '{print $2}')"
	[ -n "$destino" ] || { printf 'check-docs-redirects: FAIL — regla sin destino: %s\n' "$linea" >&2; fallos=$((fallos + 1)); continue; }

	# Un destino externo no se puede comprobar contra el arbol; se declara y se salta.
	case "$destino" in http://*|https://*) continue ;; esac

	# Normaliza: quita la barra inicial y la final, y el comodin de splat.
	p="${destino#/}"; p="${p%/}"; p="${p%/:splat}"
	[ -n "$p" ] && [ "$p" != "*" ] || continue

	if   [ -f "$DOCS/$p.md" ]        ; then :
	elif [ -f "$DOCS/$p.mdx" ]       ; then :
	elif [ -f "$DOCS/$p/index.md" ]  ; then :
	elif [ -f "$DOCS/$p/index.mdx" ] ; then :
	else
		printf 'check-docs-redirects: FAIL — el destino NO existe, la cortesia lleva a otro 404:\n' >&2
		printf '  regla:   %s\n  destino: %s\n  buscado: %s/{%s.md,%s.mdx,%s/index.md,%s/index.mdx}\n' \
			"$linea" "$destino" "$DOCS" "$p" "$p" "$p" "$p" >&2
		fallos=$((fallos + 1))
	fi
done < "$RED"

if [ "$reglas" -gt "$MAX_REGLAS" ]; then
	printf 'check-docs-redirects: FAIL — %s reglas, el limite de la plataforma es %s\n' "$reglas" "$MAX_REGLAS" >&2
	fallos=$((fallos + 1))
fi

# ⛔ CONTROL POSITIVO EN LA PROPIA PUERTA: cero reglas no es "limpio", es que no he mirado nada.
#    Sin esto, borrar el fichero de reglas o romper el bucle sale VERDE.
[ "$reglas" -gt 0 ] || no_puedo "no he leido ni una regla de $RED — un cero aqui no es limpieza"

[ "$fallos" -eq 0 ] || exit 1
printf 'check-docs-redirects: CLEAN — %s regla(s) de cortesia, todos los destinos existen.\n' "$reglas"
exit 0
