#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# web-bundle-source-digest.sh — el digest del CONJUNTO DE FUENTES que se empaquetan.
#
# ⛔ POR QUÉ EXISTE. `check-web-bundle-freshness.sh` afirmaba en su cabecera que «si cambian las
#    fuentes que se empaquetan y el bundle no cambia, SEGURO que está obsoleto». **Esa implicación es
#    falsa, y el contraejemplo está medido el 2026-08-18**: un cambio de TIPOS en TypeScript —53
#    ficheros re-apuntando un `import type` de `ColumnDef` a un alias propio— toca 53 fuentes
#    empaquetadas y produce un bundle **byte a byte idéntico**, porque los tipos se borran al
#    compilar. Se reconstruyó (`task build:web`, rc=0) y `git status` sobre `dist` dio **0 ficheros**.
#
#    La consecuencia no es un falso rojo cualquiera: es un gate que **no se puede poner verde
#    haciendo lo correcto**. Reconstruir —la orden que el propio mensaje da— no cambia nada, así que
#    la única salida era `--no-verify`. Un gate cuyo remedio no funciona enseña a saltárselo.
#
# ⇒ LA MEDIDA QUE SÍ VALE: no «¿cambió el bundle?» sino **«¿de qué fuentes salió el bundle que hay
#   commiteado?»**. Este guion produce el digest que contesta eso, y lo produce igual desde el árbol
#   de trabajo (para sellar tras construir) que desde un commit (para juzgar un push), porque en los
#   dos casos son los MISMOS hashes de blob de git.
#
# Uso:
#   scripts/web-bundle-source-digest.sh              # árbol de trabajo
#   scripts/web-bundle-source-digest.sh <commit-ish> # ese árbol, sin sacarlo a disco
#
# Salida: el digest en stdout · 2 NO HE PODIDO MIRAR (nunca imprime un digest a medias).
set -uo pipefail
LC_ALL=C
export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ" 2>/dev/null || {
	echo "web-bundle-source-digest: ⛔ NO HE PODIDO MIRAR: no existe $RAIZ" >&2
	exit 2
}
SRC_DIR="${OLIVARES_WEB_DIR_REL:-web}"

# El MISMO predicado que usa el gate. Vive aquí y el gate lo llama, porque dos copias de un
# predicado divergen: si el gate cobrase un fichero que el sello no mira, un push honesto saldría
# rojo para siempre.
# ⛔ Y LAS EXCLUSIONES SE ANCLAN EN «TABULADOR O FIN DE LÍNEA», con un TABULADOR DE VERDAD. Este predicado corre sobre DOS flujos —una
# lista de rutas a secas, y pares `ruta<TAB>sha`— y con el ancla en fin de línea la exclusión de
# `*.test.tsx` **no casaba nunca en el segundo**, porque ahí la línea acaba en el sha. Medido al
# escribirlo: 1007 ficheros por una vía y 1239 por la otra, 232 de diferencia, y el digest de un
# mismo contenido salía distinto según por dónde entrara. Una sola función mal anclada valía por
# tener dos copias divergentes, que es lo que esta función existe para evitar.
#
# ⛔⛔ Y EL PRIMER INTENTO DE ARREGLARLO NO ARREGLÓ NADA, que es el detalle que merece quedarse:
# escribí `(\t|$)` dentro de una ERE de `grep`, y **en una expresión regular extendida `\t` no es un
# tabulador: es una `t`**. Las dos vías seguían dando 1007 y 1239 con el «arreglo» puesto. Sólo lo
# cazó volver a contar en vez de dar por bueno el cambio. De ahí el `printf` que mete el byte real.
filtra_empaquetadas() {
	grep -E "^${SRC_DIR}/(src/|public/|index\.html|vite\.config|tsconfig|package\.json|pnpm-lock\.yaml)" |
		grep -vE "$(printf '\\.(test|spec)\\.(ts|tsx|js|jsx)(\t|$)')" |
		grep -vE "^${SRC_DIR}/(e2e|tests)/"
}

if [ "$#" -ge 1 ] && [ -n "${1:-}" ]; then
	# Desde un commit: `ls-tree` da <modo> <tipo> <sha>\t<ruta>, que ya es exactamente el par
	# (contenido, ruta) que hace falta. No se saca nada a disco.
	pares="$(git ls-tree -r "$1" -- "$SRC_DIR" 2>/dev/null | awk -F'\t' '{split($1,c," "); print c[3]"\t"$2}')" || {
		echo "web-bundle-source-digest: ⛔ NO HE PODIDO MIRAR: '$1' no es un árbol legible." >&2
		exit 2
	}
	if [ -z "$pares" ]; then
		echo "web-bundle-source-digest: ⛔ NO HE PODIDO MIRAR: '$1' no contiene $SRC_DIR/." >&2
		exit 2
	fi
	lista="$(printf '%s\n' "$pares" | awk -F'\t' '{print $2"\t"$1}' | filtra_empaquetadas | sort)"
else
	# Desde el árbol de trabajo: `hash-object` da el MISMO sha de blob que tendría commiteado, así
	# que los dos caminos son comparables sin construir nada.
	rutas="$(git ls-files -- "$SRC_DIR" 2>/dev/null | filtra_empaquetadas | sort)"
	if [ -z "$rutas" ]; then
		echo "web-bundle-source-digest: ⛔ NO HE PODIDO MIRAR: cero fuentes empaquetadas en $SRC_DIR/." >&2
		exit 2
	fi
	# Un `hash-object` por tanda, no por fichero: son cientos.
	shas="$(printf '%s\n' "$rutas" | tr '\n' '\0' | xargs -0 git hash-object -- 2>/dev/null)" || {
		echo "web-bundle-source-digest: ⛔ NO HE PODIDO MIRAR: git hash-object falló." >&2
		exit 2
	}
	lista="$(paste -d'\t' <(printf '%s\n' "$rutas") <(printf '%s\n' "$shas"))"
fi

# CONTROL POSITIVO: un conjunto vacío no es un digest, es una ausencia. Sellar «nada» dejaría el
# gate verde para siempre, que es el fallo que este fichero existe para no cometer.
n="$(printf '%s\n' "$lista" | grep -c . || true)"
if [ "${n:-0}" -eq 0 ]; then
	echo "web-bundle-source-digest: ⛔ NO HE PODIDO MIRAR: el filtro dejó CERO fuentes." >&2
	exit 2
fi

printf '%s\n' "$lista" | sha256sum | awk -v n="$n" '{print $1" "n}'
