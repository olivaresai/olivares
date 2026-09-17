#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-hot-race-partition.sh — the controls for the hot-manifest partition, which
# lives in scripts/race-hot-tests.sh.
#
# It never starts a `go test`. Assertions cover the SELECTION — which entries exist, who owns
# each one, what is refused — and the ROUTING that carries it: the recipe, and the exact
# parsed job and steps of the CI matrix. That is what makes it cheap enough to be a fast lint.
#
# The fixtures that need a mutated inventory or a mutated census get one in a temporary
# tree; the real manifest and the real census are never written.
set -euo pipefail

# Each fixture owns its selector state. The CI job exports a complete selector
# pair; inheriting it turns the baseline into one shard and fills the missing
# half in the refusal cases. This process-local reset cannot change the parent
# job's selectors used by the later build and race steps.
unset OLIVARES_HOT_RACE_PARTITION OLIVARES_HOT_RACE_PARTITIONS

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MANIFEST="${ROOT}/scripts/race-hot-tests.sh"
HELPER="${MANIFEST}"
COSTS="${ROOT}/scripts/hot-race-costs.tsv"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/hot-race-partition-selftest.XXXXXX")"
trap 'rm -rf "${WORK}"' EXIT

PASS=0
FAIL=0

ok() {
	PASS=$((PASS + 1))
	printf 'ok   %s\n' "$1"
}
no() {
	FAIL=$((FAIL + 1))
	printf 'FAIL %s\n' "$1" >&2
	if [ -n "${2:-}" ]; then printf '     %s\n' "$2" >&2; fi
}

# run <expected-exit> <label> -- <command...>
run_rc() {
	local want="$1" label="$2" rc=0
	shift 3
	"$@" >"${WORK}/out" 2>"${WORK}/err" || rc=$?
	if [ "${rc}" = "${want}" ]; then
		ok "${label} (exit ${rc})"
	else
		no "${label}: expected exit ${want}, got ${rc}" "$(tail -2 "${WORK}/err")"
	fi
}

# Exercise the actual consumer before partition-only checks. A helper can emit the
# correct selection yet use shell grammar the mandatory PostgreSQL reader cannot
# verify. Keep that composition failure in this helper's own cheap feedback loop.
if ( cd "${ROOT}" && go run ./cmd/olivares/tools/checkpgwiring \
	-workflow .github/workflows/mainline-ci.yml -taskfile Taskfile.yml ) \
	>"${WORK}/premise.out" 2>"${WORK}/premise.err"; then
	ok "the actual PostgreSQL reader verifies the hot helper's non-executing premise"
else
	no "the actual PostgreSQL reader rejects the hot helper" "$(tail -2 "${WORK}/premise.err")"
fi

# ── 1. THE INVENTORY IS A TRUE DISJOINT UNION ────────────────────────────────────────────
# The property that matters most: four partitions, every entry owned exactly ONCE. Not
# "roughly balanced" and not "none missing" — both directions, checked against the live
# manifest rather than against a list kept here, which would only prove the list.
bash "${MANIFEST}" --names 2>/dev/null | LC_ALL=C sort >"${WORK}/inventory"
INV_N="$(grep -c . <"${WORK}/inventory" || true)"
if [ "${INV_N}" -gt 0 ]; then
	ok "the manifest declares ${INV_N} hot entry point(s)"
else
	no "the manifest declared no entry point at all"
fi

: >"${WORK}/union"
i=1
while [ "${i}" -le 4 ]; do
	OLIVARES_HOT_RACE_PARTITION="${i}" OLIVARES_HOT_RACE_PARTITIONS=4 \
		bash "${HELPER}" --names 2>/dev/null >>"${WORK}/union"
	i=$((i + 1))
done
LC_ALL=C sort "${WORK}/union" -o "${WORK}/union"
if cmp -s "${WORK}/inventory" "${WORK}/union"; then
	ok "the 4 partitions are a DISJOINT UNION of the inventory: ${INV_N} entries, each owned exactly once"
else
	no "the 4 partitions are NOT a partition of the inventory" "$(diff "${WORK}/inventory" "${WORK}/union" | head -5)"
fi

# And the helper's own union control agrees, for 2, 3, 4 and 5 partitions. A rule that only
# holds at the count CI happens to use is a rule that breaks the first time CI changes it.
for n in 2 3 4 5; do
	run_rc 0 "--census ${n} passes its own union/empty/stale controls" -- bash "${HELPER}" --census "${n}"
done

# ── 2. POSITIVE OWNERSHIP, INCLUDING A TEST THE CENSUS DOES NOT NAME ──────────────────────
# Negative controls ("nothing is lost") pass on an empty inventory. This asserts that a
# SPECIFIC entry lands in exactly one specific partition, and that an entry with NO cost row
# is still owned — the rule that keeps a newly added hot test from falling through.
# The producer is COMPLETED first and its status read, then the captured output is searched.
# `producer | grep -q` fuses the two questions into one number and answers both wrong: a
# MATCH kills the producer with SIGPIPE and returns 141, which the `if` reads as "not owned",
# and a producer that FAILED returns non-zero too, which the `if` reads the same way. Owning
# nothing and being unable to ask are not the same answer, so they get separate ones.
owner_of() { # <name> -> OWNER
	local want="$1" k=1 rc
	OWNER=""
	while [ "${k}" -le 4 ]; do
		rc=0
		OLIVARES_HOT_RACE_PARTITION="${k}" OLIVARES_HOT_RACE_PARTITIONS=4 \
			bash "${HELPER}" --names >"${WORK}/names.${k}" 2>/dev/null || rc=$?
		if [ "${rc}" != 0 ]; then
			no "partition ${k} of 4 could not list its names (exit ${rc})" "a failed producer is not a membership answer"
		elif grep -qxF -- "${want}" "${WORK}/names.${k}"; then
			OWNER="${OWNER}${k} "
		fi
		k=$((k + 1))
	done
}
OWNER=""
owner_of "TestOrchCadencePumpRunOncePassesAllBusinessTenants"
case "${OWNER}" in
"" ) no "the entry the CI panic named is owned by NO partition" ;;
*" "*" " ) no "that entry is owned by more than one partition: ${OWNER}" ;;
* ) ok "TestOrchCadencePumpRunOncePassesAllBusinessTenants is owned by exactly one partition (${OWNER% })" ;;
esac

# The uncosted-entry case, on a FIXTURE TREE so the real census is untouched: `scripts/` is a
# copy (that is what the fixture mutates) and `cmd` is a symlink to the real one, because the
# manifest globs real test files and a tree without them would make every case below fail as a
# discovery error instead of as the thing it means to test. That distinction cost three red
# fixtures the first time this battery ran, and the helper was right each time.
mkdir -p "${WORK}/tree"
cp -R "${ROOT}/scripts" "${WORK}/tree/scripts"
ln -s "${ROOT}/cmd" "${WORK}/tree/cmd"
FIXTURE="${WORK}/tree/scripts/race-hot-tests.sh"
FIXTURE_COSTS="${WORK}/tree/scripts/hot-race-costs.tsv"
# The fixture tree must agree with the real one before anything is mutated, or a later red
# would not tell a fixture defect from a helper defect.
run_rc 0 "the unmutated fixture tree reproduces the real verdict" -- bash "${FIXTURE}" --census 4
UNCOSTED="TestProxyAuthorizeHappyPath"
grep -vxF -- "$(grep -F "${UNCOSTED}	" "${COSTS}")" "${COSTS}" >"${FIXTURE_COSTS}"
if grep -qF "${UNCOSTED}	" "${FIXTURE_COSTS}"; then
	no "the fixture failed to remove the cost row for ${UNCOSTED}"
else
	found=0
	unasked=""
	k=1
	while [ "${k}" -le 4 ]; do
		rc=0
		OLIVARES_HOT_RACE_PARTITION="${k}" OLIVARES_HOT_RACE_PARTITIONS=4 \
			bash "${FIXTURE}" --names >"${WORK}/fixture-names.${k}" 2>/dev/null || rc=$?
		if [ "${rc}" != 0 ]; then
			unasked="${unasked}${k}(exit ${rc}) "
		elif grep -qxF -- "${UNCOSTED}" "${WORK}/fixture-names.${k}"; then
			found=$((found + 1))
		fi
		k=$((k + 1))
	done
	if [ -n "${unasked}" ]; then
		no "the mutated fixture could not list names for partition(s) ${unasked% }" "a failed producer would otherwise count as \"does not own it\""
	elif [ "${found}" = 1 ]; then
		ok "an entry the census does not name is still owned, exactly once, at the default cost"
	else
		no "an uncosted entry was owned by ${found} partition(s), not 1"
	fi
fi

# ── 3. THE COMPLETE SELECTION IS UNCHANGED ───────────────────────────────────────────────
# The local leg must be what it always was. Not "equivalent": the SAME BYTES, because the
# helper takes the regex from the manifest's unchanged no-argument code path.
bash "${MANIFEST}" 2>/dev/null >"${WORK}/whole-manifest"
bash "${HELPER}" 2>/dev/null >"${WORK}/whole-helper"
if cmp -s "${WORK}/whole-manifest" "${WORK}/whole-helper"; then
	ok "with NO selector the regex is byte-identical across two invocations (the local leg is unchanged)"
else
	no "the unpartitioned selection CHANGED" "$(diff "${WORK}/whole-manifest" "${WORK}/whole-helper" | head -3)"
fi

# A partition's regex must be anchored at BOTH ends: this manifest has names that are
# prefixes of others, and Go matches an unanchored -run as a substring.
OLIVARES_HOT_RACE_PARTITION=1 OLIVARES_HOT_RACE_PARTITIONS=4 \
	bash "${HELPER}" 2>/dev/null >"${WORK}/part-regex"
if grep -q '^\^(' "${WORK}/part-regex" && grep -q ')\$$' "${WORK}/part-regex"; then
	ok "a partition's -run regex is anchored at both ends"
else
	no "a partition's -run regex is NOT anchored at both ends" "$(cat "${WORK}/part-regex")"
fi

# ── 4. THE SELECTORS FAIL CLOSED ─────────────────────────────────────────────────────────
# Every one of these must REFUSE. A selector that cannot be read must never degrade to
# running everything (a partition silently doing four partitions' work) and never to running
# nothing (a green that raced nothing at all).
refuse() { # <label> <index> <count>
	local rc=0
	OLIVARES_HOT_RACE_PARTITION="$2" OLIVARES_HOT_RACE_PARTITIONS="$3" \
		bash "${HELPER}" --names >"${WORK}/out" 2>"${WORK}/err" || rc=$?
	if [ "${rc}" = 2 ]; then
		ok "refuses $1 (exit 2)"
	elif [ "${rc}" = 0 ]; then
		no "ACCEPTED $1 and selected $(grep -c . <"${WORK}/out" || true) entries"
	else
		no "refuses $1 but with exit ${rc}, not 2" "$(tail -1 "${WORK}/err")"
	fi
}
refuse "an out-of-range index (5 of 4)" 5 4
refuse "a zero index" 0 4
refuse "a zero count" 1 0
refuse "a non-numeric index" x 4
refuse "a negative index" -1 4
refuse "a leading-zero index" 01 4
refuse "an index with a trailing space" "1 " 4
refuse "an oversized count" 1 1234567890
refuse "an empty-string index with a count set" "" 4

# Half a pair, both ways. One variable must be absent, which `refuse`
# cannot express because it always sets both.
rc=0
OLIVARES_HOT_RACE_PARTITION=1 bash "${HELPER}" --names >/dev/null 2>"${WORK}/err" || rc=$?
[ "${rc}" = 2 ] && ok "refuses half a pair (index without count)" || no "half a pair (index only) exited ${rc}, not 2"
rc=0
OLIVARES_HOT_RACE_PARTITIONS=4 bash "${HELPER}" --names >/dev/null 2>"${WORK}/err" || rc=$?
[ "${rc}" = 2 ] && ok "refuses half a pair (count without index)" || no "half a pair (count only) exited ${rc}, not 2"

# Neither set is the COMPLETE run, not a refusal: that is the local invocation.
run_rc 0 "no selector at all selects the complete manifest" -- bash "${HELPER}" --names
if [ "$(grep -c . <"${WORK}/out" || true)" = "${INV_N}" ]; then
	ok "no selector selects all ${INV_N} entries"
else
	no "no selector selected $(grep -c . <"${WORK}/out" || true) of ${INV_N} entries"
fi

# An unknown mode is refused rather than treated as an argv to run.
run_rc 2 "refuses an unknown argument, which it used to ignore" -- bash "${MANIFEST}" --regexp
run_rc 2 "refuses --census without a count" -- bash "${MANIFEST}" --census

# More partitions than entries must FAIL (1), not silently produce an empty selection.
run_rc 1 "refuses more partitions than entry points" -- bash "${HELPER}" --census 100000

# ── 5. THE CENSUS PARSER FAILS CLOSED ────────────────────────────────────────────────────
# Each fixture mutates ONLY the census, in the copied tree.
census_case() { # <label> <expected-exit> <census-content>
	local rc=0
	printf '%s\n' "$3" >"${FIXTURE_COSTS}"
	OLIVARES_HOT_RACE_PARTITION=1 OLIVARES_HOT_RACE_PARTITIONS=4 \
		bash "${FIXTURE}" --names >/dev/null 2>"${WORK}/err" || rc=$?
	if [ "${rc}" = "$2" ]; then
		ok "census: $1 (exit ${rc})"
	else
		no "census: $1 expected exit $2, got ${rc}" "$(tail -1 "${WORK}/err")"
	fi
}
census_case "a row with no TAB is malformed" 2 "TestProxyAuthorizeHappyPath 4200"
census_case "a row with three fields is malformed" 2 "TestProxyAuthorizeHappyPath	42	7"
census_case "a non-numeric cost is malformed" 2 "TestProxyAuthorizeHappyPath	fast"
census_case "a zero cost is refused" 2 "TestProxyAuthorizeHappyPath	0"
census_case "a negative cost is refused" 2 "TestProxyAuthorizeHappyPath	-5"
census_case "a name that is not an entry point is refused" 2 "BenchmarkProxyAuthorize	10"
census_case "a duplicate name is a property failure" 1 "TestProxyAuthorizeHappyPath	10
TestProxyAuthorizeHappyPath	20"
census_case "an entry the live manifest no longer has is STALE" 1 "TestProxyAuthorizeHappyPath	10
TestThisTestWasDeletedLastWeek	20"
census_case "an empty census is refused" 2 "# only a comment"
rm -f "${FIXTURE_COSTS}"
census_rc=0
OLIVARES_HOT_RACE_PARTITION=1 OLIVARES_HOT_RACE_PARTITIONS=4 \
	bash "${FIXTURE}" --names >/dev/null 2>&1 || census_rc=$?
[ "${census_rc}" = 2 ] && ok "census: a MISSING census file is refused (exit 2)" || no "a missing census exited ${census_rc}, not 2"

# The real census must itself be clean — this is the control that catches a stale row the day
# a hot test is renamed or removed, which is the whole reason the stale rule exists.
run_rc 0 "the SHIPPED census is complete and not stale" -- bash "${HELPER}" --census 4

# ── 6. THE MANIFEST'S OWN FAIL-CLOSED DISCOVERY STILL HOLDS ──────────────────────────────
# The partition rides on top of the manifest's discovery, so the manifest's refusals have to
# survive the change: a glob that resolves no file must still be a loud exit 1, never an
# empty selection that a partition would then divide into nothing.
mkdir -p "${WORK}/emptytree/scripts" "${WORK}/emptytree/cmd/olivares"
cp "${MANIFEST}" "${WORK}/emptytree/scripts/race-hot-tests.sh"
cp "${COSTS}" "${WORK}/emptytree/scripts/hot-race-costs.tsv"
rc=0
bash "${WORK}/emptytree/scripts/race-hot-tests.sh" >/dev/null 2>"${WORK}/err" || rc=$?
if [ "${rc}" = 1 ] && grep -q "resolves NO files" "${WORK}/err"; then
	ok "a manifest glob that resolves no file is still a loud exit 1"
else
	no "an empty manifest tree exited ${rc} without naming the glob" "$(tail -1 "${WORK}/err")"
fi
rc=0
OLIVARES_HOT_RACE_PARTITION=1 OLIVARES_HOT_RACE_PARTITIONS=4 \
	bash "${WORK}/emptytree/scripts/race-hot-tests.sh" >/dev/null 2>"${WORK}/err" || rc=$?
if [ "${rc}" = 1 ]; then
	ok "and it still refuses BEFORE partitioning, so no partition divides an empty selection"
else
	no "with selectors set, an empty manifest tree exited ${rc}, not 1"
fi

# ── 7. WHAT THE GO TOOL IS ACTUALLY HANDED ───────────────────────────────────────────────
# The modes above answer about the SELECTION. This answers about the RECIPE, because a
# selection that is right and a recipe that does not use it is a gate that passes its own
# controls and races the wrong tests. It reads the Taskfile entry rather than re-stating it.
RECIPE="$(grep -F 'RUN_REGEX="$(bash scripts/race-hot-tests.sh)"' "${ROOT}/Taskfile.yml" || true)"
if [ -n "${RECIPE}" ]; then
	ok "the recipe still takes its -run from this script's no-argument output"
else
	no "the test:race-hot:hot recipe no longer calls scripts/race-hot-tests.sh for its -run regex"
fi
for flag in -race -count=1 "-timeout 15m"; do
	case "${RECIPE}" in
	*"${flag}"*) ok "the recipe still carries ${flag}" ;;
	*) no "the recipe LOST ${flag}" "${RECIPE}" ;;
	esac
done
case "${RECIPE}" in
*'-run "$0" .'*) ok "the recipe still passes the regex as an anchored -run over the root package" ;;
*) no "the recipe no longer passes the regex to -run" "${RECIPE}" ;;
esac

# ── 8. THE CI ROUTING, READ FROM THE PARSED JOB AND STEP ─────────────────────────────────
# These assert on the EXACT job and step, not on the file. A whole-file `grep -q` for
# `if: always()` passes while the census step has none, because four other jobs and several
# comments carry that string; three mutations survived the first version of this battery for
# exactly that reason. The facts below come from PyYAML walking mainline-ci.yml, keyed by job
# id and step id, so a property removed from THIS step cannot be satisfied by another job.
WF="${ROOT}/.github/workflows/mainline-ci.yml"
python3 "${ROOT}/scripts/lib/hot-race-ci-facts.py" "${WF}" >"${WORK}/facts" 2>"${WORK}/facts.err"
if [ -s "${WORK}/facts" ]; then
	ok "the CI workflow parses and the race-hot-manifest job is present"
else
	no "could not parse the race-hot-manifest job out of the workflow" "$(head -2 "${WORK}/facts.err")"
fi

fact() { # <key> -> FACT
	FACT="$(grep -m1 "^$1=" "${WORK}/facts" | cut -d= -f2- || true)"
}
want_fact() { # <label> <key> <expected>
	fact "$2"
	if [ "${FACT}" = "$3" ]; then
		ok "$1"
	else
		no "$1" "${2}: expected '$3', parsed '${FACT}'"
	fi
}
contains_fact() { # <label> <key> <needle>
	fact "$2"
	case "${FACT}" in
	*"$3"*) ok "$1" ;;
	*) no "$1" "${2}: '${3}' not in parsed '${FACT}'" ;;
	esac
}
lacks_fact() { # <label> <key> <needle>
	fact "$2"
	case "${FACT}" in
	*"$3"*) no "$1" "${2}: parsed '${FACT}' still contains '${3}'" ;;
	*) ok "$1" ;;
	esac
}

# The selector pair, from the matrix and from strategy.job-total — a hand-written count that
# drifted from the matrix length would leave entries unowned.
want_fact "the job sets the partition index from the matrix" job.env.OLIVARES_HOT_RACE_PARTITION '${{ matrix.partition }}'
want_fact "the job sets the partition count from strategy.job-total" job.env.OLIVARES_HOT_RACE_PARTITIONS '${{ strategy.job-total }}'
want_fact "the matrix declares four partitions" job.matrix.partition "1 2 3 4"
# M7: a cancelled sibling reports nothing about the entries it was racing.
want_fact "fail-fast is false on THIS job's matrix" job.strategy.fail-fast "False"

# The partition control must run BEFORE the prebuild, and both later steps must require it.
want_fact "a partition-control step runs the battery" step.partition-control.run "task lint:hot-race-partition"
want_fact "the partition control precedes the prebuild" order.partition-control-before-prebuild "yes"
contains_fact "the prebuild requires the partition control" step.race-hot-manifest-build.if "steps.partition-control.outcome == 'success'"
contains_fact "the race run requires the partition control" step.race-hot-manifest.if "steps.partition-control.outcome == 'success'"

# The run step keeps the ratified command and retains its log.
contains_fact "the race run invokes the canonical task" step.race-hot-manifest.run "task test:race-hot:hot"
contains_fact "the race run tees a per-partition log" step.race-hot-manifest.run 'ci-fail-race-hot-manifest-${{ matrix.partition }}.log'
lacks_fact "the race run does not delete its own log" step.race-hot-manifest.run "rm "

# M5/M6: the census must run when the race FAILED, and must never decide the job.
want_fact "the headroom census runs on always()" step.census.if "always()"
want_fact "the headroom census is continue-on-error" step.census.continue-on-error "True"
contains_fact "the headroom census reads THIS job's log" step.census.run "--raw race-hot-manifest"
# The neighbouring legs end their census with `&& rm`, which keeps the log only when the
# checker answers nonzero: a failure BELOW the warning threshold has its log removed by the
# success of the step meant to preserve it.
lacks_fact "the census does not delete the log it just measured" step.census.run "rm "

# A consumer must receive the log on both outcomes.
want_fact "a retention step runs on always()" step.retain.if "always()"
contains_fact "retention uses the pinned upload-artifact action" step.retain.uses "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a"
contains_fact "retention names this partition's log" step.retain.path 'ci-fail-race-hot-manifest-${{ matrix.partition }}.log'

# The aggregate must fold this job in, in all four bad states.
contains_fact "race-hot needs the partitioned job" aggregate.needs "race-hot-manifest"
contains_fact "race-hot binds the partitioned job's result" aggregate.env "RESULT_HOT_MANIFEST"
contains_fact "race-hot folds it into the verdict loop" aggregate.run '"race-hot-manifest=$RESULT_HOT_MANIFEST"'
contains_fact "an absent job id is named and red" aggregate.run "reported NO result at all"

# ── 9. A BELOW-THRESHOLD FAILURE KEEPS ITS LOG ───────────────────────────────────────────
# Not an assertion about the YAML: the parsed census command is RUN against two synthetic
# logs. The one that matters is the failure the old pattern would have deleted — a red run
# well under the warning threshold, where the checker answers 0 and a trailing `&& rm` fires.
fact step.census.run
CENSUS_CMD="${FACT}"
survives() { # <label> <log-body>
	local logf="${WORK}/ci-fail-race-hot-manifest-9.log" rc=0
	printf '%s\n' "$2" >"${logf}"
	( cd "${ROOT}" && RUNNER_TEMP="${WORK}" bash -c "${CENSUS_CMD//\$\{\{ matrix.partition \}\}/9}" ) >/dev/null 2>&1 || rc=$?
	if [ -s "${logf}" ]; then
		ok "$1 survives the census (checker exit ${rc})"
	else
		no "$1 was DELETED by the census (checker exit ${rc})"
	fi
	rm -f "${logf}"
}
survives "a FAILED run far below the warning threshold" 'task: [test:race-hot:hot] go test -race -count=1 -timeout 15m -run x .
==================
WARNING: DATA RACE
==================
--- FAIL: TestSomething (12.00s)
FAIL	github.com/olivaresai/olivares/cmd/olivares	61.004s
FAIL'
survives "a run killed by the 15m cap" 'task: [test:race-hot:hot] go test -race -count=1 -timeout 15m -run x .
panic: test timed out after 15m0s
FAIL	github.com/olivaresai/olivares/cmd/olivares	900.012s
FAIL'

printf '\ntest-hot-race-partition: %s passed, %s failed\n' "${PASS}" "${FAIL}"
[ "${FAIL}" -eq 0 ]
