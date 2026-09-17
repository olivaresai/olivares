#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-check-boundary.sh — fixture battery for scripts/check-boundary.sh.
#
# Drives the REAL gate inside throwaway trees. Isolated PATH controls whether
# `go` is visible; nothing here installs a toolchain or mutates the caller's PATH.
#
# Arms:
#   1. missing Go (isolated PATH) → exit 2 UNVERIFIED (already so at 13ca)
#   2. mutant that rewrites cannot-look to exit 0 — harness control, not a
#      baseline reproduction
#   3. healthy inspection with Go present → exit 0, Boundary check OK
#   4. an Apache module that imports /core → exit 1, BOUNDARY VIOLATION
#   5. a module with go.mod and no .go files is skipped, not a false clean of the
#      whole tree (empty-surface, while siblings still inspect)
#   6. mktemp cannot create the stderr file → exit 2 UNVERIFIED, not exit 1
#
# Exit 0 = every case passed. Exit 1 = a named case failed.
# Exit 2 = could not run the battery — NOT a pass.
set -u

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)"

# shellcheck source=/dev/null
. "$HERE/lib/git-env.sh" || {
	echo "test-check-boundary: FATAL: cannot source $HERE/lib/git-env.sh" >&2
	exit 2
}

command -v bash >/dev/null 2>&1 || {
	echo "test-check-boundary: no bash — could not run. NOT a pass." >&2
	exit 2
}
command -v python3 >/dev/null 2>&1 || {
	echo "test-check-boundary: no python3 — could not run the mutant. NOT a pass." >&2
	exit 2
}
ROOT="$(cd "$HERE/.." && pwd)"
GATE="$ROOT/scripts/check-boundary.sh"
[ -f "$GATE" ] || {
	echo "test-check-boundary: $GATE not found — could not run. NOT a pass." >&2
	exit 2
}

WORK="$(mktemp -d "${TMPDIR:-/tmp}/check-boundary-tests.XXXXXX")" || {
	echo "test-check-boundary: cannot create a scratch directory — NOT a pass." >&2
	exit 2
}
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT INT TERM

fails=0
pass() { printf '  ok   %s\n' "$1"; }
fail() {
	printf '  FAIL %s\n' "$1" >&2
	printf '       %s\n' "$2" >&2
	fails=$((fails + 1))
}

# Tools the gate itself needs besides `go`. Isolated PATH for the missing-go arm
# contains these and nothing that looks like a Go toolchain.
NEED_TOOLS=(bash dirname basename pwd find mktemp grep sed cat rm mkdir chmod ln mv cp ls uname)

nogo_path() {
	local bin="$WORK/nogo-bin" t src
	mkdir -p "$bin"
	for t in "${NEED_TOOLS[@]}"; do
		src="$(command -v "$t")" || {
			echo "test-check-boundary: need $t on PATH to build the isolated fixture" >&2
			return 1
		}
		ln -s "$src" "$bin/$t"
	done
	printf '%s' "$bin"
}

# A tree the REAL gate will accept as the repository root (script-relative).
# All six forbidden modules plus core exist. `empty_mod` has go.mod and no .go
# files (legitimate empty surface). Others have a stdlib-only package.
write_gomod() { # write_gomod <dir> <module-path>
	mkdir -p "$1"
	printf 'module %s\n\ngo 1.26.5\n' "$2" >"$1/go.mod"
}

make_tree() { # make_tree <name> [violate]
	local d="$WORK/$1" violate="${2:-}"
	rm -rf "$d"
	mkdir -p "$d/scripts"
	cp "$GATE" "$d/scripts/check-boundary.sh"
	write_gomod "$d/sdk" "github.com/olivaresai/olivares/sdk"
	printf 'package sdk\n' >"$d/sdk/sdk.go"
	write_gomod "$d/sdk/plugin" "github.com/olivaresai/olivares/sdk/plugin"
	printf 'package plugin\n' >"$d/sdk/plugin/plugin.go"
	write_gomod "$d/sdk/scaffold" "github.com/olivaresai/olivares/sdk/scaffold"
	# no .go — empty surface for this module only
	write_gomod "$d/connectors" "github.com/olivaresai/olivares/connectors"
	if [ "$violate" = "violate" ]; then
		write_gomod "$d/core" "github.com/olivaresai/olivares/core"
		printf 'package core\n' >"$d/core/doc.go"
		printf 'module github.com/olivaresai/olivares/connectors\n\ngo 1.26.5\n\nrequire github.com/olivaresai/olivares/core v0.0.0\n\nreplace github.com/olivaresai/olivares/core => ../core\n' \
			>"$d/connectors/go.mod"
		printf 'package connectors\n\nimport _ "github.com/olivaresai/olivares/core"\n' \
			>"$d/connectors/c.go"
	else
		printf 'package connectors\n' >"$d/connectors/c.go"
	fi
	write_gomod "$d/clients/go" "github.com/olivaresai/olivares/clients/go"
	printf 'package clients\n' >"$d/clients/go/doc.go"
	write_gomod "$d/clients/generator" "github.com/olivaresai/olivares/clients/generator"
	printf 'package generator\n' >"$d/clients/generator/doc.go"
	write_gomod "$d/core" "github.com/olivaresai/olivares/core"
	printf 'package core\n' >"$d/core/doc.go"
	printf '%s' "$d"
}

run_gate() { # run_gate <tree> <path> [tmpdir] → writes $WORK/out and prints rc
	local tree="$1" path="$2" tmp="${3:-$WORK/tmp}" rc=0
	(cd "$tree" && env -i \
		PATH="$path" \
		HOME="$WORK/home" \
		TMPDIR="$tmp" \
		GOPROXY=off \
		GOSUMDB=off \
		GOWORK=off \
		GO111MODULE=on \
		CGO_ENABLED=0 \
		GOCACHE="${GOCACHE:-$WORK/gocache}" \
		GOROOT="${GOROOT:-}" \
		TERM=dumb \
		LC_ALL=C \
		bash scripts/check-boundary.sh >"$WORK/out" 2>&1) || rc=$?
	printf '%s' "$rc"
}

expect() { # expect <label> <rc-got> <rc-want> <substring>
	local label="$1" got="$2" want="$3" sub="$4"
	if [ "$got" != "$want" ]; then
		fail "$label" "exit $got, wanted $want — $(tr '\n' ' ' <"$WORK/out" | head -c 240)"
		return
	fi
	if grep -F -q -- "$sub" "$WORK/out"; then
		pass "$label"
	else
		fail "$label" "exit code right ($got) but never said '$sub'. Got: $(tr '\n' ' ' <"$WORK/out" | head -c 240)"
	fi
}

mkdir -p "$WORK/tmp" "$WORK/home" "$WORK/gocache"

NOGO="$(nogo_path)" || exit 2

printf 'test-check-boundary: fixture battery\n'

# ------------------------------------------------------------------ 1. missing Go
T="$(make_tree missing-go)"
rc="$(run_gate "$T" "$NOGO")"
expect "1 missing go on isolated PATH is UNVERIFIED exit 2" "$rc" "2" "UNVERIFIED"

# ------------------------------------------------------------------ 2. mutant: harness control, not a baseline reproduction
MUT="$WORK/mutant-check-boundary.sh"
if ! python3 - "$GATE" "$MUT" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
s = open(src, encoding="utf-8").read()
old = "exit 2"
if old not in s:
    sys.exit(3)
open(dst, "w", encoding="utf-8").write(s.replace(old, "exit 0"))
PY
then
	fail "2 mutant applies" "could not rewrite exit 2 → exit 0 in a copy of the gate"
else
	T="$(make_tree missing-go-mut)"
	cp "$MUT" "$T/scripts/check-boundary.sh"
	rc="$(run_gate "$T" "$NOGO")"
	expect "2 mutant (cannot-look rewritten to exit 0) is a harness control" "$rc" "0" "UNVERIFIED"
fi

# ------------------------------------------------------------------ 3. healthy inspection
if ! command -v go >/dev/null 2>&1; then
	fail "3 healthy inspection" "go is not on PATH in the battery environment — cannot prove a successful look"
else
	GO_PATH="$(dirname "$(command -v go)"):$NOGO"
	# GOROOT must travel with a relocated `go` binary.
	if [ -z "${GOROOT:-}" ]; then
		GOROOT="$(go env GOROOT 2>/dev/null || true)"
	fi
	export GOROOT
	T="$(make_tree healthy)"
	rc="$(run_gate "$T" "$GO_PATH")"
	expect "3 healthy tree with go present is OK" "$rc" "0" "Boundary check OK"
	if grep -F -q "BOUNDARY VIOLATION" "$WORK/out"; then
		fail "3b healthy tree names no violation" "$(tr '\n' ' ' <"$WORK/out" | head -c 240)"
	else
		pass "3b healthy tree names no violation"
	fi
	if grep -F -q "(no Go sources)" "$WORK/out"; then
		pass "3c scaffold with no .go files is empty-surface, not a false clean of the tree"
	else
		fail "3c empty-surface" "expected '(no Go sources)' for sdk/scaffold. Got: $(tr '\n' ' ' <"$WORK/out" | head -c 240)"
	fi
fi

# ------------------------------------------------------------------ 4. actual violation
if command -v go >/dev/null 2>&1; then
	GO_PATH="$(dirname "$(command -v go)"):$NOGO"
	if [ -z "${GOROOT:-}" ]; then
		GOROOT="$(go env GOROOT 2>/dev/null || true)"
	fi
	export GOROOT
	T="$(make_tree violate violate)"
	rc="$(run_gate "$T" "$GO_PATH")"
	expect "4 connectors importing core is BOUNDARY VIOLATION exit 1" "$rc" "1" "BOUNDARY VIOLATION"
	if grep -F -q "UNVERIFIED" "$WORK/out"; then
		fail "4b violation is not reported as cannot-look" "$(tr '\n' ' ' <"$WORK/out" | head -c 240)"
	else
		pass "4b violation is distinct from UNVERIFIED"
	fi

	T="$(make_tree mktemp-fail)"
	rc="$(run_gate "$T" "$GO_PATH" "$WORK/does-not-exist")"
	expect "5 mktemp failure is UNVERIFIED exit 2, not a violation" "$rc" "2" "could not create a temp file"
	if grep -F -q "BOUNDARY VIOLATION" "$WORK/out"; then
		fail "5b mktemp failure is not a violation" "$(tr '\n' ' ' <"$WORK/out" | head -c 240)"
	else
		pass "5b mktemp failure is distinct from a violation"
	fi
else
	fail "4 actual violation" "go is not on PATH in the battery environment — cannot prove a real violation"
fi

if [ "$fails" -ne 0 ]; then
	printf 'test-check-boundary: %d FAILED\n' "$fails" >&2
	exit 1
fi
printf 'test-check-boundary: OK\n'
exit 0
