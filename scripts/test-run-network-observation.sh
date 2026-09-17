#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Mutation battery for run-network-observation.sh. No network is used.
set -uo pipefail

ROOT="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)"
RUN="$ROOT/scripts/run-network-observation.sh"
EXEC_LIB="$ROOT/scripts/lib/exec-workdir.sh"
# shellcheck source=/dev/null
. "$EXEC_LIB" || { echo "test-run-network-observation: cannot source $EXEC_LIB" >&2; exit 2; }

work="$(olivares_pick_exec_workdir network-observation-test)" || {
	echo "test-run-network-observation: no executable scratch" >&2
	exit 2
}
trap 'rm -rf -- "$work"' EXIT

make_probe() {
	local name="$1" rc="$2"
	printf '#!/bin/sh\necho "probe payload rc=%s"\nexit %s\n' "$rc" "$rc" >"$work/$name"
	chmod +x "$work/$name"
}
make_probe clean 0
make_probe finding 1
make_probe blind 2
make_probe broken 7

fails=0
run_case() {
	local name="$1" want="$2"
	shift 2
	case_out=$("$@" 2>&1); case_rc=$?
	if [ "$case_rc" -ne "$want" ]; then
		echo "test-run-network-observation: $name expected rc=$want, got $case_rc: $case_out" >&2
		fails=$((fails + 1))
	fi
}

run_case clean 0 bash "$RUN" clean-subject -- "$work/clean"
case "$case_out" in *"probe payload rc=0"*) ;; *) echo "test-run-network-observation: clean output lost" >&2; fails=$((fails + 1)) ;; esac

run_case finding 1 bash "$RUN" finding-subject -- "$work/finding"
case "$case_out" in *"ADVISORY"*) echo "test-run-network-observation: finding was mislabeled advisory" >&2; fails=$((fails + 1)) ;; esac

run_case blind 0 env GITHUB_ACTIONS=true bash "$RUN" blind-subject -- "$work/blind"
case "$case_out" in *"ADVISORY"*"s medidos"*"::warning"*) ;; *) echo "test-run-network-observation: blind path lacks measured advisory: $case_out" >&2; fails=$((fails + 1)) ;; esac

# A-4 (PUBEXPORT): an unverified observation must be readable in the job summary,
# never silent. rc=2 stays advisory/green; the summary file is the notice.
summary="$work/step-summary.md"
: >"$summary"
run_case blind-summary 0 env GITHUB_ACTIONS=true GITHUB_STEP_SUMMARY="$summary" bash "$RUN" blind-subject -- "$work/blind"
if ! grep -q 'ADVISORY' "$summary" || ! grep -q 'blind-subject' "$summary"; then
	echo "test-run-network-observation: GITHUB_STEP_SUMMARY lacks the advisory notice: $(cat "$summary")" >&2
	fails=$((fails + 1))
fi
run_case finding-no-summary 1 env GITHUB_STEP_SUMMARY="$summary" bash "$RUN" finding-subject -- "$work/finding"
if grep -q 'finding-subject' "$summary"; then
	echo "test-run-network-observation: a blocking finding was written to the job summary as advisory" >&2
	fails=$((fails + 1))
fi

run_case unexpected 7 bash "$RUN" broken-subject -- "$work/broken"

run_case census-finding 0 bash "$RUN" --all-advisory census-subject -- "$work/finding"
case "$case_out" in *"ADVISORY"*"rc=1"*"s medidos"*) ;; *) echo "test-run-network-observation: all-advisory census was not measured: $case_out" >&2; fails=$((fails + 1)) ;; esac

# MUTANT: remove the only rc=2 arm. The blind probe must stop being advisory/green.
sed '/^2)$/,/^[[:space:]]*;;$/d' "$RUN" >"$work/mutant"
chmod +x "$work/mutant"
out=$(bash "$work/mutant" blind-mutant -- "$work/blind" 2>&1); rc=$?
if [ "$rc" -eq 0 ]; then
	echo "test-run-network-observation: MUTANT removing rc=2 advisory survived: $out" >&2
	fails=$((fails + 1))
fi

# MUTANT: ignore the explicit all-advisory mode. A census finding must become blocking again.
sed '0,/mode="all-advisory"/s//mode="unavailable-only"/' "$RUN" >"$work/all-mutant"
chmod +x "$work/all-mutant"
out=$(bash "$work/all-mutant" --all-advisory census-mutant -- "$work/finding" 2>&1); rc=$?
if [ "$rc" -eq 0 ]; then
	echo "test-run-network-observation: MUTANT disabling all-advisory mode survived: $out" >&2
	fails=$((fails + 1))
fi

if [ "$fails" -ne 0 ]; then
	echo "test-run-network-observation: $fails failure(s)" >&2
	exit 1
fi
echo "test-run-network-observation: 9/9 (clean · finding · blind · summary · finding-no-summary · unexpected · census · two mutants)"
