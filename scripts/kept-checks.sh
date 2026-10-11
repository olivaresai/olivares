#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# kept-checks.sh BASE [LIST] — every task in LIST (ci/kept-checks.txt), the checks nothing else
# runs, as a ratchet against BASE. A task that passes here is fine. A task that fails here is
# run on a checkout of BASE too:
#   - it fails this script when BASE passes it, or when its output gained lines BASE's lacks
#     (paths, hex ids and numbers normalized, success lines dropped, repeats counted);
#   - it is a warning when BASE fails it with the same output;
#   - exit 2 ("could not look"), a timeout, or the same on BASE, always fails.
# Each task runs with `task -x`, so its own exit code counts, for KEPT_CHECK_TIMEOUT seconds
# (600). Prints one line per task and each failure's new lines; exits 1 when any task fails.
set -euo pipefail

[ "$#" -ge 1 ] && [ "$#" -le 2 ] || { echo "usage: $0 BASE [LIST]" >&2; exit 2; }
base="$1"
ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"
. scripts/lib/git-env.sh
list="${2:-ci/kept-checks.txt}"
limit="${KEPT_CHECK_TIMEOUT:-600}"
work="$(mktemp -d)"
trap 'git worktree remove --force "$work/base" >/dev/null 2>&1; rm -rf "$work"' EXIT
git worktree add --quiet --detach "$work/base" "$base"

# The output's lines, comparable across the two checkouts and across runs: the checkout and
# temporary paths, hex ids and numbers normalized, success lines (ok, PASS, a check mark)
# dropped, so a check that now covers one more file is not a new failure.
norm() {
	sed -E -e "s#$ROOT#<root>#g; s#$work/base#<root>#g; s#${TMPDIR:-/tmp}/[^/[:space:]:]*#<tmp>#g" \
		-e 's/\x1b\[[0-9;]*m//g' -e 's/[0-9a-f]{7,64}/<id>/g; s/[0-9]+/N/g' "$1" |
		awk '!/^[[:space:]]*(ok|PASS)([[:space:]]|$)/ && !/^[[:space:]]*(✓|✅)/ { print $0 " #" ++n[$0] }' | sort
}
run() { # run DIR TASK OUT: prints the task's exit code
	local rc=0
	(cd "$1" && timeout -k 10 "$limit" task -x "$2") >"$3" 2>&1 </dev/null || rc=$?
	echo "$rc"
}
blind() { [ "$1" = 2 ] || [ "$1" -ge 124 ]; }

failed=0
while IFS= read -r t; do
	case "$t" in '' | '#'*) continue ;; esac
	s=$SECONDS
	rc="$(run "$ROOT" "$t" "$work/head.log")"
	took=$((SECONDS - s))
	if [ "$rc" = 0 ]; then
		echo "ok    $t (${took}s)"
		continue
	fi
	if blind "$rc"; then
		echo "FAIL  $t: could not look or timed out (rc $rc, ${took}s)"
		tail -20 "$work/head.log" | sed 's/^/      /'
		failed=1
		continue
	fi
	brc="$(run "$work/base" "$t" "$work/base.log")"
	if [ "$brc" = 0 ] || blind "$brc"; then
		echo "FAIL  $t: rc $rc here, rc $brc on $base (${took}s)"
		tail -20 "$work/head.log" | sed 's/^/      /'
		failed=1
		continue
	fi
	new="$(comm -13 <(norm "$work/base.log") <(norm "$work/head.log"))"
	if [ -n "$new" ]; then
		echo "FAIL  $t: new output since $base (rc $rc, ${took}s)"
		printf '%s\n' "$new" | sed 's/ #[0-9]*$//; s/^/      /' | head -20
		failed=1
	else
		echo "warn  $t: fails on $base the same way (rc $rc, ${took}s)"
	fi
done <"$list"
exit "$failed"
