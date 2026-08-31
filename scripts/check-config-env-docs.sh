#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-config-env-docs.sh — build and run the OLIVARES_* configuration-reference gate.
#
# THE REGRESSION IT FORBIDS. Measured 2026-08-15 and re-measured 2026-08-16: the public
# configuration reference documented 7 of the 311 OLIVARES_* names the non-test Go
# sources declare, and called them "a small set of environment variables". Nothing was
# red. A list that somebody has to remember to extend is stale the day the 312th name
# lands, so the roster is ENUMERATED FROM THE CODE and the page is REGENERATED from
# that enumeration; this gate fails when the two disagree.
#
# The gate itself is Go (scripts/config-env-docs): the question it answers is "is this
# literal a declaration or prose about one", which is an AST question, and an AST is
# not a grep. Everything it enforces, and why, is in that package's doc comment.
#
# This wrapper exists for the same reason scripts/check-error-mappers.sh has one: the
# helper is a standalone module, it is built with GOWORK=off so a broken module
# elsewhere in the workspace cannot stop this gate from looking, and /tmp is NOEXEC in
# the dev container — a binary built there cannot be run, and the failure reads as a
# mysterious "permission denied" rather than as a mount option. TMPDIR is honoured and
# named in the error.
#
#   scripts/check-config-env-docs.sh              check the published page against the code
#   scripts/check-config-env-docs.sh --write      regenerate the page and the catalog rows
#   scripts/check-config-env-docs.sh --self-test  build throwaway trees and prove it can fail
#   scripts/check-config-env-docs.sh --list       print the enumerated roster
#
# THREE ANSWERS: 0 clean / 1 the page and the code disagree, every name printed /
# 2 CANNOT LOOK. Never two: "I could not enumerate" is not "in sync".
set -uo pipefail
export LC_ALL=C

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
SRC="$ROOT/scripts/config-env-docs"

if [ ! -d "$SRC" ]; then
	echo "check-config-env-docs: CANNOT LOOK — the gate's own source is missing at $SRC." >&2
	exit 2
fi

if ! command -v go >/dev/null 2>&1; then
	echo "check-config-env-docs: CANNOT LOOK — no Go toolchain on PATH, so the gate was not built." >&2
	echo "  A gate that could not run is not a gate that passed." >&2
	exit 2
fi

# The caller points TMPDIR at a repo-local directory precisely because /tmp is noexec
# here; mktemp will not create its parent, so this does.
[ -n "${TMPDIR:-}" ] && mkdir -p "$TMPDIR" 2>/dev/null
BIN_DIR="$(mktemp -d 2>/dev/null)" || {
	echo "check-config-env-docs: CANNOT LOOK — could not create a scratch dir (TMPDIR=${TMPDIR:-unset})." >&2
	exit 2
}
trap 'rm -rf "$BIN_DIR"' EXIT
BIN="$BIN_DIR/config-env-docs"

build_err="$(cd "$SRC" && GOWORK=off go build -o "$BIN" . 2>&1)" || {
	echo "check-config-env-docs: CANNOT LOOK — the gate did not build." >&2
	printf '%s\n' "$build_err" | sed 's/^/    /' >&2
	exit 2
}

# Run it ONCE and read the shell's own verdict. 126/127 mean the shell could not
# execute the file at all — which in this container means /tmp is mounted noexec, and
# the bare message for that is "permission denied" on a file whose exec bit is set.
case "${1:-}" in
--self-test) "$BIN" --self-test ;;
--write) "$BIN" -root "$ROOT" -write ;;
--list) "$BIN" -root "$ROOT" -list ;;
"") "$BIN" -root "$ROOT" ;;
*)
	echo "check-config-env-docs: CANNOT LOOK — unknown argument '$1' (want --write, --list, --self-test or nothing)." >&2
	exit 2
	;;
esac
rc=$?

if [ "$rc" -eq 126 ] || [ "$rc" -eq 127 ]; then
	echo "check-config-env-docs: CANNOT LOOK — built the gate under TMPDIR=${TMPDIR:-/tmp} but the shell" >&2
	echo "  could not execute it (exit $rc). /tmp is mounted noexec in this container; set" >&2
	echo "  TMPDIR=/workspace/.olivares-tmptest and run again." >&2
	exit 2
fi
exit "$rc"
