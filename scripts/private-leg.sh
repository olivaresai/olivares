#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# private-leg.sh — run a Taskfile leg whose working directory only exists in the
# private hub (cloud/, commercial/), with FOUR distinguishable answers:
#
#   1. the directory exists              -> cd into it and exec the command,
#                                           propagating its exact exit code
#   2. missing, and this is the PUBLIC   -> say NOT APPLICABLE where the job
#      export (PUBLIC-EXPORT.md present)    summary shows it, and exit 0
#   3. missing, and there is no marker   -> exit 1: refuse to guess which tree
#                                           this is — a hub that lost cloud/
#                                           would otherwise run its gate against
#                                           nothing and report it green
#   4. present, toolchain NOT installed   -> exit 3: "I could not look". Added
#                                           2026-08-07; see the block itself for
#                                           what it looked like without it.
#
# WHY THIS EXISTS (measured 2026-08-01). cloud/ and commercial/ never ship, but
# build:go and test:functional walked into them unguarded: go-task does not fail
# on a missing `dir:` — it CREATES the directory and runs inside it, and
# `go build ./...` then dies with "directory prefix . does not contain main
# module", so the published tree opened RED on two required checks. The one leg
# that DID guard (lint:commerce) had the opposite defect: a silent green echo,
# i.e. commercial enforcement reported as verified without having looked.
#
# The tree test is a POSITIVE marker stamped by the export, not the absence of
# the directory itself — absence-as-marker is exactly the fail-open this script
# closes. Deleting the marker in a public clone degrades to answer 3 (a loud red
# with an explanation), never to a silent green.
set -euo pipefail

if [ "$#" -lt 3 ]; then
	echo "usage: private-leg.sh <task-name> <relative-dir> <command> [args...]" >&2
	exit 2
fi
NAME="$1"
DIR="$2"
shift 2

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# ⛔ VACIO CUENTA COMO AUSENTE, y no es celo: `[ -d ]` a secas se AUTO-ENVENENA.
# go-task CREA el `dir:` de una tarea si no existe (probado el 2026-08-19 con un Taskfile
# señuelo: `dir: no/such/place` sale rc=0 y deja el directorio hecho). Asi que una pierna
# privada declarada con `dir:` en vez de con este guardian, corrida una sola vez en el arbol
# publico, FABRICA el directorio que este `-d` mira. A partir de esa corrida el guardian ve
# un directorio, entra y ejecuta el comando dentro de una carpeta sin `go.mod` — es decir,
# el primer fallo hace permanentes todos los siguientes y NOT APPLICABLE no vuelve a salir.
#
# Fue exactamente lo que pasó con `build:commerce-core`: era la unica de las tres piernas
# privadas sin guardian, la corri en una exportacion, y el directorio vacio que dejo me hizo
# diagnosticar «la curacion deja residuo». La curacion estaba BIEN: de los 648 ficheros de
# `commercial/` no viaja ninguno y una exportacion limpia no tiene ni un directorio vacio.
if [ -d "$ROOT/$DIR" ] && [ -n "$(find "$ROOT/$DIR" -mindepth 1 -print -quit 2>/dev/null)" ]; then
	cd "$ROOT/$DIR"
	# THE FOURTH ANSWER, and it was missing (measured 2026-08-07 by another lane, confirmed here).
	# The three above are all about the DIRECTORY. This one is about its TOOLCHAIN: the directory
	# is present and its dependencies are not, which is neither "not applicable" nor "the code is
	# broken" — it is the third answer this project insists on everywhere else, "I could not look".
	#
	# What it looked like without this block: `test:license-worker` is the FIRST leg of the heavy
	# gate on `main` and on tags, and in a container with no node_modules it died with
	# `tsc: not found` / exit 127. The hook then presented that as the licence Worker being RED —
	# blaming the culprit that the leg was put first to protect everyone from. Two of the three
	# containers could not run the release gate past its first step, and the reason on screen was
	# the wrong one.
	#
	# The test is DERIVED from the leg's own tree, not a list of task names: a directory that
	# declares a package.json and has no node_modules cannot run a node command, whichever leg it
	# is. Nothing else is guessed — a leg with no package.json is unaffected.
	if [ -f "package.json" ] && [ ! -d "node_modules" ]; then
		echo "private-leg: $NAME: CANNOT LOOK — $DIR/package.json declares dependencies and" >&2
		echo "  $DIR/node_modules is absent, so this leg has no toolchain to run. This is NOT a" >&2
		echo "  verdict about the code: nothing was built and nothing was tested." >&2
		# EL AMBITO ES EL WORKTREE, NO EL CONTENEDOR, y decirlo mal cuesta la ciega entera: quien
		# lea «once per container», lo haga una vez y se olvide, seguira viendo CANNOT LOOK en cada
		# worktree nuevo — le paso al integrador una noche entera el 2026-08-22. `node_modules` vive
		# en el arbol de trabajo y esta en .gitignore, asi que cada worktree necesita el suyo; hoy
		# este clon tiene 561. Lo que SI es por contenedor es la cache de npm (~/.npm/_cacache), y
		# por eso la segunda instalacion y las siguientes son baratas, no gratis.
		echo "  Fix it in THIS worktree — node_modules is per worktree, not per container:" >&2
		# ABSOLUTA, no relativa: el contraste `sol max` del 2026-08-22 lo midio — el consejo
		# funcionaba desde la raiz y fallaba desde cualquier subdirectorio, y quien lee esto esta
		# donde le pillo el gate, no necesariamente arriba. Un consejo que hay que traducir antes
		# de usarlo es medio consejo.
		echo "    npm --prefix $ROOT/$DIR ci" >&2
		echo "  (the npm cache IS shared per container, so repeats are cheap, not free.)" >&2
		exit 3
	fi
	exec "$@"
fi

if [ -f "$ROOT/PUBLIC-EXPORT.md" ]; then
	NOTE="NOT APPLICABLE: task '$NAME' — $DIR is not part of the public tree (see PUBLIC-EXPORT.md). Nothing was built or tested by this leg, and in this tree that is the expected state."
	echo "private-leg: $NOTE"
	if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
		printf '### %s\n\n%s\n\n' "$NAME: NOT APPLICABLE (public tree)" "$NOTE" >>"$GITHUB_STEP_SUMMARY"
	fi
	if [ -n "${GITHUB_ACTIONS:-}" ]; then
		echo "::notice title=$NAME not applicable in the public tree::$NOTE"
	fi
	exit 0
fi

echo "private-leg: $NAME: $DIR is MISSING and there is no PUBLIC-EXPORT.md marker." >&2
echo "  This tree is neither a complete hub (which has $DIR) nor a stamped public" >&2
echo "  export (which has the marker). Refusing to guess: running the gate without" >&2
echo "  $DIR would report it green against nothing." >&2
exit 1
