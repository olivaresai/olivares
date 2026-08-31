#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-migration-contiguity.sh — a gap in a migration directory is a migration
# that will never run, and it fails SILENTLY.
#
# WHY THIS EXISTS, measured on 2026-08-28 by against itself.
#
# golang-migrate keeps ONE scalar version per database and Up() applies only
# Next(current). So a database that reaches version 16 never applies a 014 or 015
# that lands afterwards: they are not Next(16). The migration is skipped, no error
# is raised, and the schema silently diverges from the tree.
#
# walked straight into it. Needing a slot, it censused every remote branch,
# found 014 and 015 already claimed by feature-* and feature-*, and took
# 016 to avoid a collision. That is correct reasoning about NAMES and the wrong
# direction of failure:
#
#   a duplicate number is LOUD  - two files share a prefix, the migrator refuses
#                                 at boot, and the integrator renumbers at merge;
#   a gap is SILENT             - everything boots, and one migration never runs.
#
# => The rule for a scalar migrator is max(existing)+1 IN THE TREE YOU MERGE INTO.
# Reserving against branches solves a problem the migrator does not have, at the
# cost of one it does.
#
# Three answers, and the third is the code, not the prose (canon rule 5):
#   0  contiguous
#   1  a gap - name the missing versions
#   2  could not look (no directory, unreadable name)
set -u -o pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}" || { echo "check-migration-contiguity: NO HE PODIDO MIRAR: no cd a ${ROOT}" >&2; exit 2; }

# GitHub Actions pide un veredicto sobre el SHA que disparó el job, no sobre el HEAD mutable del
# checkout reutilizado. `HEAD` sigue siendo el sujeto local cuando no hay GITHUB_SHA; en ese modo
# se conserva `git ls-files` más abajo para que una migración recién indexada cuente.
#
# ⛔ NO SE RESUELVE EL REF FUERA DE UN REPOSITORIO. Este gate viaja también en el export público,
# donde no hay `.git` y la versión de KERNEL se repliega a disco de forma declarada. Un runner puede
# conservar GITHUB_SHA en ese contexto: convertir su ausencia de repo en rc=2 rompería el export.
TREE_REF="${GITHUB_SHA:-HEAD}"
REPO_AVAILABLE=0
TREE_SHA=""
if git rev-parse --git-dir >/dev/null 2>&1; then
	REPO_AVAILABLE=1
	if [ -n "${GITHUB_SHA:-}" ]; then
		TREE_SHA="$(git rev-parse --verify "${TREE_REF}^{commit}" 2>/dev/null)" || {
			echo "check-migration-contiguity: NO HE PODIDO MIRAR: no resuelvo ${TREE_REF} a un commit" >&2
			exit 2
		}
	fi
fi

# The directories are NAMED, not discovered, on purpose: a glob would grow this
# gate silently onto trees whose migrator may not be scalar, and a gate that
# widens by accident is how a rule stops meaning anything.
#
# One per line, and read with `while read` rather than an unquoted `for`: this
# repository runs zsh interactively, zsh does NOT word-split an unquoted
# variable, and the loop would run ONCE over the whole string while looking
# exactly like it iterated.
MIGRATION_DIRS="${OLIVARES_MIGRATION_DIRS:-cloud/control-plane/migrations}"

rc=0
looked=0
while IFS= read -r dir; do
	[ -n "${dir}" ] || continue
	# `.up.sql` only: a down migration carries the same version, and counting both
	# would report every version twice.
	# ⛔ SE LEE EL ÁRBOL VERSIONADO, NO EL DIRECTORIO EN DISCO, y esto es un rojo medido de
	# `mainline-ci`, no una precaución. Los runners auto-alojados reutilizan el mismo
	# directorio de checkout entre corridas: `git checkout <sha>` deja bien los ficheros
	# RASTREADOS, pero los NO rastreados de la corrida anterior siguen ahí. Con `ls` esta
	# pata contaba migraciones que no estaban en el SHA bajo prueba y acusaba huecos
	# inexistentes — o, peor en la otra dirección, rellenaba un hueco real con un fichero
	# ajeno y decía CLEAN.
	#
	# `git ls-files` es la primitiva correcta, y no `git ls-tree HEAD`: ls-files ve lo
	# rastreado Y lo indexado, así que una migración recién añadida con `git add` —el caso
	# normal de quien la está escribiendo— sigue contando, mientras que la basura sin
	# rastrear deja de contar. `ls-tree HEAD` habría arreglado el runner rompiendo el uso local.
	#
	# El repliegue a disco existe porque este guion VIAJA al árbol exportado, donde puede no
	# haber repositorio. No es silencioso: la pata dice en qué modo miró, porque un portón que
	# cambia de fuente sin decirlo hace que su verde signifique dos cosas distintas.
	if [ "${REPO_AVAILABLE}" -eq 1 ] && [ -n "${GITHUB_SHA:-}" ]; then
		git cat-file -e "${TREE_SHA}:${dir}" 2>/dev/null || {
			echo "check-migration-contiguity: NO HE PODIDO MIRAR: no existe ${dir} en ${TREE_SHA}" >&2
			exit 2
		}
		modo="árbol solicitado ${TREE_SHA:0:12} (git ls-tree)"
		versions="$(git ls-tree --name-only "${TREE_SHA}:${dir}" 2>/dev/null \
			| command grep -E '^[0-9]+_.*\.up\.sql$' || true)"
	elif [ "${REPO_AVAILABLE}" -eq 1 ]; then
		[ -d "${dir}" ] || {
			echo "check-migration-contiguity: NO HE PODIDO MIRAR: no existe ${dir}" >&2
			exit 2
		}
		modo="árbol versionado (git ls-files)"
		versions="$(git ls-files -- "${dir}/" 2>/dev/null | command sed 's|.*/||' \
			| command grep -E '^[0-9]+_.*\.up\.sql$' || true)"
	else
		[ -d "${dir}" ] || {
			echo "check-migration-contiguity: NO HE PODIDO MIRAR: no existe ${dir}" >&2
			exit 2
		}
		modo="directorio en disco (sin repositorio)"
		versions="$(command ls -1 "${dir}" 2>/dev/null | command grep -E '^[0-9]+_.*\.up\.sql$' || true)"
	fi
	if [ -z "${versions}" ]; then
		echo "check-migration-contiguity: NO HE PODIDO MIRAR: ${dir} no tiene ninguna .up.sql" >&2
		exit 2
	fi
	nums="$(printf '%s\n' "${versions}" | sed -E 's/^([0-9]+)_.*/\1/' | sed -E 's/^0+([0-9])/\1/' | sort -n -u)"
	looked=$((looked + 1))
	echo "check-migration-contiguity: ${dir} leído del ${modo}"
	first="$(printf '%s\n' "${nums}" | head -1)"
	last="$(printf '%s\n' "${nums}" | tail -1)"
	# Duplicates are NOT this gate's business: the migrator refuses them loudly on
	# its own, which is exactly the failure direction we prefer.
	missing=""
	n="${first}"
	while [ "${n}" -le "${last}" ]; do
		if ! printf '%s\n' "${nums}" | command grep -qx "${n}"; then
			missing="${missing} $(printf '%03d' "${n}")"
		fi
		n=$((n + 1))
	done
	if [ -n "${missing}" ]; then
		{
			echo "check-migration-contiguity: ${dir} tiene HUECOS:${missing}"
			echo "                 Rango presente: $(printf '%03d' "${first}")..$(printf '%03d' "${last}")."
			echo "                 golang-migrate lleva UNA version escalar y Up() aplica solo Next(actual),"
			echo "                 asi que una base que llegue a $(printf '%03d' "${last}") NO aplicara nunca las que faltan."
			echo "                 Renumera tu migracion a max(existentes)+1 EN EL ARBOL AL QUE MERGEAS."
			echo "                 Si el numero ya esta tomado por otra rama, la colision es RUIDOSA y la"
			echo "                 resuelve el integrador al mergear; un hueco es SILENCIOSO y no lo ve nadie."
		} >&2
		rc=1
	else
		echo "check-migration-contiguity: ${dir} CONTIGUO - $(printf '%03d' "${first}")..$(printf '%03d' "${last}"), $(printf '%s\n' "${nums}" | wc -l | tr -d ' ') versiones."
	fi
done <<EOF
${MIGRATION_DIRS}
EOF

if [ "${looked}" -eq 0 ]; then
	echo "check-migration-contiguity: NO HE PODIDO MIRAR: ningun directorio revisado" >&2
	exit 2
fi
exit "${rc}"
