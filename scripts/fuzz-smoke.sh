#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# fuzz-smoke.sh — bounded fuzz over every committed fuzz target (E2).
#
# WHY: the repo has fuzz targets (hostile-input parsers/decoders) but they ran in
# NO gate — only ad hoc. This runs each target for a SHORT, bounded fuzztime,
# starting from its checked-in seed corpus. It is a smoke, not a discovery run:
# it re-exercises the seeds and does a little mutation, so a regression that makes
# a parser panic or violate its invariant is caught quickly. A genuinely long
# fuzzing campaign is a separate, per-release/nightly activity.
#
# `go test -fuzz` fuzzes exactly ONE target per package invocation, so this
# enumerates every `func Fuzz*` and runs them one at a time with an anchored
# -fuzz regex (a package with several targets is fine).
#
# OLIVARES_FUZZTIME controls the per-target budget (default 10s; CI-REL sets it
# higher). A crash or invariant failure exits non-zero.
#
# Time is not the only resource that must be bounded. By default `go test -fuzz`
# starts one worker process per GOMAXPROCS. On an eight-CPU runner that made this
# supposedly bounded smoke start eight workers; under the concurrent mainline jobs,
# FuzzParseManifest reached its 20s deadline with workers still active and the Go
# coordinator returned `context deadline exceeded`. One worker is enough for a smoke
# whose first duty is replaying every seed; the nightly campaign owns parallel
# discovery. GOMAXPROCS=2 also bounds each coordinator/worker without starving the Go
# runtime's own housekeeping.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

FUZZTIME="${OLIVARES_FUZZTIME:-10s}"
FUZZ_GOMAXPROCS=2
FUZZ_PARALLEL=1

# Tracked + working-tree fuzz test files, scoped to the source trees (never
# node_modules / web). grep over the workspace source dirs picks up a target
# added in this branch before it is committed.
FUZZ_TREES=(cmd core modules connectors sdk operator clients terraform-provider-olivares)
# cloud/ is hub-internal and absent from the curated public export — include it only
# where it exists; every other missing tree still fails the premise check below.
if [ -d cloud ]; then
	FUZZ_TREES+=(cloud)
elif [ -f .olivares-public-export ]; then
	echo "fuzz-smoke: no cloud/ (curated public export) — fuzzing the shipped trees."
else
	echo "fuzz-smoke: cloud/ is MISSING and this tree carries no public-export marker; refusing." >&2
	exit 2
fi

# The tree list is checked BEFORE the scan. `grep -r … 2>/dev/null` swallows "No such
# file or directory" for any tree that was renamed or moved while still returning the
# survivors, so FUZZFILES stayed non-empty, the emptiness guard below never fired, and
# the gate reported "ran N target(s)" having fuzzed a silently reduced subset. There is
# no floor on N either, so the number shrinking is invisible: this is why the trees are
# named and verified rather than globbed past.
for t in "${FUZZ_TREES[@]}"; do
	[ -d "${t}" ] || {
		echo "fuzz:smoke: source tree '${t}' is missing; the target inventory would be" >&2
		echo "  silently short. Fix the tree list in this script if it moved." >&2
		exit 2
	}
done

grep_rc=0
mapfile -t FUZZFILES < <(
	grep -rElZ '^func Fuzz[A-Za-z0-9_]+\(' --include='*_test.go' "${FUZZ_TREES[@]}" 2>/dev/null |
		tr '\0' '\n' | sort -u
) || grep_rc=$?
if [ "${grep_rc}" -ge 2 ]; then
	echo "fuzz:smoke: the target scan failed (exit ${grep_rc}); the inventory is incomplete." >&2
	exit 2
fi

if [ "${#FUZZFILES[@]}" -eq 0 ]; then
	echo "fuzz:smoke: no fuzz targets found" >&2
	exit 1
fi

rc=0
count=0
for f in "${FUZZFILES[@]}"; do
	dir="$(dirname "${f}")"
	# cloud/control-plane is outside go.work.
	goenv=""
	case "${dir}" in
	cloud/control-plane* | ./cloud/control-plane*) goenv="GOWORK=off" ;;
	esac
	while read -r fn; do
		[ -z "${fn}" ] && continue
		count=$((count + 1))
		echo "==> fuzz ${fn}  (${dir}, ${FUZZTIME})"
		# Se IMPRIME la orden, no solo el objetivo. Un log que trae duraciones y no dice con que
		# orden se produjeron no se puede diagnosticar despues: el 2026-08-19
		# check-test-timeout-headroom.sh no pudo dictaminar sobre 6 paquetes de este job por eso
		# exactamente — habia `ok <pkg> <segundos>` y ninguna invocacion visible de la que deducir
		# el tope. Y el tope aqui NO es ninguno: al no pasar -timeout rige el defecto de Go, 10m,
		# que con FUZZTIME largo es alcanzable. Imprimirlo cuesta una linea.
		echo "    GOMAXPROCS=${FUZZ_GOMAXPROCS} go test -run='^\$' -fuzz='^${fn}\$' -fuzztime=${FUZZTIME} -parallel=${FUZZ_PARALLEL} .   (sin -timeout: rige el defecto de Go, 10m)"
		if ! ( cd "${dir}" && env ${goenv} GOMAXPROCS="${FUZZ_GOMAXPROCS}" go test -run='^$' -fuzz="^${fn}$" -fuzztime="${FUZZTIME}" -parallel="${FUZZ_PARALLEL}" . ); then
			echo "!! fuzz FAILED: ${fn} (${dir})" >&2
			rc=1
		fi
	done < <(grep -oE '^func (Fuzz[A-Za-z0-9_]+)' "${f}" | awk '{print $2}')
done

echo "fuzz:smoke: ran ${count} target(s) at ${FUZZTIME} each (rc=${rc})."
exit "${rc}"
