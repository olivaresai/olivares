#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for check-runtime-base.sh, in both directions.
#
# The known-bad sample is the runtime base as a Dependabot update left it on 2026-10-10:
# node:25-trixie-slim in Dockerfile.release and in Dockerfile's runtime stage. Node 25 is an
# odd, never-LTS line, and patch-velocity's hard-coded node:24 parse found no pin, so the weekly
# rescan stopped before it scanned anything. The last case runs the gate on this repository.
# No git, no docker, no network.
set -euo pipefail
export LC_ALL=C

here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
GATE="$here/check-runtime-base.sh"
scratch="$(mktemp -d "${TMPDIR:-/tmp}/runtime-base.XXXXXX")"
trap 'rm -rf -- "$scratch"' EXIT
D24="sha256:$(printf '1%.0s' {1..64})"
D25="sha256:$(printf '2%.0s' {1..64})"
pass=0
fail=0
n=0

# tree RELEASE_FROM RUNTIME_FROM: a scratch root with both Dockerfiles; "-" omits a file.
tree() {
	n=$((n + 1))
	root="$scratch/$n"
	mkdir -p "$root"
	[ "$1" = - ] || printf '# comment\n%s\nRUN true\n' "$1" >"$root/Dockerfile.release"
	[ "$2" = - ] || printf 'FROM node:26-bookworm-slim AS web\nFROM golang:1.26.9-bookworm AS build\n%s\nRUN true\n' "$2" >"$root/Dockerfile"
}

# expect NAME RC [STDOUT]: run the gate on the last tree.
expect() {
	local rc=0 out
	out="$(bash "$GATE" "$root" 2>"$scratch/err")" || rc=$?
	# A refusal must name its class on stderr: a crash that happens to exit 1 is not a VIOLATION.
	case "$2" in 1) grep -q 'VIOLATION' "$scratch/err" || rc="$rc (no VIOLATION line)" ;; 2) grep -q 'COULD NOT LOOK' "$scratch/err" || rc="$rc (no COULD NOT LOOK line)" ;; esac
	if [ "$rc" = "$2" ] && { [ $# -lt 3 ] || [ "$out" = "$3" ]; }; then
		pass=$((pass + 1))
	else
		fail=$((fail + 1))
		printf 'FAIL %s: rc=%s want %s; stdout=%q\n' "$1" "$rc" "$2" "$out"
		sed 's/^/  /' "$scratch/err"
	fi
}

tree "FROM node:24-trixie-slim@$D24" "FROM node:24-trixie-slim@$D24"
expect "LTS base pinned in both files" 0 "node:24-trixie-slim $D24"
tree "FROM node:25-trixie-slim@$D25" "FROM node:25-trixie-slim@$D25"
expect "KNOWN-BAD: the 2026-10-10 Dependabot update moved both files to node:25" 1
tree "FROM node:24-trixie-slim@$D24" "FROM node:24-trixie-slim@$D25"
expect "runtime stage pinned to another digest" 1
tree "FROM node:24-trixie-slim@$D24" "FROM node:24-bookworm-slim@$D24"
expect "runtime stage on another tag" 1
tree "FROM node:24-trixie-slim" "FROM node:24-trixie-slim"
expect "base not pinned by digest" 1
tree "FROM node:24-trixie-slim@sha256:abc" "FROM node:24-trixie-slim@sha256:abc"
expect "digest shorter than 64 hex" 1
tree "FROM node:lts-trixie-slim@$D24" "FROM node:lts-trixie-slim@$D24"
expect "tag without a major" 1
tree "$(printf 'FROM node:24-trixie-slim@%s\nFROM node:24-trixie-slim@%s' "$D24" "$D24")" "FROM node:24-trixie-slim@$D24"
expect "two FROM lines in Dockerfile.release" 1
tree "FROM node:09-trixie-slim@$D25" "FROM node:09-trixie-slim@$D25"
expect "leading-zero major (bash would read 09 as octal)" 1
tree "FROM node:24-trixie-slim@$D24" "$(printf 'FROM node:24-trixie-slim@%s\nfrom node:25-trixie-slim@%s' "$D24" "$D25")"
expect "lowercase from appends a node:25 runtime stage" 1
tree "$(printf 'FROM node:24-trixie-slim@%s\n  from node:25-trixie-slim@%s' "$D24" "$D25")" "FROM node:24-trixie-slim@$D24"
expect "indented lowercase second FROM in Dockerfile.release" 1
tree "FROM node:24-trixie-slim@$D24" -
expect "no Dockerfile" 2
tree - "FROM node:24-trixie-slim@$D24"
expect "no Dockerfile.release" 2

root="$(cd -- "$here/.." && pwd)"
expect "this repository" 0

echo "test-check-runtime-base: $pass passed, $fail failed"
[ "$fail" -eq 0 ] && [ "$pass" -eq 14 ]
