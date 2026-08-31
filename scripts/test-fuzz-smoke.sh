#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Mutation witness for the resource half of fuzz:smoke's "bounded" promise.
# The fixture replaces `go` with a recorder: this checks the command the gate
# actually executes, not prose or the line it prints. The second row removes the
# worker bound from a copy and proves that the witness rejects that mutant.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# /tmp is noexec in the development and CI containers. A fake executable placed
# there is skipped by PATH lookup and the fixture silently reaches the real Go
# binary, so the fixture belongs beside the worktree on its executable mount.
TMPROOT="$(mktemp -d "$ROOT/.fuzz-smoke-test.XXXXXX")"
trap 'rm -rf -- "$TMPROOT"' EXIT

pass=0
fail=0

probe() {
	local script=$1
	local fixture=$2
	local capture="$fixture/go.args"
	local output="$fixture/output.log"
	local run_rc=0

	mkdir -p "$fixture/scripts" "$fixture/bin" "$fixture/core/probe"
	for tree in cmd modules connectors sdk operator clients terraform-provider-olivares cloud; do
		mkdir -p "$fixture/$tree"
	done
	cp "$script" "$fixture/scripts/fuzz-smoke.sh"
	printf '%s\n' \
		'package probe' \
		'' \
		'import "testing"' \
		'' \
		'func FuzzBounded(f *testing.F) { f.Fuzz(func(*testing.T, []byte) {}) }' \
		>"$fixture/core/probe/bounded_fuzz_test.go"
	cat >"$fixture/bin/go" <<'EOF'
#!/usr/bin/env bash
set -eu
{
	printf 'GOMAXPROCS=%s\n' "${GOMAXPROCS-<unset>}"
	printf 'ARG=%s\n' "$@"
} >>"$FUZZ_CAPTURE"
EOF
	chmod +x "$fixture/bin/go"

	(
		unset GOMAXPROCS
		cd "$fixture"
		PATH="$fixture/bin:$PATH" \
			FUZZ_CAPTURE="$capture" \
			OLIVARES_FUZZTIME=1ms \
			bash scripts/fuzz-smoke.sh >"$output" 2>&1
	) || run_rc=$?
	if [ "$run_rc" -ne 0 ]; then
		sed 's/^/        /' "$output" >&2
		return "$run_rc"
	fi
	if [ ! -f "$capture" ]; then
		printf '        fuzz smoke did not invoke go\n' >&2
		return 1
	fi

	[ "$(grep -c '^GOMAXPROCS=' "$capture")" -eq 1 ] || return 1
	grep -Fxq 'GOMAXPROCS=2' "$capture" || return 1
	grep -Fxq 'ARG=-parallel=1' "$capture" || return 1
	[ "$(grep -c '^ARG=\.$' "$capture")" -eq 1 ] || return 1
}

if probe "$ROOT/scripts/fuzz-smoke.sh" "$TMPROOT/witness"; then
	printf 'ok    fuzz smoke executes one worker with GOMAXPROCS=2\n'
	pass=$((pass + 1))
else
	printf 'FAIL  fuzz smoke did not execute the promised resource bounds\n'
	fail=$((fail + 1))
fi

mutant="$TMPROOT/fuzz-smoke-no-parallel.sh"
sed 's/ -parallel="[$][{]FUZZ_PARALLEL[}]"//g' "$ROOT/scripts/fuzz-smoke.sh" >"$mutant"
chmod +x "$mutant"
if probe "$mutant" "$TMPROOT/mutant"; then
	printf 'FAIL  mutant without -parallel=1 survived the witness\n'
	fail=$((fail + 1))
else
	printf 'ok    mutant without -parallel=1 is rejected\n'
	pass=$((pass + 1))
fi

printf 'fuzz-smoke bounds selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
