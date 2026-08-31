#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-cli-ref-docs.sh — build and run the generated CLI-reference gate.
#
# THE REGRESSION IT FORBIDS. Measured 2026-08-16: the binary registers 700 command
# nodes carrying 2209 flags, and docs-site/src/content/docs/reference/cli.md
# documented FOUR of them, with a hand-written table listing 7 of `serve`'s 19
# flags. Nothing was red — there is a coverage gate for modules and there was none
# for commands. A list somebody has to remember to extend is stale the day the
# 701st command lands, so the roster is ENUMERATED FROM THE BINARY and the page is
# REGENERATED from it; this gate fails when the two disagree.
#
# TWO STAGES, because the tree only exists at RUNTIME. newRootCmd() adds command
# groups conditionally, and every flag's type, default and usage lives in a pflag
# struct rather than in text, so neither a grep nor a parse of the sources can
# enumerate this surface:
#
#   1. `go test -run TestCLIRefDump ./cmd/olivares` walks the real cobra tree
#      in-package and writes it as JSON. It is a test and not a new hidden
#      subcommand so the shipped binary carries nothing for the docs' sake; the
#      two routes were measured at the same cost (2.4s build vs 2.6s test, warm).
#   2. scripts/cli-ref-docs renders that JSON into the page's generated region and
#      compares. It is a standalone module built with GOWORK=off, like
#      scripts/config-env-docs, so a broken module elsewhere in the workspace
#      cannot stop this gate from looking.
#
#   scripts/check-cli-ref-docs.sh              check the published page against the binary
#   scripts/check-cli-ref-docs.sh --write      regenerate the page
#   scripts/check-cli-ref-docs.sh --self-test  build throwaway trees and prove it can fail
#   scripts/check-cli-ref-docs.sh --list       print the enumerated command roster
#
# THREE ANSWERS: 0 clean / 1 the page and the binary disagree, every difference
# printed / 2 CANNOT LOOK. Never two: a walk that did not run is not "in sync".
set -uo pipefail
export LC_ALL=C

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
SRC="$ROOT/scripts/cli-ref-docs"
MODE="${1:-}"

if [ ! -d "$SRC" ]; then
	echo "check-cli-ref-docs: CANNOT LOOK — the gate's own source is missing at $SRC." >&2
	exit 2
fi

if ! command -v go >/dev/null 2>&1; then
	echo "check-cli-ref-docs: CANNOT LOOK — no Go toolchain on PATH, so the command tree was never" >&2
	echo "  enumerated. A gate that could not run is not a gate that passed." >&2
	exit 2
fi

# The caller points TMPDIR at a repo-local directory precisely because /tmp is
# noexec here; mktemp will not create its parent, so this does.
[ -n "${TMPDIR:-}" ] && mkdir -p "$TMPDIR" 2>/dev/null
SCRATCH="$(mktemp -d 2>/dev/null)" || {
	echo "check-cli-ref-docs: CANNOT LOOK — could not create a scratch dir (TMPDIR=${TMPDIR:-unset})." >&2
	exit 2
}
trap 'rm -rf "$SCRATCH"' EXIT
export GOTMPDIR="${GOTMPDIR:-$SCRATCH}"

BIN="$SCRATCH/cli-ref-docs"
build_err="$(cd "$SRC" && GOWORK=off go build -o "$BIN" . 2>&1)" || {
	echo "check-cli-ref-docs: CANNOT LOOK — the gate did not build." >&2
	printf '%s\n' "$build_err" | sed 's/^/    /' >&2
	exit 2
}

# --self-test needs no tree walk for its first half: the generator's own battery
# plants throwaway fixtures. The second half does, and is a separate script for a
# reason recorded there — the generator's battery links the generator and therefore
# cannot see THIS file, so two mutants of the stages below (a walk failure swallowed
# with `|| true`, and the "wrote no artifact" guard removed) passed all 28 of its
# cases while the wrapper answered "OK, in sync" on a walk whose assertions failed.
if [ "$MODE" = "--self-test" ]; then
	"$BIN" --self-test
	rc=$?
	[ "$rc" -eq 126 ] || [ "$rc" -eq 127 ] && {
		echo "check-cli-ref-docs: CANNOT LOOK — built the gate under TMPDIR=${TMPDIR:-/tmp} but the" >&2
		echo "  shell could not execute it (exit $rc). /tmp is mounted noexec in this container; set" >&2
		echo "  TMPDIR=/workspace/.olivares-tmptest and run again." >&2
		exit 2
	}
	[ "$rc" -eq 0 ] || exit "$rc"

	# Its absence is CANNOT LOOK, not "nothing to report": the stages this file owns
	# would then have no witness at all, which is the state the battery exists to end.
	if [ ! -f "$ROOT/scripts/test-cli-ref-wrapper.sh" ]; then
		echo "check-cli-ref-docs: CANNOT LOOK — scripts/test-cli-ref-wrapper.sh is missing, so this" >&2
		echo "  script's own CANNOT LOOK stages were never exercised." >&2
		exit 2
	fi
	bash "$ROOT/scripts/test-cli-ref-wrapper.sh"
	exit $?
fi

# ── stage 1: walk the real cobra tree ─────────────────────────────────────────────────
#
# NOT `|| true`. If the walk fails — the package does not compile, a command lost
# its --help, a flag default started reading the environment — then there is no
# trustworthy enumeration, and "I could not enumerate" must never be reported as
# "the page is in sync". The test's own output is printed, because it names what
# broke far better than this script could.
DUMP="$SCRATCH/cli-tree.json"
test_out="$(cd "$ROOT" && OLIVARES_CLIREF_DUMP_OUT="$DUMP" \
	go test -run '^TestCLIRefDump$' -count=1 ./cmd/olivares 2>&1)"
test_rc=$?
if [ "$test_rc" -ne 0 ]; then
	echo "check-cli-ref-docs: CANNOT LOOK — the command-tree walk failed, so the CLI reference was" >&2
	echo "  not checked against the binary at all (go test exit $test_rc)." >&2
	printf '%s\n' "$test_out" | sed 's/^/    /' >&2
	exit 2
fi
if [ ! -s "$DUMP" ]; then
	echo "check-cli-ref-docs: CANNOT LOOK — the walk reported success but wrote no command tree to" >&2
	echo "  $DUMP. A test that passes without producing its artifact is a skip in disguise." >&2
	printf '%s\n' "$test_out" | sed 's/^/    /' >&2
	exit 2
fi

# ── stage 2: render and compare ───────────────────────────────────────────────────────
case "$MODE" in
--write) "$BIN" -root "$ROOT" -dump "$DUMP" -write ;;
--list) "$BIN" -root "$ROOT" -dump "$DUMP" -list ;;
"") "$BIN" -root "$ROOT" -dump "$DUMP" ;;
*)
	echo "check-cli-ref-docs: CANNOT LOOK — unknown argument '$MODE' (want --write, --list," >&2
	echo "  --self-test or nothing)." >&2
	exit 2
	;;
esac
rc=$?

# 126/127 mean the shell could not execute the file at all — which in this
# container means /tmp is mounted noexec, and the bare message for that is
# "permission denied" on a file whose exec bit is set.
if [ "$rc" -eq 126 ] || [ "$rc" -eq 127 ]; then
	echo "check-cli-ref-docs: CANNOT LOOK — built the gate under TMPDIR=${TMPDIR:-/tmp} but the shell" >&2
	echo "  could not execute it (exit $rc). /tmp is mounted noexec in this container; set" >&2
	echo "  TMPDIR=/workspace/.olivares-tmptest and run again." >&2
	exit 2
fi
exit "$rc"
