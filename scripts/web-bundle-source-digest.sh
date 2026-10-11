#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Hash console source paths and Git blob hashes, from a commit or the filesystem.
# The filesystem mode includes new sources and works in archives without .git.
set -uo pipefail
LC_ALL=C
export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ" 2>/dev/null || {
	echo "web-bundle-source-digest: ⛔ COULD NOT LOOK: $RAIZ does not exist" >&2
	exit 2
}
SRC_DIR="${OLIVARES_WEB_DIR_REL:-web}"

filtra_empaquetadas() {
	grep -E "^${SRC_DIR}/(src/|public/|index\.html|vite\.config|tsconfig|package\.json|pnpm-lock\.yaml)" |
		grep -vE "$(printf '\\.(test|spec)\\.(ts|tsx|js|jsx)(\t|$)')" |
		grep -vE "^${SRC_DIR}/(e2e|tests)/"
}

if [ "$#" -ge 1 ] && [ -n "${1:-}" ]; then
	# Desde un commit: `ls-tree` da <modo> <tipo> <sha>\t<ruta>, que ya es exactamente el par
	# (contenido, ruta) que hace falta. No se saca nada a disco.
	pares="$(git ls-tree -r "$1" -- "$SRC_DIR" 2>/dev/null | awk -F'\t' '{split($1,c," "); print c[3]"\t"$2}')" || {
		echo "web-bundle-source-digest: ⛔ COULD NOT LOOK: '$1' is not a readable tree." >&2
		exit 2
	}
	if [ -z "$pares" ]; then
		echo "web-bundle-source-digest: ⛔ COULD NOT LOOK: '$1' does not contain $SRC_DIR/." >&2
		exit 2
	fi
	lista="$(printf '%s\n' "$pares" | awk -F'\t' '{print $2"\t"$1}' | filtra_empaquetadas | sort)"
else
    # Enumerate actual build inputs, including new source files. This also works
    # in a source archive without .git; git hash-object does not need a repository.
    dirs=("$SRC_DIR")
    for dir in "$SRC_DIR/src" "$SRC_DIR/public"; do
        [ ! -d "$dir" ] || dirs+=("$dir")
    done
    rutas="$( { find "$SRC_DIR" -maxdepth 1 -type f || exit 2; \
        for dir in "${dirs[@]:1}"; do find "$dir" -type f || exit 2; done; } | filtra_empaquetadas | sort)" || {
        echo "web-bundle-source-digest: cannot enumerate source files" >&2
        exit 2
    }
	if [ -z "$rutas" ]; then
		echo "web-bundle-source-digest: ⛔ COULD NOT LOOK: no bundled source files in $SRC_DIR/." >&2
		exit 2
	fi
	# Un `hash-object` por tanda, no por fichero: son cientos.
	shas="$(printf '%s\n' "$rutas" | tr '\n' '\0' | xargs -0 git hash-object -- 2>/dev/null)" || {
		echo "web-bundle-source-digest: ⛔ COULD NOT LOOK: git hash-object failed." >&2
		exit 2
	}
	lista="$(paste -d'\t' <(printf '%s\n' "$rutas") <(printf '%s\n' "$shas"))"
fi

# CONTROL POSITIVO: un conjunto vacío no es un digest, es una ausencia. Sellar «nada» dejaría el
# gate verde para siempre, que es el fallo que este fichero existe para no cometer.
n="$(printf '%s\n' "$lista" | grep -c . || true)"
if [ "${n:-0}" -eq 0 ]; then
	echo "web-bundle-source-digest: ⛔ COULD NOT LOOK: the filter left ZERO source files." >&2
	exit 2
fi

printf '%s\n' "$lista" | sha256sum | awk -v n="$n" '{print $1" "n}'
