#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# run-network-observation.sh — keep an unobservable external subject from grading the tree.
#
# This wrapper is deliberately narrow: the wrapped check must use the repository's three-answer
# contract (0 observed-clean, 1 observed-finding, 2 could-not-look). By default findings still
# block and only the third answer becomes an advisory. `--all-advisory` is the explicit exception
# for censuses already declared non-blocking. Both advisory paths print wall cost; unexpected exit
# codes remain failures.
#
# Usage: bash scripts/run-network-observation.sh [--all-advisory] <label> -- <command> [args...]
set -uo pipefail

mode="unavailable-only"
if [ "${1:-}" = "--all-advisory" ]; then
	mode="all-advisory"
	shift
fi
if [ "$#" -lt 3 ] || [ -z "${1:-}" ] || [ "${2:-}" != "--" ]; then
	echo "run-network-observation: usage: [--all-advisory] <label> -- <command> [args...]" >&2
	exit 2
fi

label="$1"
shift 2
SECONDS=0
"$@"
rc=$?
elapsed="$SECONDS"

advise() {
	local reason="$1" message
	message="$label: ADVISORY — $reason; ${elapsed}s medidos. No se atribuye ese resultado al árbol."
	echo "run-network-observation: ⚠ $message" >&2
	if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
		echo "::warning title=External observation advisory::$message" >&2
	fi
	# Job summary is the durable notice (A-4): ::warning is an annotation and
	# can be missed. rc=1 never reaches this function, so a finding stays red.
	if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
		printf '%s\n' "⚠ $message" >>"$GITHUB_STEP_SUMMARY"
	fi
	exit 0
}

case "$rc" in
0)
	exit 0
	;;
1)
	# Most callers observed a real mismatch and must block. A census explicitly marked advisory is
	# informational even when it found something (the pre-push unpublished-work ratchet).
	if [ "$mode" = "all-advisory" ]; then
		advise "el censo externo/local informó un hallazgo (rc=1)"
	fi
	exit 1
	;;
2)
	advise "el runner no pudo observar el sujeto externo (red/IP o herramienta)"
	;;
*)
	echo "run-network-observation: $label devolvió rc=$rc, fuera del contrato 0/1/2; no se degrada." >&2
	exit "$rc"
	;;
esac
