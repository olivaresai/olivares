#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# render-download-contract.sh — emite a stdout los SIETE hechos de la puerta de descarga que los
# gates del OVERLAY necesitan, DERIVADOS de los .ts que son su autoridad.
#
# ⛔ POR QUE EXISTE. El overlay comprueba que su productor publica exactamente la clave que la
# puerta lee, y para eso lee `commercial/license-worker/src/download/{artifacts,sets}.ts` a traves
# de `hub-gate-contract.sh`. En el repo de DESARROLLO su submodulo `public/` apunta al hub y los
# ficheros estan; en el ESPEJO publicado apunta al export, que retira `commercial/` ENTERO
# (`export-public.sh` TOP_BLOCK). Resultado medido el 2026-08-30: dos de las quince patas del
# espejo no pueden mirar — `lint:set-producer` (su unico rojo) y `lint:release-manifests`, que
# habria fallado detras si se curaba solo la primera.
#
# ⛔ Y POR QUE VIAJA EL CONTRATO Y NO LA FUENTE. Los dos .ts son `LicenseRef-Olivares-Commercial`
# y `check-spdx.sh:58` dice de `commercial/`: «internal fulfilment backend; never exported».
# Exportarlos seria relicenciar fuente comercial, que no es una decision de gate. Aqui viajan siete
# HECHOS bajo AGPL, no codigo.
#
# ⛔ GENERADO, NUNCA TRANSCRITO — y no es una preferencia: `sets.ts:24-25` cuenta el episodio con
# su cifra, «anadir un quinto add-on habria dejado ALLOWED_SET_SLUGS corto (17 en vez de 33) — 404
# para sus compradores — con el test EN VERDE, porque comprobaba el acuerdo con su propia copia».
# Por eso `check-download-contract.sh` regenera y compara en vez de leer una lista escrita a mano.
set -uo pipefail
blind() { printf 'render-download-contract: NO HE PODIDO MIRAR — %s\n' "$*" >&2; exit 2; }
ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
D="$ROOT/commercial/license-worker/src/download"
A="$D/artifacts.ts"; T="$D/sets.ts"
for f in "$A" "$T"; do [ -r "$f" ] || blind "no puedo leer $f, que es la AUTORIDAD del contrato"; done

PREFIX="$(command grep -oE 'ARTIFACT_BASENAME_PREFIX = "[^"]+"' "$A" | command grep -oE '"[^"]+"' | tr -d '"')"
[ -n "$PREFIX" ] || blind "no pude leer ARTIFACT_BASENAME_PREFIX de artifacts.ts"

KEY="$(command grep -oE 'return `enterprise/\$\{[^`]*`' "$A" | head -1)"
[ -n "$KEY" ] || blind "no pude leer la forma de la clave de artifactKey()"
KEY="${KEY#return \`}"; KEY="${KEY%\`}"

# Misma doctrina de anclaje que abajo: la primera plantilla con backticks de artifacts.ts es la
# CLAVE (:68), que MENCIONA artifactFilename dentro. El hecho se toma del `return` que sigue a su
# DECLARACION, no del primero que aparezca — medido: el ancla ingenua da la clave, no el nombre.
FNAME="$(awk '/^export function artifactFilename/{f=1} f&&/return `/{print; exit}' "$A" \
	| command grep -oE '`[^`]*`' | head -1)"
[ -n "$FNAME" ] || blind "no pude leer la forma del nombre de fichero de artifactFilename()"
FNAME="${FNAME#\`}"; FNAME="${FNAME%\`}"

# ⛔ ANCLADO EN LA DECLARACION, NO EN LA MENCION. La primera aparicion de ALLOWED_SET_SLUGS en
# sets.ts es un COMENTARIO (:24): arrancar ahi captura ADDON_CODES y BASE_CODE y devuelve seis
# slugs en vez de diecisiete. Medido al escribir esto.
#
# ⛔ Y UN SOLO LECTOR, QUE ES EL DE SUS CONSUMIDORES. La primera version de este guion usaba un
# extractor propio, mas laxo (`"[a-z0-9][a-z0-9+_-]*"` sobre el bloque entero). Medido el
# 2026-08-30: con `sets.ts` de hoy los dos dan 17, pero renombrando un slug a `biz+reg2` —
# TypeScript perfectamente valido — el laxo ve 17 y el de `set-sku-matrix.sh` ve 16. Un contrato
# generado con el lector laxo y consumido por el estricto es la reaparicion de F02 por la puerta
# de atras: la lista corta viaja al espejo y su productor publica un conjunto menos, en verde.
# `set-sku-matrix.sh:52` ya lo dice de sus dos lectores — «dos lectores distintos de la misma
# autoridad no pueden discrepar sobre que es un slug» — y este es el tercero.
BLOQUE="$(sed -n '/^export const ALLOWED_SET_SLUGS/,/^]);/p' "$T")"
[ -n "$BLOQUE" ] || blind "no encuentro el bloque ALLOWED_SET_SLUGS en sets.ts"
mapfile -t _SLUGS < <(printf '%s\n' "$BLOQUE" |
	command grep -oE '^[[:space:]]+"[a-z+]+",?$' | command grep -oE '"[a-z+]+"' | tr -d '"')
[ "${#_SLUGS[@]}" -gt 0 ] || blind "0 slugs leidos de sets.ts: la extraccion fallo o la puerta cambio de forma"

# ⛔ Y LA GUARDA DE COMPLETITUD, QUE ES LA QUE HACE EL CONTRATO FIABLE. El espejo no tiene la
# fuente: no puede comprobar que la lista este ENTERA, solo puede creerse la que le llega. Asi que
# la completitud se comprueba AQUI, donde la fuente esta. Se cuenta lo que el bloque DECLARA —
# lineas que no son apertura, cierre, vacia ni comentario — y tiene que casar con lo entendido.
# Si el parser entiende menos, NO se genera contrato: rc=2. Una lista leida a medias no es una lista.
DECLARADAS="$(printf '%s\n' "$BLOQUE" | sed -e '1d' -e '$d' |
	command grep -vE '^[[:space:]]*(//|/\*|\*|$)' | command grep -c '[^[:space:]]')"
[ "$DECLARADAS" = "${#_SLUGS[@]}" ] || blind "el bloque ALLOWED_SET_SLUGS declara $DECLARADAS entrada(s) y solo entendi ${#_SLUGS[@]}.
  Generar el contrato con la lista corta lo publicaria al espejo, cuyo productor construiria un
  conjunto menos EN VERDE y devolveria 404 a sus compradores. Revisa sets.ts: comillas simples,
  un digito o un guion en un codigo, o un formato multilinea son ediciones validas que este
  extractor NO reconoce."
SLUGS="${_SLUGS[*]}"

# Los add-ons, la base y el codigo enterprise: `set-sku-matrix.sh` los necesita para componer los
# SKUs y para su correspondencia en los dos sentidos (todo codigo de un slug es BASE o add-on, y
# todo add-on aparece en algun slug). Sin ellos el espejo tendria los slugs y no podria validarlos.
ADDON_DECL="$(sed -n 's/^export const ADDON_CODES = \[\(.*\)\] as const;/\1/p' "$T")"
[ -n "$ADDON_DECL" ] || blind "ADDON_CODES no esta en la forma de una sola linea que este extractor sabe leer; no adivino los add-ons"
mapfile -t _ADDONS < <(printf '%s\n' "$ADDON_DECL" | command grep -oE '"[a-z]+"' | tr -d '"')
_N_COMAS="$(printf '%s' "$ADDON_DECL" | tr -cd ',' | wc -c | tr -d ' ')"
[ "$((_N_COMAS + 1))" = "${#_ADDONS[@]}" ] || blind "ADDON_CODES declara $((_N_COMAS + 1)) codigo(s) y solo entendi ${#_ADDONS[@]}: un add-on que no se entiende deja su tag fuera de TODOS los builds"
ADDONS="${_ADDONS[*]}"

BASE="$(sed -n 's/^export const BASE_CODE = "\([a-z]*\)";/\1/p' "$T")"
ENTC="$(sed -n 's/^export const ENTERPRISE_CODE = "\([a-z]*\)";/\1/p' "$T")"
[ -n "$BASE" ] && [ -n "$ENTC" ] || blind "no pude leer BASE_CODE/ENTERPRISE_CODE de sets.ts"

printf '# SPDX-FileCopyrightText: 2026 Olivares.AI\n'
printf '# SPDX-License-Identifier: AGPL-3.0-only\n'
printf '#\n'
printf '# GENERADO por scripts/render-download-contract.sh — NO editar a mano.\n'
printf '# Autoridad: commercial/license-worker/src/download/{artifacts,sets}.ts (no viajan: son\n'
printf '# LicenseRef-Olivares-Commercial). Lo que viaja son estos hechos, para que el arbol\n'
printf '# PUBLICADO pueda comprobar a su productor sin la fuente comercial.\n'
printf '# `lint:download-contract` regenera y compara: si divergen, es ROJO.\n'
printf 'artifact_basename_prefix=%s\n' "$PREFIX"
printf 'artifact_key_shape=%s\n' "$KEY"
printf 'artifact_filename_shape=%s\n' "$FNAME"
printf 'allowed_set_slugs=%s\n' "$SLUGS"
printf 'addon_codes=%s\n' "$ADDONS"
printf 'base_code=%s\n' "$BASE"
printf 'enterprise_code=%s\n' "$ENTC"
