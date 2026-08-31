#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-docs-site-live.sh — does the LIVE docs site keep the promises this tree makes?
#
# ⛔ THE DEFECT THIS EXISTS FOR, measured 2026-08-27 and not hypothetical.
# `docs-site/public/_redirects` landed on main (31e13c238) declaring
#   /cli/*        -> /reference/cli/     301
#   /quickstart/* -> /start/quickstart/  301
# derived from 35 measured external mentions. In production BOTH answered 404. The deploy of the
# docs site is manual, so main moved and the site did not, and NOTHING said so — the site can only
# be published by somebody remembering, and a site that depends on remembering goes stale in
# silence. The live build carried the ADR sweep of 08-25 22:56 and nothing after it.
#
# ⛔ AND THE EXPECTATIONS ARE DERIVED FROM THE TREE, NEVER TYPED HERE. A hardcoded list is the
# form of gate this repository has found broken most often: it certifies the drift it exists to
# catch the moment somebody edits `_redirects` and forgets the copy. Every redirect asserted below
# is PARSED out of docs-site/public/_redirects, and the structural pages are DISCOVERED from
# docs-site/src/content/docs/. If a rule is added to the tree, this gate starts demanding it with
# no edit here.
#
# THREE ANSWERS, AND THE CODE CARRIES THEM — never the prose (canon §1.5):
#   0  the live site agrees with the tree
#   1  it disagrees: the site is stale, or a promise is broken
#   2  I COULD NOT LOOK: no curl, no network, no source file. Not a pass.
#
# Usage:  bash scripts/check-docs-site-live.sh [--host docs.olivares.ai]
set -uo pipefail
export LC_ALL=C

HOST="docs.olivares.ai"
while [ $# -gt 0 ]; do
	case "$1" in
	--host)
		# ⛔ `shift 2 || true` NO TERMINA cuando falta el valor: shift falla, `|| true` se traga el
		# fallo, `$#` no baja y el bucle se repite para siempre. Un gate que se cuelga con un
		# argumento mal escrito es peor que uno que falla: no da veredicto y nadie sabe por qué.
		if [ $# -lt 2 ] || [ -z "${2:-}" ]; then
			echo "check-docs-site-live: ⛔ --host necesita un valor." >&2
			exit 2
		fi
		HOST="$2"
		shift 2
		;;
	*)
		echo "check-docs-site-live: unknown argument: $1" >&2
		exit 2
		;;
	esac
done

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
REDIRECTS="$RAIZ/docs-site/public/_redirects"
CONTENT="$RAIZ/docs-site/src/content/docs"

command -v curl >/dev/null 2>&1 || {
	echo "check-docs-site-live: ⛔ NO HE PODIDO MIRAR: no encuentro curl." >&2
	exit 2
}
[ -r "$REDIRECTS" ] || {
	echo "check-docs-site-live: ⛔ NO HE PODIDO MIRAR: falta $REDIRECTS." >&2
	exit 2
}
[ -d "$CONTENT" ] || {
	echo "check-docs-site-live: ⛔ NO HE PODIDO MIRAR: falta $CONTENT." >&2
	exit 2
}

# ⛔ A NON-BROWSER User-Agent gets a 1010 from workers.dev-class hosts, measured in this repo. A
# gate that trips on its own client is a gate that reports the site broken when it is not.
UA='Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36'
CURL=(curl -sS --max-time 20 -A "$UA" -o /dev/null)

# probe <url> -> prints "<http_code> <redirect_url>"; empty output means the request itself failed,
# which is a COULD-NOT-LOOK for that probe and never a finding about the site.
probe() {
	local url="$1" out
	out="$("${CURL[@]}" -w '%{http_code} %{redirect_url}' "$url" 2>/dev/null)" || return 1
	printf '%s\n' "$out"
}

hallazgos=0
ciego=0
nota() { printf '  %s\n' "$*"; }

echo "check-docs-site-live: https://$HOST"

# ---------------------------------------------------------------------------------------------
# 1 · POSITIVE CONTROL. Before believing any 404, prove this client can get a 200 out of the host.
#     Without it, "everything is 404" and "the network is down" are written the same way.
# ---------------------------------------------------------------------------------------------
control="$(probe "https://$HOST/")" || control=""
control_code="${control%% *}"
if [ -z "$control" ] || [ "$control_code" != "200" ]; then
	echo "check-docs-site-live: ⛔ NO HE PODIDO MIRAR: el control positivo https://$HOST/ dio" \
		"'${control:-sin respuesta}' en vez de 200. Sin él, un 404 no distingue un sitio roto de" \
		"una red caída." >&2
	exit 2
fi
nota "control positivo: / -> 200"

# ---------------------------------------------------------------------------------------------
# 2 · THE REDIRECTS THE TREE PROMISES. Parsed, not typed.
#     `_redirects` lines are: <source> <destination> [code]. Splats are matched by requesting the
#     splat-less prefix, which is what an external mention actually looks like (/cli, /quickstart).
# ---------------------------------------------------------------------------------------------
reglas=0
malformadas=0
# ⛔ `set -f` ANTES DEL BUCLE, y no es ceremonia: `set -- $linea` sin comillas hace expansión de
# RUTAS además de división en palabras, y las fuentes de `_redirects` llevan `*` por diseño
# (`/cli/*`). Probado en esta caja: con un directorio `cli/` que contenga `a` y `b`,
# `set -- "cli/*  /reference/cli/  301"` devuelve CUATRO campos —`cli/a cli/b /reference/cli/ 301`—
# y el destino pasa a ser `cli/b`. El gate compararía entonces contra un destino inventado y
# publicaría un hallazgo FALSO, o peor, daría por buena una comprobación que no hizo. Que hoy los
# patrones sean absolutos (`/cli/*`) y `/cli/` no exista es suerte, no una guarda.
set -f
while IFS= read -r linea; do
	case "$linea" in '' | '#'*) continue ;; esac
	# shellcheck disable=SC2086
	set -- $linea
	[ $# -ge 2 ] || { malformadas=$((malformadas + 1)); continue; }
	src="$1" dst="$2" code="${3:-301}"
	case "$src" in /*) ;; *) malformadas=$((malformadas + 1)); continue ;; esac
	# ⛔ UNA REGLA CON SPLAT SE EJERCE CON EL SPLAT. `/cli/*` y `/cli` son DOS reglas en
	# `_redirects` y este bucle probaba `/cli` para las dos — es decir, comprobaba la regla bare
	# dos veces y el comodín NUNCA. Cloudflare documenta que `*` consume con avidez el resto de la
	# ruta, así que que `/cli` redirija no dice nada de `/cli/algo`. Ahora cada regla se sondea en
	# su propia forma: la bare tal cual, y la de splat con un segmento detrás.
	case "$src" in
	*'/*') sonda="${src%\*}olivares-splat-probe" ;;
	*) sonda="${src%/}" ;;
	esac
	[ -n "$sonda" ] || continue
	reglas=$((reglas + 1))
	respuesta="$(probe "https://$HOST$sonda")" || {
		nota "⚠ NO HE PODIDO MIRAR $sonda (la petición falló)"
		ciego=$((ciego + 1))
		continue
	}
	got_code="${respuesta%% *}"
	got_loc="${respuesta#* }"
	if [ "$got_code" != "$code" ]; then
		nota "✗ $sonda -> $got_code (el árbol promete $code hacia $dst)"
		hallazgos=$((hallazgos + 1))
		continue
	fi
	# ⛔ EL DESTINO SE COMPARA POR RUTA, NO POR SUBCADENA. `*"$dst"*` aceptaba
	# `https://otro-host.example/reference/cli/basura` — código correcto y subcadena correcta no
	# son una Location correcta. El contraste reprodujo ese verde falso. Se extrae la RUTA de la
	# Location (quitando esquema+host si los lleva) y se compara entera, tolerando sólo la barra
	# final, que es la única variación que el propio sitio introduce.
	loc_host=""
	loc_path="$got_loc"
	case "$got_loc" in
	http://* | https://*)
		sin_esquema="${got_loc#*://}"
		loc_host="${sin_esquema%%/*}"
		loc_path="/${sin_esquema#*/}"
		[ "$sin_esquema" = "$loc_host" ] && loc_path="/"
		;;
	esac
	if [ -n "$loc_host" ] && [ "$loc_host" != "$HOST" ]; then
		nota "✗ $sonda -> $got_code hacia OTRO HOST ('$loc_host'), no hacia $HOST"
		hallazgos=$((hallazgos + 1))
		continue
	fi
	loc_path="${loc_path%%[?#]*}"
	if [ "$loc_path" = "$dst" ] || [ "$loc_path" = "${dst%/}" ] || [ "$loc_path" = "${dst%/}/" ]; then
		nota "✓ $sonda -> $got_code $loc_path"
	else
		nota "✗ $sonda -> $got_code pero hacia '$loc_path', no hacia '$dst'"
		hallazgos=$((hallazgos + 1))
	fi
done <"$REDIRECTS"
set +f
nota "reglas de _redirects verificadas: $reglas (líneas ilegibles: $malformadas)"
# ⛔ POSTCONDICIÓN: CERO REGLAS NO ES LIMPIO. Un `_redirects` vacío, truncado o enteramente
# malformado hacía que este bucle no comprobara NADA y el gate saliera 0 — «no he mirado» escrito
# como «está bien», que es el defecto que este repositorio persigue por encima de todos. Cero
# reglas es NO HE PODIDO MIRAR (2), y una línea que el gate no entiende es un hallazgo, porque
# Cloudflare IGNORA en silencio toda regla que no cumpla `source destination [code]`: una regla
# ilegible aquí es una redirección que en producción no existe.
if [ "$reglas" -eq 0 ]; then
	echo "check-docs-site-live: ⛔ NO HE PODIDO MIRAR: ninguna regla legible en $REDIRECTS." \
		"Un fichero vacío o malformado no es un sitio sin redirecciones: es un gate sin entrada." >&2
	exit 2
fi
if [ "$malformadas" -gt 0 ]; then
	nota "✗ $malformadas línea(s) de _redirects que este gate no puede leer — Cloudflare las ignora" \
		"en silencio, así que prometen una redirección que en producción no existe"
	hallazgos=$((hallazgos + 1))
fi

# ---------------------------------------------------------------------------------------------
# 3 · STRUCTURAL PAGES, DISCOVERED. Every top-level section of the English tree must resolve, and
#     every published locale root must resolve. Discovered from the filesystem so a new section is
#     demanded automatically — the property `console:walk` taught this repository to prefer
#     (canon §0-COBERTURA: a gate says what its DISCOVERY reaches).
# ---------------------------------------------------------------------------------------------
rutas=0
locales=""
for dir in "$CONTENT"/*/; do
	nombre="$(basename "$dir")"
	# 2026-06 is the frozen version snapshot; it is served, but it is not a section of the
	# current site and its own README forbids treating it as one.
	case "$nombre" in 2026-06) continue ;; esac
	# ⛔ LOCALES DESCUBIERTOS, NO TECLEADOS. Aquí había `case "$nombre" in de|es|fr|ja|ru|zh`, y
	# una lista escrita a mano es la forma de gate que este repositorio ha encontrado rota más
	# veces: el día que se publique un locale nuevo, el gate sigue verde sobre un árbol que ya no
	# cubre, y nada lo dice. Un directorio de locale se reconoce por lo que ES —lleva su propio
	# índice y su propio `how-to/`—, que es lo que lo distingue de una sección.
	if [ -f "$dir/index.mdx" ] && [ -d "$dir/how-to" ]; then
		locales="${locales}${nombre}
"
		continue
	fi
	# ⛔ ONLY sections that ACTUALLY BUILD a root page. Starlight emits a page per content file;
	# a section without index.md/index.mdx has no root and 404s BY DESIGN — measured 2026-08-27:
	# /how-to/, /start/ and /tutorials/ have no index in the tree and answer 404 in production,
	# while /explanation/ and /reference/ do have one and answer 200. Demanding all five would
	# have made this gate report three permanent findings that no deploy can ever clear, which is
	# how a gate teaches its readers to ignore it.
	if [ ! -f "$dir/index.md" ] && [ ! -f "$dir/index.mdx" ]; then
		continue
	fi
	rutas=$((rutas + 1))
	respuesta="$(probe "https://$HOST/$nombre/")" || {
		nota "⚠ NO HE PODIDO MIRAR /$nombre/"
		ciego=$((ciego + 1))
		continue
	}
	got_code="${respuesta%% *}"
	if [ "$got_code" != "200" ]; then
		nota "✗ /$nombre/ -> $got_code (la sección existe en el árbol)"
		hallazgos=$((hallazgos + 1))
	fi
done
# ⛔ UN BUCLE SOBRE UNA VARIABLE SIN COMILLAS DEPENDE DE QUE EL SHELL DIVIDA EN PALABRAS, y zsh
# NO lo hace: allí correría UNA sola vez sobre la cadena entera y el gate diría haber mirado un
# locale sin mirar ninguno. Este fichero declara bash, pero un gate no debe depender de que su
# shebang se respete: `while read` se comporta igual en los dos.
while IFS= read -r loc; do
	[ -n "$loc" ] || continue
	rutas=$((rutas + 1))
	respuesta="$(probe "https://$HOST/$loc/")" || {
		nota "⚠ NO HE PODIDO MIRAR /$loc/"
		ciego=$((ciego + 1))
		continue
	}
	got_code="${respuesta%% *}"
	if [ "$got_code" != "200" ]; then
		nota "✗ /$loc/ -> $got_code (el locale existe en el árbol)"
		hallazgos=$((hallazgos + 1))
	fi
done <<EOF
$locales
EOF
nota "rutas estructurales verificadas: $rutas"
# ⛔ POSTCONDICIÓN, la hermana de la de reglas: si el descubrimiento no encuentra NADA —árbol
# movido, ruta equivocada— este tramo no comprueba nada y el gate salía 0. Cero rutas es no haber
# mirado, no un sitio correcto.
if [ "$rutas" -eq 0 ]; then
	echo "check-docs-site-live: ⛔ NO HE PODIDO MIRAR: cero rutas estructurales descubiertas bajo" \
		"$CONTENT. Descubrir nada no es lo mismo que comprobar algo." >&2
	exit 2
fi

# ---------------------------------------------------------------------------------------------
# 4 · THE HONEST 404. `not_found_handling: 404-page` is declared in docs-site/wrangler.jsonc; if a
#     nonsense path answers 200 the site is serving the index for everything, and every check
#     above would pass while nothing works. This is the NEGATIVE control of the whole gate.
# ---------------------------------------------------------------------------------------------
respuesta="$(probe "https://$HOST/olivares-negative-control-$$/")" || respuesta=""
if [ -z "$respuesta" ]; then
	nota "⚠ NO HE PODIDO MIRAR el control negativo"
	ciego=$((ciego + 1))
elif [ "${respuesta%% *}" != "404" ]; then
	nota "✗ control negativo: una ruta inexistente dio ${respuesta%% *}, no 404 — el sitio está" \
		"sirviendo el índice para todo y los verdes de arriba no significan nada"
	hallazgos=$((hallazgos + 1))
else
	nota "control negativo: ruta inexistente -> 404"
fi

# ---------------------------------------------------------------------------------------------
if [ "$hallazgos" -gt 0 ]; then
	echo "check-docs-site-live: $hallazgos hallazgo(s) — el sitio en vivo NO cumple lo que promete" \
		"este árbol. Publica docs-site (workflow docs-site-deploy) y vuelve a medir." >&2
	exit 1
fi
if [ "$ciego" -gt 0 ]; then
	echo "check-docs-site-live: ⛔ NO HE PODIDO MIRAR $ciego sonda(s). Eso no es un verde." >&2
	exit 2
fi
echo "check-docs-site-live: ✔ el sitio en vivo cumple lo que promete el árbol ($reglas redirecciones, $rutas rutas)"
exit 0
