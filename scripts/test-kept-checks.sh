#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# test-kept-checks.sh — scripts/kept-checks.sh on a throwaway repository whose tasks print the
# lines of a data file and fail when it is not empty. A base commit and a head commit differ in
# those files, so each verdict is exercised: pass, the same failure (also with moved line
# numbers, another temporary directory or one more success line), a new line, one more repeat of a line, a task that passed on base, could not look,
# a task base lacks, and a timeout.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# A hook exports GIT_DIR; without this, `git init` below would write to the caller's repository.
. "$ROOT/scripts/lib/git-env.sh"
command -v task >/dev/null || { echo "test-kept-checks: task is not on PATH" >&2; exit 2; }
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

R="$WORK/repo"
git init -q -b main "$R"
cd "$R"
git config user.email t@example.invalid
git config user.name test
mkdir -p scripts/lib data
cp "$ROOT/scripts/kept-checks.sh" scripts/
cp "$ROOT/scripts/lib/git-env.sh" scripts/lib/
taskfile() { # taskfile NAME...: one task per name that prints data/NAME.txt and fails if it has lines
	printf 'version: "3"\ntasks:\n'
	for n in "$@"; do
		printf '  %s:\n    cmds:\n      - sh -c '\''cat data/%s.txt; test ! -s data/%s.txt'\''\n' "$n" "$n" "$n"
	done
	printf '  blind:\n    cmds:\n      - sh -c '\''echo cannot look; exit 2'\''\n'
	printf '  slow:\n    cmds:\n      - sleep 5\n'
	for n in blinded flipped; do
		printf '  %s:\n    cmds:\n      - sh -c '\''echo same words; exit $(cat data/%s.rc)'\''\n' "$n" "$n"
	done
}
taskfile clean same moved grew repeat regressed covered tmp >Taskfile.yml
: >data/clean.txt
printf 'finding: same thing\n' >data/same.txt
printf 'finding at line 3\n' >data/moved.txt
printf 'finding: one\n' >data/grew.txt
printf 'finding: twice\nfinding: twice\n' >data/repeat.txt
: >data/regressed.txt
printf 'BROKEN: a.sh\n' >data/covered.txt
printf 'finding in %s/tmp.AAAAAAAA/x\n' "${TMPDIR:-/tmp}" >data/tmp.txt
echo 1 >data/blinded.rc
echo 0 >data/flipped.rc
git add -A
git commit -qm base
base="$(git rev-parse HEAD)"
taskfile clean same moved grew repeat regressed covered tmp added >Taskfile.yml
printf 'finding at line 9\n' >data/moved.txt
printf 'finding: one\nfinding: two\n' >data/grew.txt
printf 'finding: twice\nfinding: twice\nfinding: twice\n' >data/repeat.txt
printf 'finding: new\n' >data/regressed.txt
printf 'finding: new task\n' >data/added.txt
printf 'ok    b.sh isolates\nBROKEN: a.sh\n' >data/covered.txt
printf 'finding in %s/tmp.BBBBBBBB/x\n' "${TMPDIR:-/tmp}" >data/tmp.txt
echo 2 >data/blinded.rc
echo 1 >data/flipped.rc
git add -A
git commit -qm head

# verdict TASK [TIMEOUT]: "<exit code> <first word of the script's line for TASK>"
verdict() {
	local rc=0
	printf '%s\n' "$1" >"$WORK/list"
	KEPT_CHECK_TIMEOUT="${2:-60}" bash scripts/kept-checks.sh "$base" "$WORK/list" >"$WORK/out" 2>&1 || rc=$?
	echo "$rc $(awk 'NR==1 {print $1}' "$WORK/out")"
}
check "a passing task is ok" "$(verdict clean)" "0 ok"
check "a task that fails on base the same way is a warning" "$(verdict same)" "0 warn"
check "moved line numbers are the same failure" "$(verdict moved)" "0 warn"
check "a new success line is not a new failure" "$(verdict covered)" "0 warn"
check "another temporary directory is the same failure" "$(verdict tmp)" "0 warn"
check "a new line fails" "$(verdict grew)" "1 FAIL"
rc=0
grep -q 'finding: two' "$WORK/out" || rc=$?
check "and the new line is shown" "$rc" 0
check "one more repeat of a line fails" "$(verdict repeat)" "1 FAIL"
check "a task that passed on base fails" "$(verdict regressed)" "1 FAIL"
check "it fails even when it prints what base printed" "$(verdict flipped)" "1 FAIL"
check "could not look fails" "$(verdict blind)" "1 FAIL"
check "could not look here fails even when base printed the same" "$(verdict blinded)" "1 FAIL"
check "a failing task base lacks fails" "$(verdict added)" "1 FAIL"
check "a timeout fails" "$(verdict slow 1)" "1 FAIL"
printf 'clean\n# a comment\n\nsame\n' >"$WORK/list"
rc=0
bash scripts/kept-checks.sh "$base" "$WORK/list" >"$WORK/out" 2>&1 || rc=$?
check "passing and warned tasks pass; comments and blank lines are skipped" "$rc/$(grep -c . "$WORK/out")" 0/2
check "no base checkout is left" "$(git worktree list | grep -c .)" 1

echo "test-kept-checks: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
