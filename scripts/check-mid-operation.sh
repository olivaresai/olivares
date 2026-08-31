#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-mid-operation.sh — publicar desde una operación a medias publica un árbol PARCIAL.
#
# CENSUS-SUBJECT: repo
#   Su sujeto es ESTE árbol de trabajo. Un repo limpio pasa; ésa es la respuesta correcta.
#
# POR QUÉ EXISTE, medido el 2026-08-19 sobre esta misma sesión y no deducido.
#
# El hub integró el PR #1029 con `git merge --no-ff`, resolvió su único conflicto, commiteó, y
# antes de publicar hizo `git rebase origin/main` como en todas las publicaciones. **`git rebase`
# APLANA el commit de merge** y replaya los commits sueltos que trajo. Tres entraron; el cuarto
# volvió a chocar contra la MISMA línea, y el rebase se detuvo dejando HEAD detached en el estado
# intermedio.
#
# ⛔ Y AHÍ ESTÁ EL FILO: la cadena de publicación siguió. Corrió `lint:format-ratchet`,
# `lint:prose` y `lint:inbox` — los TRES pasaron, porque el árbol parcial era internamente
# consistente — y `git push HEAD:main` publicó ese parcial. El mensaje «gates OK» era CIERTO. Era
# cierto sobre un árbol que no era el que el integrador creía estar publicando.
#
# `main` quedó consistente por suerte, no por diseño: el commit que faltaba era el que resolvía el
# conflicto, así que quedó la versión previa, que también estaba entera. Si el reparto hubiera
# caído al revés —la mitad de un cambio en dos ficheros— `main` habría quedado roto para los cinco
# carriles, publicado por una cadena que dijo que estaba verde.
#
# NINGÚN gate de contenido puede cazar esto, y por eso es un gate aparte: todos miran el árbol, y
# el árbol parcial está bien formado. Lo que hay que mirar es el ESTADO DE LA OPERACIÓN, que vive
# en `.git` y no en ningún fichero versionado.
#
# ⚠ NO DUPLICA `lint:conflict-markers`, y la diferencia es exactamente el caso que costó el push:
# aquél lee el CONTENIDO de los ficheros trackeados buscando `<<<<<<<`. Un rebase detenido cuyo
# conflicto YA se ha resuelto y añadido al índice no tiene ni un marcador — y sigue estando a
# medias. Éste lee el ESTADO DE LA OPERACIÓN. Los dos hacen falta y ninguno cubre al otro.
#
# TRES RESPUESTAS, como todos los nuestros: limpio (0), a medias (1), no he podido mirar (2).
set -euo pipefail

if ! command -v git >/dev/null 2>&1; then
	echo "check-mid-operation: NO_HE_PODIDO_MIRAR — no hay git en el PATH" >&2
	exit 2
fi
if ! git rev-parse --git-dir >/dev/null 2>&1; then
	echo "check-mid-operation: NO_HE_PODIDO_MIRAR — esto no es un repositorio git" >&2
	exit 2
fi

# `--git-path` resuelve BIEN desde un worktree enlazado, donde el estado de rebase vive en
# .git/worktrees/<nombre>/ y NO en el .git del clon principal. Construir la ruta a mano con
# "$(git rev-parse --git-dir)/rebase-merge" también funciona hoy, pero --git-path es el que git
# documenta para esto y no depende de cómo esté montado el worktree.
hallazgos=0
report() {
	echo "check-mid-operation: ⛔ $1"
	hallazgos=$((hallazgos + 1))
}

for estado in rebase-merge rebase-apply; do
	ruta="$(git rev-parse --git-path "$estado")"
	if [ -d "$ruta" ]; then
		report "REBASE A MEDIAS ($estado). Termínalo con 'git rebase --continue' o retíralo con 'git rebase --abort' ANTES de publicar."
	fi
done
for estado in MERGE_HEAD CHERRY_PICK_HEAD REVERT_HEAD BISECT_LOG; do
	ruta="$(git rev-parse --git-path "$estado")"
	if [ -e "$ruta" ]; then
		report "OPERACIÓN A MEDIAS ($estado). Termínala o retírala ANTES de publicar."
	fi
done

# Un conflicto sin resolver puede existir sin ninguno de los ficheros de arriba (por ejemplo tras
# un `git checkout -m`), así que se mira también el índice: la etapa distinta de 0 ES el conflicto.
if conflictos="$(git diff --name-only --diff-filter=U 2>/dev/null)" && [ -n "$conflictos" ]; then
	n="$(printf '%s\n' "$conflictos" | wc -l | tr -d ' ')"
	report "$n fichero(s) con CONFLICTO SIN RESOLVER en el índice:"
	printf '%s\n' "$conflictos" | sed 's/^/    /'
fi

if [ "$hallazgos" -gt 0 ]; then
	echo "check-mid-operation: $hallazgos hallazgo(s). Un árbol a medias es internamente consistente:"
	echo "                     los gates de contenido pasan y publican un PARCIAL. Medido el 2026-08-19."
	exit 1
fi
echo "check-mid-operation: OK — ninguna operación de git a medias"
exit 0
