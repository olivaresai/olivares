#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Run one fixture battery in an owned scratch root. A clean battery must leave that root empty;
# the wrapper makes a cleanup regression loud and still removes the residue before returning.
set -u -o pipefail

[ "$#" -gt 0 ] || {
	echo "with-clean-tmp: NO HE PODIDO MIRAR: falta el comando" >&2
	exit 2
}

base="${TMPDIR:-/tmp}"
[ -d "$base" ] && [ -w "$base" ] || {
	echo "with-clean-tmp: NO HE PODIDO MIRAR: la base temporal no es escribible" >&2
	exit 2
}
base="$(cd -- "$base" 2>/dev/null && pwd -P)" || exit 2
root="$(mktemp -d "$base/olivares-fixture-run.XXXXXX")" || exit 2
cleanup() {
	case "$root" in
	"$base"/olivares-fixture-run.*) ;;
	*) return 2 ;;
	esac
	chmod -R u+rwX "$root" 2>/dev/null
	rm -rf -- "$root"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
work="$root/work"
mkdir -p "$work" || exit 2

TMPDIR="$work" GOTMPDIR="$work" "$@"
rc=$?
looked=1
if residue="$(find "$work" -mindepth 1 -maxdepth 1 -printf x 2>/dev/null)"; then
	left="${#residue}"
else
	echo "with-clean-tmp: NO HE PODIDO MIRAR el scratch después del comando" >&2
	left=0
	looked=0
	rc=2
fi
if [ "$left" -gt 0 ]; then
	echo "with-clean-tmp: BROKEN: el comando dejó $left entrada(s) en su scratch" >&2
	[ "$rc" -ne 0 ] || rc=1
elif [ "$looked" -eq 1 ]; then
	echo "with-clean-tmp: CLEAN: el comando dejó su scratch como lo encontró"
fi

cleanup || rc=2
trap - EXIT
[ ! -e "$root" ] || {
	echo "with-clean-tmp: NO HE PODIDO LIMPIAR su raíz privada" >&2
	rc=2
}
exit "$rc"
