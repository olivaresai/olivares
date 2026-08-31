#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-boundary.sh — enforce the license/architecture frontier (ARCHITECTURE.md,
# ARCHITECTURE.md, LICENSING.md):
#
#   The Apache-2.0 SDK and the Apache-2.0 connectors must NEVER depend on the
#   AGPL engine (/core). This keeps the AGPL/Apache boundary clean so the
#   community can build connectors without copyleft contamination — the
#   amplitude moat. A breach here is a licensing defect, not a style nit.
#
# HOW: for the `sdk` and `connectors` modules we ask the Go toolchain for the
# FULL transitive import set of every package (`go list -deps ./...`) and fail
# if any dependency is under github.com/olivaresai/olivares/core. `go list`
# sees through the go.work workspace and the local `replace` directives, so this
# is the real build graph, not a textual grep that misses indirect imports.
#
# Usage:  scripts/check-boundary.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

CORE_PREFIX="github.com/olivaresai/olivares/core"

# A boundary that could not be examined is not a boundary that held. Until 2026-08-01
# every toolchain call here ended in `2>/dev/null` and either `|| true` or a pipe into
# grep, and the two shapes failed open in different ways:
#
#   * `pkgs="$(cd "${m}" && go list ./... 2>/dev/null || true)"` turned ANY toolchain
#     failure into an empty package list, which the next line reported as "(no
#     packages)" and skipped. Demonstrated by running the gate with `go` off the PATH:
#     it printed "(no packages)" for every Apache module and then "Boundary check OK",
#     exit 0, having inspected nothing.
#   * `(cd core && go list -deps ./... 2>/dev/null) | grep -q ...` is worse, because
#     `set -o pipefail` is active: the pipeline's status is go list's when go list
#     fails, EVEN IF grep matched. So a genuine violation in a tree that also emitted a
#     load error would be read as "no violation" — the gate is quietest exactly when
#     the build graph is in trouble.
#
# Both are replaced by helpers that abort with exit 2 and name the module. The grep is
# fed from a variable instead of a pipe, so its own status is the only one that counts.
die_unverified() { # die_unverified <what> <dir> <rc> <output>
	echo "check-boundary: ${1} failed in ${2} (exit ${3}); the licence boundary is UNVERIFIED." >&2
	printf '%s\n' "${4}" | sed 's/^/    /' >&2
	exit 2
}

# stderr goes to a file, never into the captured stdout: `go list` can warn while
# succeeding, and a warning folded into the package list would be walked as if it were
# a package name.
BOUNDARY_ERR="$(mktemp)"
trap 'rm -f "${BOUNDARY_ERR}"' EXIT

list_pkgs() { # list_pkgs <dir>
	local dir="$1" out rc=0
	out="$(cd "${dir}" && go list ./... 2>"${BOUNDARY_ERR}")" || rc=$?
	[ "${rc}" -eq 0 ] || die_unverified "'go list ./...'" "${dir}" "${rc}" "$(cat "${BOUNDARY_ERR}")"
	printf '%s\n' "${out}"
}

list_deps() { # list_deps <dir> <pattern-or-pkg>
	local dir="$1" pkg="$2" out rc=0
	out="$(cd "${dir}" && go list -deps "${pkg}" 2>"${BOUNDARY_ERR}")" || rc=$?
	[ "${rc}" -eq 0 ] || die_unverified "'go list -deps ${pkg}'" "${dir}" "${rc}" "$(cat "${BOUNDARY_ERR}")"
	printf '%s\n' "${out}"
}

inspected=0
apache_inspected=0   # graphs from the FORBIDDEN (Apache) modules ONLY
core_inspected=0     # the layering graph of core, counted apart

# Modules that are forbidden from importing the engine (every Apache-2.0 module:
# the SDK interface module, its gRPC/go-plugin transport, the connectors, and
# the client SDKs + their generator — pure REST consumers of the frozen
# /v1 contract, never linked against the engine).
FORBIDDEN_MODULES=("sdk" "sdk/plugin" "sdk/scaffold" "connectors" "clients/go" "clients/generator")

rc=0
missing=""
for m in "${FORBIDDEN_MODULES[@]}"; do
  # A MODULE ON THIS LIST THAT DOES NOT EXIST IS INFORMATION, NOT A NON-EVENT. It used to be
  # `[ -d "${m}" ] || continue` — skipped in silence — and that is the second door into the
  # failure this file's own header names at :36: "exit 0, having inspected nothing". The header
  # closed the toolchain door (go list failing now exits 2) and left this one open. Rename or
  # move `connectors/` or `sdk/` and every -d test fails, the loop runs zero times, and the
  # LICENCE boundary reports OK having examined nothing. Measured 2026-08-08 on a tree
  # containing only this script: "Boundary check OK across 0 inspected package graph(s)",
  # exit 0. On the real tree the same run inspects 256.
  if [ ! -d "${m}" ]; then
    missing="${missing} ${m}"
    echo "==> ${m}: ABSENT — on the forbidden list and not on disk"
    continue
  fi
  echo "==> ${m}: must not import ${CORE_PREFIX}"

  # All packages in the module. An empty list is only legitimate when the module has
  # no Go sources at all; otherwise the toolchain saw something we did not, and the
  # gate must say so instead of skipping the module in silence.
  pkgs="$(list_pkgs "${m}")"
  if [ -z "${pkgs}" ]; then
    if [ -n "$(find "${m}" -name '*.go' -not -path '*/vendor/*' -print -quit)" ]; then
      echo "check-boundary: ${m} has Go sources but 'go list ./...' returned no packages;" >&2
      echo "  the licence boundary is UNVERIFIED for it." >&2
      exit 2
    fi
    echo "    (no Go sources)"
    continue
  fi

  # For each package, list its transitive deps and look for a core import. The grep
  # reads a variable rather than a pipe: under pipefail a piped `go list` failure would
  # mask a grep MATCH, i.e. hide the very violation this gate exists to find.
  while IFS= read -r pkg; do
    [ -n "${pkg}" ] || continue
    inspected=$((inspected + 1))
    apache_inspected=$((apache_inspected + 1))
    deps="$(list_deps "${m}" "${pkg}")"
    hits="$(printf '%s\n' "${deps}" | grep "^${CORE_PREFIX}\(/\|$\)" || true)"
    if [ -n "${hits}" ]; then
      echo "    BOUNDARY VIOLATION: ${pkg} transitively imports ${CORE_PREFIX}"
      echo "      offending deps:"
      printf '%s\n' "${hits}" | sed 's/^/        /'
      rc=1
    fi
  done <<<"${pkgs}"
done

# The reverse layering rule: the AGPL engine (/core) depends only INWARD (core + sdk).
# It must NOT import /modules or /connectors, which sit ABOVE it (ARCHITECTURE.md). The deploy
# executor (core/runtime/executor) in particular is self-contained on stdlib; this
# guard fails the build if any core package transitively pulls in /modules or /connectors
# (the layering inversion the executor was warned against), which `go list -deps` catches.
OUTWARD="github.com/olivaresai/olivares/\(modules\|connectors\)"
# CORE ABSENT IS SAID, NOT SKIPPED. This `if` used to fall through in silence, so a tree without
# core/ still printed "core imports no /modules or /connectors" and exited 0 — asserting a layering
# rule it had not evaluated. Same shape as the absent Apache module below it, found by the same
# contrast. The bookkeeping is what makes it visible: core_inspected stays 0 and the verdict says so.
if [ ! -d "core" ]; then
  echo "==> core: ABSENT — the layering rule (core must not import outward) was NOT evaluated"
  core_missing=1
fi
if [ -d "core" ]; then
  echo "==> core: must not import /modules or /connectors (no outward layering inversion)"
  core_deps="$(list_deps core "./...")"
  [ -n "${core_deps}" ] || { echo "check-boundary: core resolved to no dependencies at all; UNVERIFIED." >&2; exit 2; }
  core_hits="$(printf '%s\n' "${core_deps}" | grep "^${OUTWARD}\(/\|$\)" || true)"
  if [ -n "${core_hits}" ]; then
    echo "    BOUNDARY VIOLATION: core transitively imports /modules or /connectors"
    printf '%s\n' "${core_hits}" | sed 's/^/        /'
    rc=1
  fi
  inspected=$((inspected + 1))
  core_inspected=$((core_inspected + 1))
fi

if [ "${rc}" -ne 0 ]; then
  echo
  echo "Boundary check FAILED: an Apache-2.0 module imports the AGPL engine (/core),"
  echo "or the AGPL engine (/core) imports outward into /modules or /connectors."
  echo "Fix: connectors/sdk depend only on github.com/olivaresai/olivares/sdk; core depends only inward (core + sdk)."
  exit 1
fi

# ZERO INSPECTED IS NOT A PASS. This is the property the header promised and the code did not
# have: a boundary that examined nothing did not hold, it was not looked at. Exit 2 — COULD NOT
# LOOK — never 0, because "OK across 0" and "OK across 256" are printed by the same line and
# nobody reads the number.
# THE TWO SIDES ARE COUNTED APART, and lumping them was a real hole in the first version of this
# guard. `inspected` was incremented BOTH per Apache package (:116) and once for core's layering
# graph (:144), so a tree where all six Apache modules exist with a go.mod and NO Go sources
# reached the end with inspected=1 -- and that 1 was CORE. It printed "Boundary check OK across 1
# inspected package graph(s): sdk/ and connectors/ depend on no /core package", exit 0, having
# measured nothing at all on the side the licence boundary is about. Found by an adversarial
# contrast of that very fix, 2026-08-08: a guard against measuring nothing that could still
# certify the wrong nothing.
if [ "${core_missing:-0}" -eq 1 ]; then
  echo "check-boundary: core/ is absent, so the layering half of this gate was not evaluated;" >&2
  echo "  the boundary is UNVERIFIED in that direction. Either this is not the repository root," >&2
  echo "  or core/ moved and this gate needs updating in the same commit that moved it." >&2
  exit 2
fi

if [ "${apache_inspected}" -eq 0 ]; then
  echo "check-boundary: ZERO Apache-module package graphs were inspected; the licence boundary" >&2
  echo "  is UNVERIFIED. (core contributed ${core_inspected} graph(s), which says nothing about" >&2
  echo "  whether an Apache module imports the engine — that is the direction this gate exists" >&2
  echo "  for, and it was not measured.)" >&2
  if [ -n "${missing}" ]; then
    echo "  these modules are on the forbidden list and absent from disk:${missing}" >&2
    echo "  either they moved (update FORBIDDEN_MODULES in the same commit that moved them)" >&2
    echo "  or this is not the repository root." >&2
  else
    echo "  every module exists but produced no packages, which the per-module check should" >&2
    echo "  already have caught: read its output above before trusting anything here." >&2
  fi
  exit 2
fi

# An absent module with OTHERS inspected is still worth refusing: the list is the definition of
# what the boundary covers, so a stale entry silently shrinks the gate's scope. Loud, and named.
if [ -n "${missing}" ]; then
  echo "check-boundary: ${inspected} graph(s) inspected, but these are on the forbidden list" >&2
  echo "  and absent from disk:${missing}" >&2
  echo "  A stale entry SHRINKS what this gate covers without anyone noticing. Update" >&2
  echo "  FORBIDDEN_MODULES in the same commit that moves or removes a module." >&2
  exit 2
fi

echo "Boundary check OK across ${inspected} inspected package graph(s) (${apache_inspected} Apache + ${core_inspected} core): sdk/ and connectors/ depend"
echo "on no /core package; core imports no /modules or /connectors."
