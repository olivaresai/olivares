#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-guide-docs.sh — build and run the generated guide gate: the CONSOLE reference,
# the gRPC reference, and the claims of the UPGRADE guide.
#
# THE REGRESSION IT FORBIDS. Measured 2026-08-15 and recorded in
# an internal design note (not shipped):74: "Consola: 0 de 57 rutas con guía … Sin guía
# de actualización de ninguna clase … Sin referencia gRPC (28 rpc)". Three public surfaces
# at zero, and nothing red anywhere, because — :79 of the same document — there is a
# coverage gate for modules and there was none for screens, upgrades or rpc. A page
# somebody has to remember to extend is stale the day the 58th route lands, so the roster
# is ENUMERATED from the tree and the page is REGENERATED from it; this gate fails when the
# two disagree.
#
# TWO STAGES, because two of the three rosters live in two languages:
#
#   1. `node scripts/guide-docs/console-dump.mjs` reads the console's routes with the
#      TypeScript compiler the console itself is built with, and writes them as JSON. It is
#      the same discipline scripts/check-console-perms.mjs established: a guard that parses
#      the console with a different compiler is reading a different language than the one
#      that ships. That check already runs earlier in the same push gate
#      (.githooks/pre-push:598), so this stage adds no new precondition to the lane.
#   2. scripts/guide-docs renders that JSON — plus the gRPC registration tables and
#      core/release, both read with go/parser — into the pages' generated regions and
#      compares. It is a standalone module built with GOWORK=off, like
#      scripts/cli-ref-docs and scripts/config-env-docs, so a broken module elsewhere in
#      the workspace cannot stop this gate from looking.
#
#   scripts/check-guide-docs.sh              check the published guides against the tree
#   scripts/check-guide-docs.sh --write      regenerate them
#   scripts/check-guide-docs.sh --self-test  build throwaway trees and prove it can fail
#   scripts/check-guide-docs.sh --list       print the enumerated rosters
#
# THREE ANSWERS: 0 clean / 1 the tree and the pages disagree, every difference printed /
# 2 CANNOT LOOK. Never two: an enumeration that did not run is not "in sync".
set -uo pipefail
export LC_ALL=C

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
SRC="$ROOT/scripts/guide-docs"
MODE="${1:-}"

if [ ! -d "$SRC" ]; then
	echo "check-guide-docs: CANNOT LOOK — the gate's own source is missing at $SRC." >&2
	exit 2
fi

if ! command -v go >/dev/null 2>&1; then
	echo "check-guide-docs: CANNOT LOOK — no Go toolchain on PATH, so neither the gRPC" >&2
	echo "  registration tables nor the release channels were enumerated. A gate that could not" >&2
	echo "  run is not a gate that passed." >&2
	exit 2
fi
if ! command -v node >/dev/null 2>&1; then
	echo "check-guide-docs: CANNOT LOOK — no node on PATH, so the console's routes were never" >&2
	echo "  read out of web/src." >&2
	exit 2
fi

# The caller points TMPDIR at a repo-local directory precisely because /tmp is noexec here;
# mktemp will not create its parent, so this does.
[ -n "${TMPDIR:-}" ] && mkdir -p "$TMPDIR" 2>/dev/null
SCRATCH="$(mktemp -d 2>/dev/null)" || {
	echo "check-guide-docs: CANNOT LOOK — could not create a scratch dir (TMPDIR=${TMPDIR:-unset})." >&2
	exit 2
}
trap 'rm -rf "$SCRATCH"' EXIT
export GOTMPDIR="${GOTMPDIR:-$SCRATCH}"

BIN="$SCRATCH/guide-docs"
build_err="$(cd "$SRC" && GOWORK=off go build -o "$BIN" . 2>&1)" || {
	echo "check-guide-docs: CANNOT LOOK — the gate did not build." >&2
	printf '%s\n' "$build_err" | sed 's/^/    /' >&2
	exit 2
}

noexec_hint() {
	echo "check-guide-docs: CANNOT LOOK — built the gate under TMPDIR=${TMPDIR:-/tmp} but the" >&2
	echo "  shell could not execute it (exit $1). /tmp is mounted noexec in this container; set" >&2
	echo "  TMPDIR=/workspace/.olivares-tmptest and run again." >&2
	exit 2
}

# --self-test needs no tree: it plants its own fixtures.
if [ "$MODE" = "--self-test" ]; then
	"$BIN" --self-test -stage1 "$ROOT/scripts/guide-docs/console-dump.mjs"
	rc=$?
	if [ "$rc" -eq 126 ] || [ "$rc" -eq 127 ]; then noexec_hint "$rc"; fi
	exit "$rc"
fi

# ── stage 1: read the console's routes ────────────────────────────────────────────────
#
# NOT `|| true`. If the dump fails — the TypeScript compiler is not installed, the registry
# stopped being an array literal, a view's permission became a computed expression — then
# there is no trustworthy roster, and "I could not enumerate" must never be reported as
# "the page is in sync". The dumper's own diagnostic is printed: it names what broke far
# better than this script could.
# THE ORDER OF THE TWO REDIRECTIONS IS THE WHOLE POINT, and it was wrong until 2026-08-17.
# `>"$DUMP" 2>&1` sends stdout to the file and THEN points stderr at wherever stdout now
# is — the file — so the dumper's diagnostic was appended to the JSON and thrown away with
# the scratch dir, and `dump_err` captured the empty string. Measured on two of this
# gate's own refusals: with FEATURE_VIEWS turned into a call expression, and with
# web/node_modules absent, the operator got "the console route dump failed (node exit 2)"
# followed by a line of four spaces, while console-dump.mjs had written
# "no TypeScript compiler under web/node_modules ...; run `pnpm --dir web install`" into
# the dump file. Exiting 2 with the cause deleted still fails closed, but it fails closed
# WITHOUT saying what was missing, which is the half of the rule that makes the answer
# usable. `2>&1 >"$DUMP"` duplicates stderr onto the command substitution's pipe FIRST and
# only then redirects stdout to the file.
DUMP="$SCRATCH/console-routes.json"
dump_err="$(cd "$ROOT" && node scripts/guide-docs/console-dump.mjs 2>&1 >"$DUMP")"
dump_rc=$?
if [ "$dump_rc" -ne 0 ]; then
	echo "check-guide-docs: CANNOT LOOK — the console route dump failed (node exit $dump_rc), so" >&2
	echo "  the console reference was not checked against the console at all." >&2
	printf '%s\n' "$dump_err" | sed 's/^/    /' >&2
	exit 2
fi
if [ ! -s "$DUMP" ]; then
	echo "check-guide-docs: CANNOT LOOK — the dump reported success and wrote no routes to $DUMP." >&2
	echo "  A stage that succeeds without producing its artifact is a skip in disguise." >&2
	exit 2
fi

# ── stage 2: render and compare ───────────────────────────────────────────────────────
case "$MODE" in
--write) "$BIN" -root "$ROOT" -dump "$DUMP" -write ;;
--list) "$BIN" -root "$ROOT" -dump "$DUMP" -list ;;
"") "$BIN" -root "$ROOT" -dump "$DUMP" ;;
*)
	echo "check-guide-docs: CANNOT LOOK — unknown argument '$MODE' (want --write, --list," >&2
	echo "  --self-test or nothing)." >&2
	exit 2
	;;
esac
rc=$?

if [ "$rc" -eq 126 ] || [ "$rc" -eq 127 ]; then noexec_hint "$rc"; fi
exit "$rc"
