#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-list-page-discarded.sh — build and run the truncated-list gate.
#
# The gate itself is Go (scripts/check-list-page-discarded): the question it answers
# is "does the query passed to THIS call declare a page size", and the query is
# usually built several lines above the call, in a variable. That is a dataflow
# question, and a dataflow question is not a grep — the census that opened this work
# gave four different answers (143, 228, 374, 539) depending on the pattern. What it
# enforces, and the 65 calls its mechanism does not reach, are in that package's doc
# comment.
#
# This wrapper mirrors scripts/check-error-mappers.sh for the same reasons: the
# helper is a standalone module built with GOWORK=off so a broken module elsewhere in
# the workspace cannot stop this gate from looking, and /tmp is NOEXEC in the dev
# container — a binary built there cannot be run and the failure reads as a
# mysterious "permission denied" rather than as a mount option.
#
#   scripts/check-list-page-discarded.sh              scan the repository
#   scripts/check-list-page-discarded.sh --self-test  prove the gate can fail, on the two shapes that shipped
#   scripts/check-list-page-discarded.sh <dir>        scan another tree
#
# THREE ANSWERS: 0 clean / 1 offenders named / 2 CANNOT LOOK.
set -uo pipefail
export LC_ALL=C

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
SRC="$ROOT/scripts/check-list-page-discarded"

if [ ! -d "$SRC" ]; then
	echo "check-list-page-discarded: CANNOT LOOK — the gate's own source is missing at $SRC." >&2
	exit 2
fi

if ! command -v go >/dev/null 2>&1; then
	echo "check-list-page-discarded: CANNOT LOOK — no Go toolchain on PATH, so the gate was not built." >&2
	echo "  A gate that could not run is not a gate that passed." >&2
	exit 2
fi

[ -n "${TMPDIR:-}" ] && mkdir -p "$TMPDIR" 2>/dev/null
BIN_DIR="$(mktemp -d 2>/dev/null)" || {
	echo "check-list-page-discarded: CANNOT LOOK — could not create a scratch dir (TMPDIR=${TMPDIR:-unset})." >&2
	exit 2
}
trap 'rm -rf "$BIN_DIR"' EXIT
BIN="$BIN_DIR/check-list-page-discarded"

build_err="$(cd "$SRC" && GOWORK=off go build -o "$BIN" . 2>&1)" || {
	echo "check-list-page-discarded: CANNOT LOOK — the gate did not build." >&2
	printf '%s\n' "$build_err" | sed 's/^/    /' >&2
	exit 2
}

if [ "${1:-}" = "--self-test" ]; then
	"$BIN" --self-test
else
	"$BIN" "${1:-$ROOT}"
fi
rc=$?

if [ "$rc" -eq 126 ] || [ "$rc" -eq 127 ]; then
	echo "check-list-page-discarded: CANNOT LOOK — built the gate under TMPDIR=${TMPDIR:-/tmp} but the shell" >&2
	echo "  could not execute it (exit $rc). /tmp is mounted noexec in this container; set" >&2
	echo "  TMPDIR=/workspace/.olivares-tmptest and run again." >&2
	exit 2
fi
exit "$rc"
