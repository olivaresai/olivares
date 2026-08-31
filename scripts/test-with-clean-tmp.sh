#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
set -u -o pipefail

HERE="$(cd -- "$(dirname -- "$0")" && pwd)"
SUT="$HERE/with-clean-tmp.sh"
[ -r "$SUT" ] || { echo "test-with-clean-tmp: cannot read $SUT" >&2; exit 2; }
BASE="$(mktemp -d "${TMPDIR:-/tmp}/with-clean-tmp-test.XXXXXX")" || exit 2
trap 'chmod -R u+rwX "$BASE" 2>/dev/null; rm -rf -- "$BASE"' EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir -p "$BASE/outer"

pass=0
fail=0
check() {
	if "$@"; then pass=$((pass + 1)); else fail=$((fail + 1)); fi
}
outer_empty() {
	local residue
	residue="$(find "$BASE/outer" -mindepth 1 -maxdepth 1 -printf x)" || return 1
	[ -z "$residue" ]
}

printf '#!/usr/bin/env bash\nmkdir -p "$TMPDIR/ephemeral"\nrmdir "$TMPDIR/ephemeral"\n' > "$BASE/clean.sh"
printf '#!/usr/bin/env bash\nmkdir -p "$TMPDIR/leaked"\n' > "$BASE/leak.sh"
printf '#!/usr/bin/env bash\nexit 7\n' > "$BASE/fail.sh"
printf '#!/usr/bin/env bash\nmkdir -p "$TMPDIR/leaked"\nexit 7\n' > "$BASE/fail-leak.sh"

out="$(TMPDIR="$BASE/outer" bash "$SUT" bash "$BASE/clean.sh" 2>&1)"; rc=$?
check test "$rc" -eq 0
check outer_empty

out="$(TMPDIR="$BASE/outer" bash "$SUT" bash "$BASE/leak.sh" 2>&1)"; rc=$?
check test "$rc" -eq 1
check bash -c 'case "$1" in *"dejó 1 entrada"*) exit 0;; *) exit 1;; esac' _ "$out"
check outer_empty

TMPDIR="$BASE/outer" bash "$SUT" bash "$BASE/fail.sh" >/dev/null 2>&1; rc=$?
check test "$rc" -eq 7
TMPDIR="$BASE/outer" bash "$SUT" bash "$BASE/fail-leak.sh" >/dev/null 2>&1; rc=$?
check test "$rc" -eq 7
check outer_empty

# Compiling mutant: a cleanup assertion that tolerates 999 entries must let the leaky child pass;
# observing that false green proves the real threshold is load-bearing.
mutant="$BASE/with-clean-tmp-mutant.sh"
sed 's/\[ "$left" -gt 0 \]/[ "$left" -gt 999 ]/' "$SUT" > "$mutant"
bash -n "$mutant" || exit 2
grep -Fq '"$left" -gt 999' "$mutant" || exit 2
TMPDIR="$BASE/outer" bash "$mutant" bash "$BASE/leak.sh" >/dev/null 2>&1; rc=$?
check test "$rc" -eq 0
check outer_empty

printf 'test-with-clean-tmp: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
