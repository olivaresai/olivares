#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# test-changed-go-packages.sh — scripts/changed-go-packages.sh on a throwaway workspace: a
# changed .go, testdata or embedded file maps to its package; a directory outside go.work, with
# or without its own go.mod, or whose files build tags exclude, is named on stderr, not selected;
# an unreadable range and a package that fails to load exit non-zero.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# A hook exports GIT_DIR; without this, `git init` below would write to the caller's repository.
. "$ROOT/scripts/lib/git-env.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
pass=0
fail=0
check() {
	if [ "$2" = "$3" ]; then
		pass=$((pass + 1))
	else
		fail=$((fail + 1))
		printf 'FAIL %s\n  want: %s\n  got:  %s\n' "$1" "$3" "$2"
	fi
}

cd "$WORK"
git init -q -b main .
git config user.email t@example.invalid
git config user.name test
mkdir -p scripts m/a m/b/testdata m/c/migrations/postgres m/e2e x priv docs
cp "$ROOT/scripts/changed-go-packages.sh" scripts/
printf 'go 1.26\n\nuse ./m\n' > go.work
printf 'module example.invalid/m\n\ngo 1.26\n' > m/go.mod
printf 'package a\n' > m/a/a.go
printf 'package b\n' > m/b/b.go
printf '{}\n' > m/b/testdata/case.json
printf 'package c\n' > m/c/schema.go
printf '//go:build e2e\n\npackage e2e\n' > m/e2e/e2e_test.go
printf 'SELECT 1;\n' > m/c/migrations/postgres/001.sql
printf 'package x\n' > x/x.go
printf 'module example.invalid/priv\n\ngo 1.26\n' > priv/go.mod
printf 'package priv\n' > priv/p.go
printf 'base\n' > docs/readme.md
git add -A
git commit -qm base
base="$(git rev-parse HEAD)"
printf 'package a\n\nconst A = 1\n' > m/a/a.go
printf '{"changed": true}\n' > m/b/testdata/case.json
printf 'SELECT 2;\n' > m/c/migrations/postgres/001.sql
printf '//go:build e2e\n\npackage e2e\n\nconst E = 1\n' > m/e2e/e2e_test.go
printf 'package x\n\nconst X = 1\n' > x/x.go
printf 'package priv\n\nconst P = 1\n' > priv/p.go
printf 'changed\n' > docs/readme.md
git commit -qam change

cgp() { bash scripts/changed-go-packages.sh "$@"; }
check "a changed .go, testdata or embedded file maps to its package" \
	"$(cgp "$base" 2>"$WORK/err" | paste -sd,)" "./m/a,./m/b,./m/c"
check "each directory outside go.work is named on one line" \
	"$(grep -c 'not a workspace package' "$WORK/err")" 2
check "a package whose files build tags exclude is named, not a failure" \
	"$(grep -c 'build tags exclude every file: ./m/e2e' "$WORK/err")/$(wc -l <"$WORK/err" | tr -d ' ')" 1/3
check "an explicit head reads the same range" "$(cgp "$base" HEAD 2>/dev/null | paste -sd,)" "./m/a,./m/b,./m/c"
check "no change is no package" "$(cgp HEAD | wc -l | tr -d ' ')" 0
rc=0
cgp no-such-ref >/dev/null 2>&1 || rc=$?
check "an unreadable range exits 2" "$rc" 2
rc=0
cgp >/dev/null 2>&1 || rc=$?
check "a missing base exits 2" "$rc" 2

# A package inside a workspace module must not disappear when loading it fails.
printf 'package wrong_test\n' > m/a/a_test.go
git add m/a/a_test.go
git commit -qm 'mismatched test package'
rc=0
cgp HEAD^ >"$WORK/out" 2>"$WORK/err" || rc=$?
check "a workspace package loading error fails selection" "$((rc != 0))" 1
check "the package loading diagnostic is preserved" \
	"$(grep -c 'found packages' "$WORK/err" || true)" 1
check "a loading error is not an out-of-workspace skip" \
	"$(grep -c 'not a workspace package' "$WORK/err" || true)" 0

echo "test-changed-go-packages: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
