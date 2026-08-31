#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-error-mappers.sh — build and run the mapper-parity gate.
#
# The gate itself is Go (scripts/check-error-mappers): the question it answers is
# "does this function reach that one", which is a call graph, and a call graph is
# not a grep. Everything it enforces, and why, is in that package's doc comment.
#
# This wrapper exists for the same reason the export curation script has one: the
# helper is a standalone module, it is built with GOWORK=off so a broken module
# elsewhere in the workspace cannot stop this gate from looking, and /tmp is NOEXEC
# in the dev container — a binary built there cannot be run, and the failure reads
# as a mysterious "permission denied" rather than as a mount option. TMPDIR is
# honoured and named in the error.
#
#   scripts/check-error-mappers.sh              scan modules/
#   scripts/check-error-mappers.sh --self-test  build throwaway trees and prove the gate can fail
#   scripts/check-error-mappers.sh <dir>        scan another tree
#
# THREE ANSWERS: 0 clean / 1 offenders named / 2 CANNOT LOOK.
set -uo pipefail
export LC_ALL=C

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
SRC="$ROOT/scripts/check-error-mappers"

if [ ! -d "$SRC" ]; then
	echo "check-error-mappers: CANNOT LOOK — the gate's own source is missing at $SRC." >&2
	exit 2
fi

if ! command -v go >/dev/null 2>&1; then
	echo "check-error-mappers: CANNOT LOOK — no Go toolchain on PATH, so the gate was not built." >&2
	echo "  A gate that could not run is not a gate that passed." >&2
	exit 2
fi

# The Taskfile points TMPDIR at a repo-local, gitignored directory precisely because
# /tmp is noexec here; mktemp will not create its parent, so this does.
[ -n "${TMPDIR:-}" ] && mkdir -p "$TMPDIR" 2>/dev/null
BIN_DIR="$(mktemp -d 2>/dev/null)" || {
	echo "check-error-mappers: CANNOT LOOK — could not create a scratch dir (TMPDIR=${TMPDIR:-unset})." >&2
	exit 2
}
trap 'rm -rf "$BIN_DIR"' EXIT
BIN="$BIN_DIR/check-error-mappers"

build_err="$(cd "$SRC" && GOWORK=off go build -o "$BIN" . 2>&1)" || {
	echo "check-error-mappers: CANNOT LOOK — the gate did not build." >&2
	printf '%s\n' "$build_err" | sed 's/^/    /' >&2
	exit 2
}

# Run it ONCE and read the shell's own verdict. 126/127 mean the shell could not
# execute the file at all — which in this container means /tmp is mounted noexec, and
# the bare message for that is "permission denied" on a file whose exec bit is set.
# 0, 1 and 2 are the gate's three answers from a binary that DID run, so they pass
# straight through. Probing with a throwaway run first would double the work and, on a
# tree with offenders, print the report twice.
if [ "${1:-}" = "--self-test" ]; then
	"$BIN" --self-test
else
	"$BIN" "${1:-$ROOT/modules}"
fi
rc=$?

if [ "$rc" -eq 126 ] || [ "$rc" -eq 127 ]; then
	echo "check-error-mappers: CANNOT LOOK — built the gate under TMPDIR=${TMPDIR:-/tmp} but the shell" >&2
	echo "  could not execute it (exit $rc). /tmp is mounted noexec in this container; set" >&2
	echo "  TMPDIR=/workspace/.olivares-tmptest and run again." >&2
	exit 2
fi
exit "$rc"
