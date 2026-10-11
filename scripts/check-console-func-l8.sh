#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Three-state executable oracle for the L8 console contracts.
#   0: focused assertions ran and passed (FUNCIONA)
#   1: focused assertions ran and failed (ROTO)
#   2: assertions could not be run or identified (NO PUDE MIRAR)
set -u -o pipefail

NAME='console-func-l8'
cannot() {
	printf '%s: COULD NOT CHECK — %s\n' "$NAME" "$*" >&2
	exit 2
}

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || cannot "cannot load $_olivares_git_env (Git environment isolation)"
unset _olivares_git_env

for command_name in git pnpm go grep mktemp mkdir; do
	command -v "$command_name" >/dev/null 2>&1 || cannot "$command_name is unavailable"
done

ROOT=${OLIVARES_L8_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null)}
[ -n "$ROOT" ] || cannot 'cannot resolve the repository root'
WEB=${OLIVARES_L8_WEB_DIR:-$ROOT/web}
TESTS=(
	src/features/inference-proxy/inference-proxy-view.test.tsx
	src/features/models/gpai.test.tsx
	src/features/models/execute-view.test.tsx
	src/features/sandbox/sandbox.test.tsx
	src/features/sandbox/stream.test.tsx
	src/features/evals/gate-view.test.tsx
	src/features/evals/calibration-view.test.tsx
	src/features/evals/baseline-view.test.tsx
	src/features/evals/ab-view-wire.test.tsx
	src/features/evals/evals.test.tsx
	src/features/model-ops/documents.test.tsx
	src/features/model-ops/owned-models.test.tsx
	src/features/model-ops/admission.test.tsx
)

[ -r "$WEB/package.json" ] || cannot "cannot read $WEB/package.json"
for test_file in "${TESTS[@]}"; do
	[ -r "$WEB/$test_file" ] || cannot "cannot read $WEB/$test_file"
done
[ -r "$ROOT/modules/inferenceproxy/stepup_test.go" ] || cannot 'cannot read the AAL3 module test'

TMP=$(mktemp -d "${TMPDIR:-/tmp}/console-func-l8.XXXXXX") || cannot 'mktemp failed'
[ -d "$TMP" ] || cannot 'mktemp did not return a directory'
trap 'rm -rf -- "$TMP"' EXIT
mkdir -p "$TMP/go" || cannot 'cannot create executable temporary Go directory'

(
	cd "$ROOT" || exit 2
	TMPDIR="$TMP/go" NO_COLOR=1 go test ./modules/inferenceproxy \
		-run '^TestGovernanceWritesRequireAAL3$' -count=1
) >"$TMP/go.log" 2>&1
go_rc=$?
cat "$TMP/go.log"
if [ "$go_rc" -ne 0 ]; then
	if grep -Eq '(^|[[:space:]])FAIL([[:space:]]|$)|--- FAIL:' "$TMP/go.log"; then
		printf '%s: FAIL — L8_FOCAL_CONTRACT: a focused functional assertion failed\n' "$NAME" >&2
		exit 1
	fi
	cannot "go test exited rc=$go_rc without an identifiable functional assertion"
fi
grep -Eq '^ok[[:space:]]+github.com/olivaresai/olivares/modules/inferenceproxy' "$TMP/go.log" ||
	cannot 'go test exited 0 without running the inferenceproxy package'

(
	unset FORCE_COLOR
	export NO_COLOR=1
	pnpm -C "$WEB" exec vitest run --reporter=dot "${TESTS[@]}"
) >"$TMP/vitest.log" 2>&1
web_rc=$?
cat "$TMP/vitest.log"

if [ "$web_rc" -ne 0 ]; then
	if grep -Eq 'Failed Tests|[[:space:]]FAIL[[:space:]]' "$TMP/vitest.log"; then
		printf '%s: FAIL — L8_FOCAL_CONTRACT: a focused functional assertion failed\n' "$NAME" >&2
		exit 1
	fi
	cannot "vitest exited rc=$web_rc without an identifiable functional assertion"
fi

grep -Eq '^[[:space:]]*Test Files[[:space:]]+13 passed \(13\)[[:space:]]*$' "$TMP/vitest.log" ||
	cannot 'vitest exited 0 without running the thirteen focused suites'
grep -Eq '^[[:space:]]*Tests[[:space:]]+128 passed \(128\)[[:space:]]*$' "$TMP/vitest.log" ||
	cannot 'vitest exited 0 without running the 128 focused cases'

printf '%s: PASS — AAL3, GPAI, models, sandbox, evals, and documents verified\n' "$NAME"
