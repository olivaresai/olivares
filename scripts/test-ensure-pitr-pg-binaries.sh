#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-ensure-pitr-pg-binaries.sh — offline selftest for scripts/ensure-pitr-pg-binaries.sh.
#
# The negative cases need no PostgreSQL packages: they feed the helper artifacts that must be
# refused. The positive case needs the real pinned PGDG debs and a host with glibc >= 2.38
# (the runners); it is run by hand against the same --artifact-dir and pasted into the PR.
set -uo pipefail

here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
script="$here/ensure-pitr-pg-binaries.sh"
workflow="$here/../.github/workflows/drills-nightly.yml"
work="$(mktemp -d "${TMPDIR:-/tmp}/test-pitr-pg.XXXXXX")"
trap 'rm -rf -- "$work"' EXIT

fails=0
pass() { printf 'ok   %s\n' "$1"; }
fail() {
	printf 'FAIL %s\n' "$1"
	shift
	printf '    %s\n' "$@"
	fails=$((fails + 1))
}

# expect_rc NAME WANT_RC COMMAND...: the command must exit WANT_RC; its output is shown on failure.
expect_rc() {
	local name=$1 want=$2 out rc
	shift 2
	out="$("$@" 2>&1)"
	rc=$?
	if [[ $rc -eq $want ]]; then
		pass "$name"
	else
		fail "$name: rc=$rc, want $want" "$out"
	fi
}

expect_rc "refuses without --dest" 2 bash "$script"
expect_rc "refuses a relative --dest" 2 bash "$script" --dest relative/dir
expect_rc "refuses an unknown argument" 2 bash "$script" --dest "$work/d" --bogus

# Every pinned artifact name, with the wrong bytes: size and digest checks must refuse it.
mkdir -p "$work/wrong"
for pkg in postgresql-15 postgresql-16 postgresql-17 postgresql-18 \
	postgresql-client-15 postgresql-client-16 postgresql-client-17 postgresql-client-18 libpq5; do
	printf 'not a package\n' >"$work/wrong/$pkg.deb"
done
expect_rc "refuses artifacts that are not the pinned packages" 2 \
	bash "$script" --dest "$work/d1" --artifact-dir "$work/wrong"

mkdir -p "$work/empty"
expect_rc "refuses an empty --artifact-dir" 2 bash "$script" --dest "$work/d2" --artifact-dir "$work/empty"

# A refused run must publish nothing: no root, no stage left behind under --dest.
if [[ -e "$work/d1/root" || -n "$(ls -A "$work/d1" 2>/dev/null)" ]]; then
	fail "a refused run leaves nothing published under --dest" "$(ls -A "$work/d1")"
else
	pass "a refused run leaves nothing published under --dest"
fi

# The workflow must provision the binaries on self-hosted runners and export them to later steps.
if grep -q 'ensure-pitr-pg-binaries.sh' "$workflow" && grep -q "runner.environment != 'github-hosted'" "$workflow"; then
	pass "drills-nightly pitr job provisions the binaries on self-hosted runners"
else
	fail "drills-nightly pitr job provisions the binaries on self-hosted runners" \
		"no ensure-pitr-pg-binaries.sh step guarded by runner.environment != 'github-hosted' in $workflow"
fi

if ((fails)); then
	printf '%d failing\n' "$fails"
	exit 1
fi
printf 'all ok\n'
