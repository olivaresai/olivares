#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for scripts/core-race-partition.sh and for the CI wiring that consumes it.
#
# A. The rule. Every package `go list ./...` discovers inside ./core, except
#    core/internal/store/sqlstore, is owned by exactly one partition. sqlstore is split by
#    entry point across the same four jobs. A package owned by nobody is never raced and
#    all four jobs still go green, which is the failure here that is worse than a red, so
#    the property is checked on the live inventory, on synthetic inventories holding
#    packages this tree does not have, and against mutants of the rule itself: a control
#    only ever seen to pass has never been seen to work. The selector refusals belong here
#    too — a selector the shell cannot evaluate must be refused, not skipped.
#
# B. The wiring. A four-way matrix has failure modes a green run hides: a concurrency group
#    that does not name the matrix value makes the four partitions cancel each other, and a
#    declared count that disagrees with the matrix length silently drops the packages of the
#    missing indexes. Each is mutated into a copy of the workflow, which then has to redden.
#
# C. Progress. A partition must name the test it is running, or a leg killed by a clock
#    reports only that it died. `go test` buffers package output above width 1, so the flag
#    and the width are checked as a pair, on a fixture, in both directions.
#
# D. The job's environment. race-core exports the selector pair for the whole job, so this
#    battery inherits it in every partition. The cases that mean "no selector" share run(),
#    which unsets both; each state the job can hand down is exported against that seam and
#    against a bare child that bypasses it, which must still see the state.
#
# Answers: 0 every case passed · 1 a case failed · 2 the battery could not run its own
# fixtures (never a pass).
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}" || exit 2

SUT="${ROOT}/scripts/core-race-partition.sh"
WF="${ROOT}/.github/workflows/mainline-ci.yml"
TASKFILE="${ROOT}/Taskfile.yml"

[ -r "${SUT}" ] || { printf 'test-core-race-partition: COULD NOT LOOK — %s is unreadable\n' "${SUT}" >&2; exit 2; }
[ -r "${WF}" ] || { printf 'test-core-race-partition: COULD NOT LOOK — %s is unreadable\n' "${WF}" >&2; exit 2; }

_tmp_base="${TMPDIR:-/tmp}"
mkdir -p "${_tmp_base}" || exit 2
TMP="$(mktemp -d "${_tmp_base}/mrp.XXXXXX")" || exit 2
trap 'rm -rf "${TMP}"' EXIT HUP INT TERM

pass=0
fail=0
ok() {
	printf 'ok    %-62s %s\n' "$1" "${2:-}"
	pass=$((pass + 1))
}
bad() {
	printf 'FAIL  %-62s %s\n' "$1" "${2:-}" >&2
	fail=$((fail + 1))
}
blind() {
	printf 'test-core-race-partition: COULD NOT LOOK — %s\n' "$*" >&2
	exit 2
}

# DECLARED CASE COUNT. A run that measures fewer cases than this battery claims to measure
# is a FAILED run, whatever the individual rows said: several cases live inside `if <build
# the fixture>` blocks, and a fixture that cannot be built makes its case CEASE TO EXIST
# while the total still prints green. Raise it deliberately when adding a case.
DECLARED_CASES=67

# run <sut> <args...> — leaves stdout in $TMP/out, stderr in $TMP/err, status in $TMP/rc.
# The SUT runs with NEITHER selector set, whatever this battery inherited. Every case that
# means "no selector" goes through here, and race-core exports the pair for its whole job,
# so without the unset those cases race the partition that hosts them (section D). A case
# that wants a selector sets both halves on a direct `bash` command, as (4), refusal() and
# (24) do.
run() {
	local sut="$1"
	shift
	local rc=0
	(
		unset OLIVARES_CORE_RACE_PARTITION OLIVARES_CORE_RACE_PARTITIONS
		exec bash "${sut}" "$@"
	) >"${TMP}/out" 2>"${TMP}/err" || rc=$?
	printf '%s' "${rc}" >"${TMP}/rc"
}
rc() { cat "${TMP}/rc"; }
outlines() { grep -c . "${TMP}/out" || true; }
errhas() { grep -qF "$1" "${TMP}/err"; }

# mutate copies the script under test, changes ONE thing, and refuses to proceed unless the
# change APPLIED and still parses: a mutation that did not land proves nothing.
mutate() { # mutate <name> <sed-expression>
	local name="$1" expr="$2"
	sed "${expr}" "${SUT}" >"${TMP}/${name}.sh" || return 1
	if cmp -s "${SUT}" "${TMP}/${name}.sh"; then
		bad "MUTATION DID NOT APPLY: ${name}" "the anchor moved; this case proves nothing"
		return 1
	fi
	bash -n "${TMP}/${name}.sh" || {
		bad "MUTATION DID NOT PARSE: ${name}" "the fixture is broken, not the control"
		return 1
	}
	return 0
}

# ── A. THE RULE ──────────────────────────────────────────────────────────────────────────

# (1) The live inventory is discoverable at all. Every case below is about a set, so a
#     battery that cannot read the set is not entitled to report on it.
if ! bash "${SUT}" --inventory >"${TMP}/inv" 2>"${TMP}/inv.err"; then
	blind "the live inventory could not be read: $(head -1 "${TMP}/inv.err")"
fi
INV_N="$(grep -c . "${TMP}/inv" || true)"
[ "${INV_N}" -ge 4 ] || blind "the live inventory has ${INV_N} package(s); this battery needs at least 4 to talk about four partitions"
SQLSTORE_LINE="$(grep '/internal/store/sqlstore$' "${TMP}/inv" || true)"
SQLSTORE_N="$(printf '%s\n' "${SQLSTORE_LINE}" | grep -c . || true)"
[ "${SQLSTORE_N}" = 1 ] || blind "the live inventory must contain exactly one sqlstore package; got ${SQLSTORE_N}"
PKG_N=$((INV_N - 1))
ok "the live ./core inventory is discoverable" "${INV_N} package(s), ${PKG_N} after excluding sqlstore"

# (2) The live four-way partition is a partition.
run "${SUT}" --check 4
if [ "$(rc)" = 0 ] && grep -q 'CLEAN' "${TMP}/out"; then
	ok "the live 4-way partition owns every package exactly once" "$(grep -o 'CLEAN.*' "${TMP}/out" | cut -c1-40)"
else
	bad "the live 4-way partition should be CLEAN" "rc=$(rc) $(head -1 "${TMP}/err")"
fi

# (3) The census accounts for the whole inventory — a SECOND arithmetic over the same
#     placement, because the check's own counting is what case (2) is trusting.
run "${SUT}" --census 4
CENSUS_N=0
while IFS= read -r n; do
	[ -n "${n}" ] || continue
	CENSUS_N=$((CENSUS_N + n))
done < <(grep -o 'partition [0-9]*/4 — [0-9]* package' "${TMP}/out" | grep -o -- '— [0-9]*' | grep -o '[0-9]*')
if [ "${CENSUS_N:-0}" = "${PKG_N}" ]; then
	ok "the census counts sum to the non-sqlstore inventory" "${CENSUS_N} = ${PKG_N}"
else
	bad "the census counts do not sum to the non-sqlstore inventory" "census=${CENSUS_N:-?} packages=${PKG_N}"
fi

# (4) THE UNION, DERIVED A THIRD WAY: the four --packages selections concatenated must equal
#     the inventory exactly, with no duplicate. This does not use --check at all, so a bug
#     inside --check cannot make this agree with it.
: >"${TMP}/union"
i=1
while [ "${i}" -le 4 ]; do
	OLIVARES_CORE_RACE_PARTITION="${i}" OLIVARES_CORE_RACE_PARTITIONS=4 \
		bash "${SUT}" --packages >>"${TMP}/union" 2>/dev/null || blind "partition ${i} of 4 could not be selected"
	i=$((i + 1))
done
LC_ALL=C sort -o "${TMP}/union" "${TMP}/union"
LC_ALL=C sort "${TMP}/inv" >"${TMP}/inv.sorted"
grep -v '/internal/store/sqlstore$' "${TMP}/inv.sorted" >"${TMP}/inv.nosql"
if cmp -s "${TMP}/union" "${TMP}/inv.nosql"; then
	ok "the four selections, concatenated, ARE the non-sqlstore inventory" "$(grep -c . "${TMP}/union") = ${PKG_N}"
else
	bad "the four selections are not the non-sqlstore inventory" "$(diff "${TMP}/inv.nosql" "${TMP}/union" | head -3 | tr '\n' ' ')"
fi
if [ "$(LC_ALL=C sort -u "${TMP}/union" | grep -c . || true)" = "$(grep -c . "${TMP}/union" || true)" ]; then
	ok "no package is owned by two partitions" "no duplicate in the union"
else
	bad "a package is owned twice" "$(LC_ALL=C sort "${TMP}/union" | uniq -d | head -2 | tr '\n' ' ')"
fi
if grep -q '/internal/store/sqlstore$' "${TMP}/union"; then
	bad "sqlstore is still in a package selection" "it would be raced whole and as entry shards"
else
	ok "sqlstore is absent from every partitioned package selection" "entry helper owns it"
fi

# (5) With no selector the selection IS the whole inventory — the property the local gate
#     depends on, and the one an operator loses silently if the default ever changes.
run "${SUT}" --packages
if [ "$(rc)" = 0 ] && cmp -s <(LC_ALL=C sort "${TMP}/out") "${TMP}/inv.sorted"; then
	ok "no selector selects the COMPLETE inventory" "${INV_N} package(s)"
else
	bad "no selector should select everything" "rc=$(rc) got $(outlines) line(s)"
fi
if grep -q '/internal/store/sqlstore$' "${TMP}/out"; then
	ok "no selector still includes sqlstore in the package list" "local ./... is not reduced"
else
	bad "no selector dropped sqlstore from the complete inventory" "the local recipe would lose the package"
fi

# ── synthetic inventories ────────────────────────────────────────────────────────────────
# The live tree can only ever prove the rule for the packages it happens to have today.

# (6) TOMORROW'S PACKAGES. The live inventory plus three names this tree does not contain,
#     none of them in the cost table, all of them under the same module path.
{
	cat "${TMP}/inv"
	printf '%s\n' \
		'github.com/olivaresai/olivares/core/futuretelemetry' \
		'github.com/olivaresai/olivares/core/futuretelemetry/ingest' \
		'github.com/olivaresai/olivares/core/zzz-last-alphabetically'
} >"${TMP}/future"
if bash "${SUT}" --check 4 - <"${TMP}/future" >"${TMP}/out" 2>"${TMP}/err"; then
	ok "a SYNTHETIC future inventory is still a partition" "$(grep -o '[0-9]* package(s), 4' "${TMP}/out")"
else
	bad "the future inventory should partition cleanly" "$(head -1 "${TMP}/err")"
fi
FUT_OWNED=0
i=1
while [ "${i}" -le 4 ]; do
	n="$(bash "${SUT}" --assign "${i}" 4 <"${TMP}/future" 2>/dev/null | grep -c 'futuretelemetry\|zzz-last-alphabetically' || true)"
	FUT_OWNED=$((FUT_OWNED + n))
	i=$((i + 1))
done
if [ "${FUT_OWNED}" = 3 ]; then
	ok "each of the three unknown packages has exactly one owner" "3 of 3"
else
	bad "the unknown packages are not owned exactly once" "owned ${FUT_OWNED} time(s), expected 3"
fi

# (7) The smallest inventory four partitions can have: one package each.
printf 'a\nb\nc\nd\n' >"${TMP}/four"
if bash "${SUT}" --check 4 - <"${TMP}/four" >"${TMP}/out" 2>"${TMP}/err"; then
	ok "four packages over four partitions" "one each"
else
	bad "four over four should be clean" "$(head -1 "${TMP}/err")"
fi

# (8) One partition: it owns everything, which is the degenerate case CI must not special-case.
if bash "${SUT}" --check 1 - <"${TMP}/inv" >"${TMP}/out" 2>"${TMP}/err" &&
	[ "$(bash "${SUT}" --assign 1 1 <"${TMP}/inv" 2>/dev/null | grep -c . || true)" = "${INV_N}" ]; then
	ok "one partition owns the whole inventory" "${INV_N} package(s)"
else
	bad "a single partition should own everything" "$(head -1 "${TMP}/err")"
fi

# (9) Seven partitions over the live inventory: nothing about the rule may assume four.
if bash "${SUT}" --check 7 - <"${TMP}/inv" >"${TMP}/out" 2>"${TMP}/err"; then
	ok "seven partitions over the live inventory" "still a partition"
else
	bad "seven partitions should be clean too" "$(head -1 "${TMP}/err")"
fi

# (10) DETERMINISM. Two independent runs of the same selection are byte-identical; four
#      jobs computing their own partition depend on exactly this.
bash "${SUT}" --assign 2 4 <"${TMP}/future" >"${TMP}/det1" 2>/dev/null || true
bash "${SUT}" --assign 2 4 <"${TMP}/future" >"${TMP}/det2" 2>/dev/null || true
if [ -s "${TMP}/det1" ] && cmp -s "${TMP}/det1" "${TMP}/det2"; then
	ok "the placement is deterministic" "$(grep -c . "${TMP}/det1") package(s), identical twice"
else
	bad "the placement is not deterministic" "or it produced nothing"
fi

# ── refusals: a bad selector must FAIL, never run something ──────────────────────────────
# Every one of these asserts TWO things: a nonzero status, and an EMPTY stdout. A refusal
# that still printed a package list would be handed to `go test` by the recipe.
refusal() { # refusal <label> <expected-rc> <env-index> <env-count>
	local label="$1" want="$2" idx="$3" cnt="$4" rc=0
	OLIVARES_CORE_RACE_PARTITION="${idx}" OLIVARES_CORE_RACE_PARTITIONS="${cnt}" \
		bash "${SUT}" --packages >"${TMP}/out" 2>"${TMP}/err" || rc=$?
	if [ "${rc}" = "${want}" ] && [ "$(outlines)" = 0 ]; then
		ok "${label}" "rc=${rc}, no package printed"
	else
		bad "${label}" "rc=${rc} (wanted ${want}), $(outlines) line(s) on stdout"
	fi
}
refusal "an index with no count is refused" 2 2 ""
refusal "a count with no index is refused" 2 "" 4
refusal "index 0 is refused" 2 0 4
refusal "index 5 of 4 is refused" 2 5 4
refusal "count 0 is refused" 2 1 0
refusal "a non-numeric index is refused" 2 two 4
refusal "a zero-padded index is refused" 2 04 4
refusal "a negative index is refused" 2 -1 4
refusal "an index with a trailing space is refused" 2 "1 " 4

# Oversized selectors. All digits, so the decimal test accepts them, and wider than a signed
# 64-bit integer, so `[ x -gt y ]` prints "integer expression expected" and reads as FALSE:
# without a length bound the range check is skipped rather than enforced. The bound answers
# 2 (an input that could not be read), which is the same class as any other bad selector.
HUGE="99999999999999999999"
refusal "an oversized index is refused" 2 "${HUGE}" 4
refusal "an oversized count is refused" 2 1 "${HUGE}"

# (a) The refusal names the limit, and it is this script that refuses: a run whose stderr
#     carries the shell's own arithmetic complaint got past the bound.
rc=0
OLIVARES_CORE_RACE_PARTITION="${HUGE}" OLIVARES_CORE_RACE_PARTITIONS=4 \
	bash "${SUT}" --packages >"${TMP}/out" 2>"${TMP}/err" || rc=$?
if [ "${rc}" = 2 ] && errhas "digits" && ! errhas "integer expression expected"; then
	ok "the oversized refusal names the digit limit" "rc=2, no shell arithmetic error"
else
	bad "the oversized refusal is not the one that fired" "rc=${rc}: $(head -1 "${TMP}/err")"
fi

# (b) and (c) The same bound guards the inspection modes, which take their count from argv
#     instead of the environment.
run "${SUT}" --check "${HUGE}"
if [ "$(rc)" = 2 ] && errhas "digits"; then
	ok "--check refuses an oversized count" "rc=2"
else
	bad "--check accepted an oversized count" "rc=$(rc)"
fi
rc=0
bash "${SUT}" --assign 1 "${HUGE}" <"${TMP}/inv" >"${TMP}/out" 2>"${TMP}/err" || rc=$?
if [ "${rc}" = 2 ] && errhas "digits"; then
	ok "--assign refuses an oversized count" "rc=2"
else
	bad "--assign accepted an oversized count" "rc=${rc}"
fi
rc=0
bash "${SUT}" --assign "${HUGE}" 4 <"${TMP}/inv" >"${TMP}/out" 2>"${TMP}/err" || rc=$?
if [ "${rc}" = 2 ] && errhas "digits" && [ "$(outlines)" = 0 ]; then
	ok "--assign refuses an oversized index" "rc=2, no package printed"
else
	bad "--assign accepted an oversized index" "rc=${rc}, $(outlines) line(s)"
fi

# (d) CONTROL POSITIVE for the bound, and it exhibits the defect the bound closes. With the
#     limit raised, the same input reaches `[ "${i}" -gt "${n}" ]`, the shell complains on
#     stderr, the test reads as FALSE, and the index passes validation: the selection is
#     then empty and the status is 0 — a green answer that owns no package. The mutant is
#     driven through --assign because a copy outside the repository cannot discover the live
#     inventory, and this case is about the bound, not about discovery.
if mutate nobound 's|^MAX_SELECTOR_DIGITS=9$|MAX_SELECTOR_DIGITS=99|'; then
	rc=0
	bash "${TMP}/nobound.sh" --assign "${HUGE}" 4 <"${TMP}/inv" >"${TMP}/out" 2>"${TMP}/err" || rc=$?
	if [ "${rc}" = 0 ] && [ "$(outlines)" = 0 ] && errhas "integer expression expected"; then
		ok "control positive: without the bound the range test is skipped" "rc=0, 0 package(s)"
	else
		bad "the bound mutant did not change the answer" "rc=${rc}, $(outlines) line(s); the oversized cases prove nothing"
	fi
fi

# (e) More partitions than packages through --assign, which reads its inventory from stdin:
#     the same refusal as --check, reached by the other door.
rc=0
bash "${SUT}" --assign 1 5 <"${TMP}/four" >"${TMP}/out" 2>"${TMP}/err" || rc=$?
if [ "${rc}" = 1 ] && errhas "EMPTY"; then
	ok "--assign refuses 5 partitions over 4 packages" "rc=1"
else
	bad "--assign accepted an empty-partition count" "rc=${rc}"
fi

# (20) More partitions than packages: at least one partition would be EMPTY, and an empty
#      `go test` argv tests the current directory and exits 0.
rc=0
bash "${SUT}" --check 5 - <"${TMP}/four" >"${TMP}/out" 2>"${TMP}/err" || rc=$?
if [ "${rc}" = 1 ] && errhas "EMPTY"; then
	ok "5 partitions over 4 packages is refused" "rc=1, names the empty partition"
else
	bad "5 over 4 should refuse" "rc=${rc}: $(head -1 "${TMP}/err")"
fi

# (21) An empty inventory is COULD NOT LOOK, never "cleanly partitioned nothing".
rc=0
printf '' | bash "${SUT}" --check 4 - >"${TMP}/out" 2>"${TMP}/err" || rc=$?
if [ "${rc}" = 2 ]; then
	ok "an empty inventory is COULD NOT LOOK" "rc=2"
else
	bad "an empty inventory must not pass" "rc=${rc}"
fi

# (22) An unknown mode is refused rather than treated as an argv to run.
run "${SUT}" --definitely-not-a-mode
if [ "$(rc)" = 2 ] && errhas "unknown mode"; then
	ok "an unknown --mode is refused" "rc=2"
else
	bad "an unknown --mode should be refused" "rc=$(rc)"
fi

# ── run mode: what the recipe actually executes ──────────────────────────────────────────
# Unselected commands still accept echo/false unchanged. Selected commands now require
# a supported Go argv. This owned Go fixture lists real packages, declares four synthetic
# entries and records execution argv; it does not pretend to execute product tests.
ARGV_GO="${TMP}/argv-go/go"
mkdir -p "${TMP}/argv-go"
{
	printf '#!/bin/bash\n'
	printf 'if [ "$1" = env ] || [ "$1" = list ]; then exec %q "$@"; fi\n' "$(command -v go)"
	printf 'shift\ncase " $* " in\n*" -list "*) printf "TestAlpha\\nTestBeta\\nTestGamma\\nTestOmega\\n" ;;\n*) printf "%%s\\n" "$*" ;;\nesac\n'
} >"${ARGV_GO}"
chmod +x "${ARGV_GO}"

# (23) With no selector the argv ends in `./...` — byte for byte the pre-partition recipe.
run "${SUT}" echo
if [ "$(rc)" = 0 ] && [ "$(cat "${TMP}/out")" = "./..." ]; then
	ok "run mode with no selector passes ./..." "$(cat "${TMP}/out")"
else
	bad "run mode should pass ./... unpartitioned" "rc=$(rc) got '$(cat "${TMP}/out")'"
fi

# (24) With a selector the argv is `-p 1` plus exactly the packages that partition owns.
rc=0
OLIVARES_CORE_RACE_PARTITION=3 OLIVARES_CORE_RACE_PARTITIONS=4 \
	bash "${SUT}" "${ARGV_GO}" test >"${TMP}/out" 2>"${TMP}/err" || rc=$?
WANT_N="$(OLIVARES_CORE_RACE_PARTITION=3 OLIVARES_CORE_RACE_PARTITIONS=4 bash "${SUT}" --packages 2>/dev/null | grep -c . || true)"
GOT_N="$(tr ' ' '\n' <"${TMP}/out" | grep -c 'github.com/olivaresai/olivares/core' || true)"
if [ "${rc}" = 0 ] && [ "${GOT_N}" = "${WANT_N}" ] && grep -q -- '-p 1 ' "${TMP}/out"; then
	ok "run mode passes -p 1 and exactly its own packages" "${GOT_N} package(s)"
else
	bad "run mode argv is wrong" "rc=${rc} got ${GOT_N} package(s), wanted ${WANT_N}"
fi

# (f) NAMED TEST PROGRESS. A partition must report which test it is running, or a leg that
#     dies on a clock says only that it died. `-v` is how the packages in the argv name
#     their tests; it belongs to the partitioned argv only, so the local gate keeps the
#     exact recipe it had.
rc=0
OLIVARES_CORE_RACE_PARTITION=2 OLIVARES_CORE_RACE_PARTITIONS=4 \
	bash "${SUT}" "${ARGV_GO}" test >"${TMP}/out" 2>"${TMP}/err" || rc=$?
if [ "${rc}" = 0 ] && grep -q -- '-v -p 1 ' "${TMP}/out"; then
	ok "the partitioned argv asks for named test progress" "-v -p 1"
else
	bad "the partitioned argv carries no -v" "rc=${rc}: $(cut -c1-40 "${TMP}/out")"
fi

rc=0
OLIVARES_CORE_RACE_PARTITION=1 OLIVARES_CORE_RACE_PARTITIONS=4 \
	bash "${SUT}" echo >"${TMP}/out" 2>"${TMP}/err" || rc=$?
if [ "${rc}" = 2 ] && errhas 'unsupported partitioned command'; then
	ok "selected run refuses an arbitrary command before either leg" "echo is supported only on the unchanged unselected path"
else
	bad "selected run accepted a command without a Go-test inventory" "rc=${rc}"
fi

# (g) …and the unpartitioned argv does not: `./...` alone, byte for byte the pre-partition
#     recipe, because a developer's full gate was never the leg that ran out of clock.
run "${SUT}" echo
if [ "$(rc)" = 0 ] && [ "$(cat "${TMP}/out")" = "./..." ]; then
	ok "the unpartitioned argv is unchanged by the progress flag" "./..."
else
	bad "the unpartitioned argv gained a flag" "got '$(cat "${TMP}/out")'"
fi

# (25) The argv runs INSIDE ./core. `go test` resolves `./...` from the working
#      directory, so this is the difference between racing ./core and racing the root.
run "${SUT}" pwd
if [ "$(rc)" = 0 ] && [ "$(head -1 "${TMP}/out")" = "${ROOT}/core" ]; then
	ok "the argv runs inside ./core" "$(head -1 "${TMP}/out" | tail -c 30)"
else
	bad "the argv should run inside ./core" "got '$(head -1 "${TMP}/out")'"
fi

# (26) EXIT PROPAGATION, which is the whole point of the wrapper chain: a failing argv is a
#      failing task. `false` exits 1 whatever its arguments are.
run "${SUT}" false
if [ "$(rc)" = 1 ]; then
	ok "a failing argv propagates its status" "rc=1"
else
	bad "a failing argv must not be reported as a pass" "rc=$(rc)"
fi
run "${SUT}" true
if [ "$(rc)" = 0 ]; then
	ok "a succeeding argv propagates its status" "rc=0"
else
	bad "a succeeding argv should be rc=0" "rc=$(rc)"
fi

# ── mutants of the rule ──────────────────────────────────────────────────────────────────
# Each copies the script, changes ONE thing, and demands the control notice. A mutation that
# does not APPLY proves nothing, so each one is compared against the original first.
printf 'github.com/olivaresai/olivares/core/PKGDROP\n' >"${TMP}/mut.inv"
cat "${TMP}/inv" >>"${TMP}/mut.inv"

# (28) A package that reaches NO partition: the failure that goes green without this control.
if mutate drop 's|^\t\tPLACED="${PLACED}${best}:${w}:${p}".*|\t\tif [ "${p}" != "github.com/olivaresai/olivares/core/PKGDROP" ]; then PLACED="${PLACED}${best}:${w}:${p}"$'"'"'\\n'"'"'; fi|'; then
	rc=0
	bash "${TMP}/drop.sh" --check 4 - <"${TMP}/mut.inv" >"${TMP}/out" 2>"${TMP}/err" || rc=$?
	if [ "${rc}" = 1 ] && errhas "NOBODY"; then
		ok "mutant: a package owned by nobody is caught" "rc=1"
	else
		bad "a package owned by nobody was NOT caught" "rc=${rc}: $(head -2 "${TMP}/err" | tr '\n' ' ')"
	fi
fi

# (29) A package owned TWICE: raced twice, one runner's budget spent on a duplicate.
if mutate double 's|^\t\tPLACED="${PLACED}${best}:${w}:${p}".*|\t\tPLACED="${PLACED}${best}:${w}:${p}"$'"'"'\\n'"'"'; if [ "${p}" = "github.com/olivaresai/olivares/core/PKGDROP" ]; then PLACED="${PLACED}1:${w}:${p}"$'"'"'\\n'"'"'; fi|'; then
	rc=0
	bash "${TMP}/double.sh" --check 4 - <"${TMP}/mut.inv" >"${TMP}/out" 2>"${TMP}/err" || rc=$?
	if [ "${rc}" = 1 ]; then
		ok "mutant: a package owned twice is caught" "rc=1"
	else
		bad "a package owned twice was NOT caught" "rc=${rc}"
	fi
fi

# (30) Every package to partition 1: three jobs would boot a Postgres and race nothing.
if mutate onebucket 's|^\t\t\tif \[ "${load\[i\]}" -lt .*|\t\t\t:|'; then
	rc=0
	bash "${TMP}/onebucket.sh" --check 4 - <"${TMP}/inv" >"${TMP}/out" 2>"${TMP}/err" || rc=$?
	if [ "${rc}" = 1 ] && errhas "owns NO package"; then
		ok "mutant: an empty partition is caught" "rc=1"
	else
		bad "an empty partition was NOT caught" "rc=${rc}: $(head -1 "${TMP}/err")"
	fi
fi

# (31) CONTROL POSITIVE for the range refusal. Without it, "index 5 of 4 is refused" could
#      be passing for any other reason; with the bound removed the same input goes through.
if mutate norange 's|if \[ "${i}" -gt "${n}" \]; then|if [ "${i}" -gt 999999 ]; then|'; then
	rc=0
	bash "${TMP}/norange.sh" --assign 5 4 <"${TMP}/inv" >"${TMP}/out" 2>"${TMP}/err" || rc=$?
	if [ "${rc}" = 0 ]; then
		ok "control positive: the range bound is what refuses index 5 of 4" "mutant accepts it"
	else
		bad "the range mutant did not change the answer" "rc=${rc}; case (14) proves nothing"
	fi
fi

# ── B. THE WIRING ────────────────────────────────────────────────────────────────────────
# Text, not YAML: no container of ours is guaranteed to have PyYAML (that is why
# check-ci-env-reach was ported to Go), and the assertions below are about literal lines
# inside one job block.

jobblock() { # jobblock <file> <job-id>
	awk -v job="  $2:" '
		$0 == job { inblock = 1; print; next }
		inblock && /^  [a-zA-Z0-9_-]+:/ { inblock = 0 }
		inblock { print }
	' "$1"
}

# has <pattern> <file> — a plain grep, with no pipeline for `grep -q` to close: under
# pipefail `producer | grep -q X` returns 141 exactly when it MATCHES, which a boolean then
# reads as absent (scripts/check-sigpipe-booleans.sh). The job blocks are written to files
# once per call for the same reason.
has() { grep -q "$1" "$2"; }

# wiring <workflow> — prints one line per finding, exits 1 if there are any.
wiring() {
	local wf="$1" findings=0
	local mods="${TMP}/mods.txt" agg="${TMP}/agg.txt"
	jobblock "${wf}" race-core >"${mods}"
	jobblock "${wf}" race-hot >"${agg}"
	[ -s "${mods}" ] || {
		printf 'no race-core job\n'
		return 1
	}
	[ -s "${agg}" ] || {
		printf 'no race-hot aggregator\n'
		return 1
	}
	local n
	n="$(grep -oE 'partition: \[[0-9, ]+\]' "${mods}" | grep -oE '[0-9]+' | grep -c . || true)"
	[ "${n}" -ge 2 ] || {
		printf 'race-core declares no partition matrix\n'
		findings=$((findings + 1))
	}
	has 'fail-fast: false' "${mods}" || {
		printf 'the matrix is fail-fast: one partition failing would CANCEL the evidence of the other three\n'
		findings=$((findings + 1))
	}
	# THE ONE THAT COSTS THREE LEGS: a concurrency group shared by the four matrix jobs with
	# cancel-in-progress cancels its own siblings.
	has 'matrix.partition' <(grep -A3 'concurrency:' "${mods}") || {
		printf 'the concurrency group does not name matrix.partition: the four partitions would cancel each other\n'
		findings=$((findings + 1))
	}
	has 'OLIVARES_CORE_RACE_PARTITION: ' "${mods}" || {
		printf 'race-core does not pass the partition index to the task\n'
		findings=$((findings + 1))
	}
	# THE COUNT IS EITHER DERIVED OR EQUAL, and a literal that disagrees with the matrix is
	# the quiet one: the indexes above the declared count own packages nobody ever races,
	# with every leg green.
	local declared num
	declared="$(grep -oE 'OLIVARES_CORE_RACE_PARTITIONS:.*' "${mods}" | head -1 || true)"
	if [ -z "${declared}" ]; then
		printf 'race-core does not declare a partition count\n'
		findings=$((findings + 1))
	elif ! has 'strategy.job-total' <(printf '%s\n' "${declared}"); then
		num="$(printf '%s' "${declared}" | grep -oE '[0-9]+' | head -1 || true)"
		[ "${num:-0}" = "${n}" ] || {
			printf 'the declared partition count (%s) is not the matrix length (%s): the missing indexes own packages nobody runs\n' "${num:-none}" "${n}"
			findings=$((findings + 1))
		}
	fi
	# CONTINUE-ON-ERROR, read where YAML actually allows it. `continue-on-error` is a step
	# property and a job property, and inside a step it may sit BEFORE or AFTER `run:` — the
	# first version of this check only looked after the `task` command and would have missed
	# the ordinary placement. So: capture the WHOLE verdict-bearing step, from its own
	# `- name:` to the next one, and inspect that; then inspect the job mapping separately.
	# The advisory timeout-headroom census keeps its documented `continue-on-error`
	# ("warns above 75%, does not block") and must not be caught by either test.
	#
	# Narrower than the modules twin only because race-core's job block genuinely reaches its
	# census step (jobblock ends at the next top-level key and race-modules' block stops
	# before its own identical census). The modules check is weaker than it reads; that is a
	# finding, not a licence to relax this one.
	awk '
		/^      - name:/ { instep = ($0 ~ /race \(the \.\/core module/) }
		instep { print }
	' "${mods}" >"${TMP}/verdict-step.txt"
	[ -s "${TMP}/verdict-step.txt" ] || {
		printf 'the verdict-bearing race step could not be found in the race-core job\n'
		findings=$((findings + 1))
	}
	has 'task test:race-hot:core' "${TMP}/verdict-step.txt" || {
		printf 'the captured verdict-bearing step does not run task test:race-hot:core\n'
		findings=$((findings + 1))
	}
	has 'continue-on-error' "${TMP}/verdict-step.txt" && {
		printf 'the race-core verdict step is continue-on-error: a red would be hidden\n'
		findings=$((findings + 1))
	}
	# Job-level: a `continue-on-error` on the job mapping hides EVERY step, including the one
	# above. It sits at the job's own indentation, which no step property ever uses.
	grep -qE '^    continue-on-error' "${mods}" && {
		printf 'the race-core JOB is continue-on-error: every step red would be hidden\n'
		findings=$((findings + 1))
	}
	# Positive control for the pair above: the advisory census still declares its own
	# continue-on-error, so neither test passes by the property being absent everywhere.
	has 'continue-on-error' "${mods}" || {
		printf 'the advisory timeout-headroom census lost its continue-on-error: the two tests above would pass vacuously\n'
		findings=$((findings + 1))
	}
	# The teed failure evidence must be per-partition or one leg overwrites another's name
	# in the report the failure action assembles from $RUNNER_TEMP/ci-fail-*.log.
	has 'ci-fail-race-core-.*matrix.partition' "${mods}" || {
		printf 'the teed failure log is not unique per partition\n'
		findings=$((findings + 1))
	}
	# The aggregator is the required context: it must WAIT for the matrix and refuse
	# anything that is not a success.
	has 'needs:.*race-core' "${agg}" || {
		printf 'the race-hot aggregator does not depend on race-core\n'
		findings=$((findings + 1))
	}
	has 'RESULT_CORE: ${{ needs.race-core.result }}' "${agg}" || {
		printf 'the aggregator does not read race-core.result\n'
		findings=$((findings + 1))
	}
	has 'race-core=$RESULT_CORE' "${agg}" || {
		printf 'the aggregator does not judge race-core in its verdict loop\n'
		findings=$((findings + 1))
	}
	has 'continue-on-error' "${agg}" && {
		printf 'the aggregator has a continue-on-error step\n'
		findings=$((findings + 1))
	}
	[ "${findings}" = 0 ] || return 1
	return 0
}

# (32) The real workflow satisfies all of it.
if wiring "${WF}" >"${TMP}/wiring" 2>&1; then
	ok "the real mainline-ci wiring is complete" "matrix, group, count, evidence, aggregation"
else
	bad "the real mainline-ci wiring is incomplete" "$(head -2 "${TMP}/wiring" | tr '\n' ' ')"
fi

# wiring mutants. Each has to redden, and the one that matters most is the concurrency group.
wf_mutant() { # wf_mutant <label> <sed-expression> <expected-substring>
	local label="$1" expr="$2" want="$3"
	sed "${expr}" "${WF}" >"${TMP}/wf.yml"
	if cmp -s "${WF}" "${TMP}/wf.yml"; then
		bad "MUTATION DID NOT APPLY: ${label}" "the anchor moved; this case proves nothing"
		return
	fi
	if wiring "${TMP}/wf.yml" >"${TMP}/wf.out" 2>&1; then
		bad "${label}" "the mutated workflow passed"
	elif grep -qF "${want}" "${TMP}/wf.out"; then
		ok "mutant: ${label}" "named"
	else
		bad "${label}" "red, but for the wrong reason: $(head -1 "${TMP}/wf.out")"
	fi
}
wf_mutant "a concurrency group without matrix.partition is caught" \
	's|group: mainline-ci-race-core-${{ matrix.partition }}|group: mainline-ci-race-core|' \
	'cancel each other'
wf_mutant "fail-fast: true is caught" \
	'/^  race-core:/,/^    runs-on:/ s|fail-fast: false|fail-fast: true|' \
	'fail-fast'
wf_mutant "a declared count that is not the matrix length is caught" \
	's|OLIVARES_CORE_RACE_PARTITIONS: ${{ strategy.job-total }}|OLIVARES_CORE_RACE_PARTITIONS: "3"|' \
	'is not the matrix length'
wf_mutant "an aggregator that stops judging race-core is caught" \
	's|"race-core=$RESULT_CORE"|"race-rest=$RESULT_REST"|' \
	'verdict loop'
# The anchors below name the STEP (its id) and the JOB (its block), never a timeout literal:
# the ceilings are sized from measurement (ci-time series) and a mutant anchored on a number
# stops applying the day the number moves — measured on run 35073941231 (race-modules p1),
# where 180/190 had become 334/345 and both cases reported MUTATION DID NOT APPLY.
wf_mutant "continue-on-error BEFORE run on the verdict step is caught" \
	'/^  race-core:/,/^  race-sessions:/ s|^        id: race-core$|        id: race-core\n        continue-on-error: true|' \
	'verdict step is continue-on-error'
wf_mutant "continue-on-error AFTER run on the verdict step is caught" \
	's|^        run: task test:race-hot:core \(.*\)$|        run: task test:race-hot:core \1\n        continue-on-error: true|' \
	'verdict step is continue-on-error'
wf_mutant "a job-level continue-on-error is caught" \
	'/^  race-core:/,/^    steps:/ s|^    timeout-minutes: [0-9][0-9]*$|    continue-on-error: true\n&|' \
	'JOB is continue-on-error'
wf_mutant "losing the advisory census continue-on-error is caught" \
	's|^        continue-on-error: true$||' \
	'would pass vacuously'
wf_mutant "a shared failure-log name is caught" \
	's|ci-fail-race-core-${{ matrix.partition }}.log|ci-fail-race-core.log|g' \
	'not unique per partition'

# (33) The recipe really is the consumer: if the Taskfile stops calling the helper, every
#      case above is measuring a script nothing runs.
if grep -q 'with-pg-env.sh bash scripts/core-race-partition.sh go test -race' "${TASKFILE}"; then
	ok "the Taskfile recipe runs the partition helper through the wrapper" "test:race-hot:core"
else
	bad "the modules race recipe no longer calls the helper" "this battery would be measuring dead code"
fi
if grep -q 'bash scripts/sqlstore-race-entry-partition.sh "$@"' "${SUT}"; then
	ok "partitioned run mode invokes the sqlstore entry helper" "same argv, same PG posture"
else
	bad "partitioned run mode does not invoke the sqlstore helper" "sqlstore would stay one 90-minute binary"
fi

# ── C. PROGRESS IS A PAIR ────────────────────────────────────────────────────────────────
# `-v` alone does not restore progress, and these two cases are why the partitioned argv
# carries `-v` AND `-p 1`. `go test` buffers a package's output whenever more than one
# package may run at a time and releases it in argv order — that is why the failed job
# printed all 28 package results in one burst at 02:34:52 after two silent hours. The
# fixture is two packages, one of which sleeps, and the measure is the distance between the
# first and the last output line.
STREAM="${TMP}/stream"
mkdir -p "${STREAM}/a" "${STREAM}/b" || blind "the streaming fixture could not be created"
printf 'module streamprobe\n\ngo 1.26\n' >"${STREAM}/go.mod"
{
	printf 'package a\n\nimport (\n\t"testing"\n\t"time"\n)\n\n'
	printf 'func TestStreamSlow(t *testing.T) { time.Sleep(3 * time.Second) }\n'
} >"${STREAM}/a/a_test.go"
{
	printf 'package b\n\nimport "testing"\n\n'
	printf 'func TestStreamFast(t *testing.T) {}\n'
} >"${STREAM}/b/b_test.go"

# stream_gap <-p width> -> STREAM_GAP, whole seconds between the first and the last line of
# `go test -v`. GOWORK and GOFLAGS are cleared so the probe measures the Go tool's own
# behaviour and not the environment this battery happens to run in.
STREAM_GAP=""
STREAM_EXIT=""
stream_gap() {
	local width="$1" first="" last="" line stamp
	# The process substitution below is a SUBSHELL: its status never reaches this one, so the
	# earlier version read a compile error's output, measured a zero-second "burst" and called
	# that the control positive. The child now appends its own status as a final line, which
	# is the only way to keep live streaming (needed for the gap) and still learn the exit.
	STREAM_EXIT=""
	while IFS= read -r line; do
		case "${line}" in
		"olivares-stream-exit:"*)
			STREAM_EXIT="${line#olivares-stream-exit:}"
			continue
			;;
		esac
		stamp="$(date -u +%s)"
		[ -n "${first}" ] || first="${stamp}"
		last="${stamp}"
		printf '%s %s\n' "${stamp}" "${line}"
	done < <(cd "${STREAM}" && GOWORK=off GOFLAGS= go test -count=1 -p "${width}" -v ./a ./b 2>&1; printf 'olivares-stream-exit:%s\n' "$?") >"${TMP}/stream-p${width}.log"
	if [ -z "${first}" ] || [ -z "${STREAM_EXIT}" ]; then
		STREAM_GAP=""
		return 1
	fi
	STREAM_GAP=$((last - first))
	return 0
}

# stream_healthy — the probe only measures streaming if the run actually PASSED both packages.
# A compile error exits nonzero and names no test, and it would otherwise satisfy a "burst".
stream_healthy() { # <width>
	[ "${STREAM_EXIT}" = "0" ] || return 1
	grep -q -- '--- PASS: TestStreamSlow' "${TMP}/stream-p$1.log" || return 1
	grep -q -- '--- PASS: TestStreamFast' "${TMP}/stream-p$1.log" || return 1
	return 0
}

# (h) At width 1 the run names each test as it happens: the slow package's result arrives
#     seconds before the last line of the run.
if stream_gap 1 && [ -n "${STREAM_GAP}" ] && stream_healthy 1; then
	if grep -q 'RUN   TestStreamSlow' "${TMP}/stream-p1.log" && [ "${STREAM_GAP}" -ge 2 ]; then
		ok "-v -p 1 streams named test progress while the run continues" "${STREAM_GAP}s first line to last"
	else
		bad "-v -p 1 did not stream" "gap ${STREAM_GAP}s, $(grep -c . "${TMP}/stream-p1.log" || true) line(s)"
	fi
else
	blind "the width-1 streaming probe did not produce a PASSING two-package run (exit \"${STREAM_EXIT}\"); a gap measured over a broken build proves nothing"
fi

# (i) CONTROL POSITIVE: the same fixture and the same `-v` at width 2 arrives in one burst
#     after both packages have finished. Without this case, (h) could be passing because
#     `-v` alone is enough, and the `-p 1` in the partitioned argv would be unexplained.
if stream_gap 2 && [ -n "${STREAM_GAP}" ] && stream_healthy 2; then
	if [ "${STREAM_GAP}" -le 1 ]; then
		ok "control positive: at width 2 the same output arrives in one burst" "${STREAM_GAP}s first line to last"
	else
		bad "width 2 also streamed" "gap ${STREAM_GAP}s; case (h) does not establish that -p 1 is what restores progress"
	fi
else
	blind "the width-2 streaming probe did not produce a PASSING two-package run (exit \"${STREAM_EXIT}\"); a zero-second burst over a compile error is not a control"
fi

# (j) CAUSAL CONTROL for (h) and (i): break the fixture's build and the probe must REFUSE.
#     Without this, both streaming cases could be reading a compile error — nonzero exit, no
#     named test, zero-second "burst" — and calling it a measurement of width.
cp "${STREAM}/b/b_test.go" "${TMP}/b_test.go.orig"
printf 'package b\n\nfunc init() { this is not go }\n' >>"${STREAM}/b/b_test.go"
if stream_gap 2; then
	if stream_healthy 2; then
		bad "a broken build still satisfied the streaming probe" "exit ${STREAM_EXIT}: the width control could pass over a compile error"
	else
		ok "control: a broken build makes the streaming probe refuse" "exit ${STREAM_EXIT}, no named PASS"
	fi
else
	ok "control: a broken build makes the streaming probe refuse" "the probe returned nonzero before measuring"
fi
cp "${TMP}/b_test.go.orig" "${STREAM}/b/b_test.go"
# And the restored fixture must be healthy again, or (h)/(i) above were measured on rubble.
if stream_gap 1 && stream_healthy 1; then
	ok "control: the restored fixture passes both packages again" "exit ${STREAM_EXIT}"
else
	bad "the fixture did not recover" "exit ${STREAM_EXIT}: the streaming cases cannot be trusted"
fi

# ── D. THE JOB'S ENVIRONMENT IS NOT A FIXTURE ────────────────────────────────────────────
# race-core exports the selector pair for the whole job, and this battery is a step of that
# job, so every partition hands the battery its own pair. In run 34572093665 the cases that
# mean "no selector" passed it on through run() and got a partitioned argv back: 48 passed,
# 4 failed, in partitions 1, 2 and 3. run() is the seam and removes both variables. This
# section exports each state the job can hand down against that seam, and against a bare
# child that bypasses it: a scrub only ever seen with nothing to scrub has never been seen to
# work, and the pre-push path inherits nothing.

# The values are read from the job, not typed here, so a fifth leg is a fifth state. The count
# is the number of values, which is what `strategy.job-total` hands the job.
jobblock "${WF}" race-core >"${TMP}/ambient-job"
grep -oE 'partition: \[[0-9, ]+\]' "${TMP}/ambient-job" | grep -oE '[0-9]+' >"${TMP}/ambient-matrix" || true
MATRIX=()
while IFS= read -r v; do
	[ -n "${v}" ] && MATRIX+=("${v}")
done <"${TMP}/ambient-matrix"
MATRIX_N="${#MATRIX[@]}"
[ "${MATRIX_N}" -ge 2 ] || blind "the race-core matrix could not be read from ${WF}; section D exports its values"

# bare <sut> <args...> is run() without the unset: the child gets whatever the shell exports.
bare() {
	local sut="$1"
	shift
	local rc=0
	bash "${sut}" "$@" >"${TMP}/out" 2>"${TMP}/err" || rc=$?
	printf '%s' "${rc}" >"${TMP}/rc"
}

# ambient <seam|bare> <index|-> <count|-> <args...> hands <args> to the helper through run()
# (`seam`) or bare(), in a subshell that EXPORTS the pair as the job does, `-` leaving that half
# unset. Nothing leaks back into the battery.
ambient() {
	local via="$1" idx="$2" cnt="$3"
	shift 3
	(
		unset OLIVARES_CORE_RACE_PARTITION OLIVARES_CORE_RACE_PARTITIONS
		[ "${idx}" = - ] || export OLIVARES_CORE_RACE_PARTITION="${idx}"
		[ "${cnt}" = - ] || export OLIVARES_CORE_RACE_PARTITIONS="${cnt}"
		if [ "${via}" = seam ]; then
			run "${SUT}" "$@"
		else
			bare "${SUT}" "$@"
		fi
	)
}

ran_unpartitioned() { [ "$(rc)" = 0 ] && [ "$(cat "${TMP}/out")" = "./..." ]; }
got() { printf 'rc=%s %s' "$(rc)" "$(head -c 24 "${TMP}/out" | tr '\n' ' ')"; }

# (j) Nothing inherited, which is the pre-push path. The seam and a bare child agree, and that
#     agreement is what makes a bare child's answer below the state's doing, not the probe's.
ambient seam - - echo
seam="$(got)"
ran_unpartitioned && seam=ok
ambient bare - - echo
if [ "${seam}" = ok ] && ran_unpartitioned; then
	ok "nothing inherited: the seam and a bare child both run ./..." "the reference for the states below"
else
	bad "nothing inherited should run ./... either way" "seam ${seam}, bare $(got)"
fi

# (k) Each value race-core exports, through the seam: the argv is `./...` and --packages is
#     the complete inventory, which are cases (23) and (5) of the failed run.
held=""
leaked=""
for v in "${MATRIX[@]}"; do
	ambient seam "${v}" "${MATRIX_N}" echo
	ran_unpartitioned || leaked="${leaked} ${v}/${MATRIX_N} echo $(got);"
	ambient seam "${v}" "${MATRIX_N}" --packages
	if [ "$(rc)" = 0 ] && cmp -s <(LC_ALL=C sort "${TMP}/out") "${TMP}/inv.sorted"; then
		held="${held} ${v}/${MATRIX_N}"
	else
		leaked="${leaked} ${v}/${MATRIX_N} --packages rc=$(rc) $(outlines) line(s);"
	fi
done
if [ -z "${leaked}" ]; then
	ok "each inherited matrix value leaves the run seam unpartitioned" "${held# }: ./... and ${INV_N} package(s)"
else
	bad "an inherited matrix value reached the run seam" "${leaked# }"
fi

# (l) CONTROL POSITIVE: the same values reach a bare child, which partitions. That is the
#     failed run's symptom, so (k) holds because of the seam and not because nothing arrived.
live=0
dead=""
for v in "${MATRIX[@]}"; do
	ambient bare "${v}" "${MATRIX_N}" "${ARGV_GO}" test
	if [ "$(rc)" = 0 ] && grep -q '^-v -p 1 github.com/' "${TMP}/out"; then
		live=$((live + 1))
	else
		dead="${dead} ${v}/${MATRIX_N} $(got);"
	fi
done
if [ -z "${dead}" ]; then
	ok "control positive: each matrix value partitions a bare child" "-v -p 1 in ${live} of ${MATRIX_N}"
else
	bad "an inherited matrix value did not reach a bare child" "${dead# } (k) proves nothing for it"
fi

# (m) What a wrong edit to the job would hand down: either half of the pair alone, or an index
#     outside the matrix. The helper refuses each with exit 2, so inherited through run() they
#     turn (22) and (26) red as well as the four above.
out_of_range="$((MATRIX_N + 1))"
leaked=""
ambient seam 1 - echo
ran_unpartitioned || leaked="${leaked} index-only $(got);"
ambient seam - "${MATRIX_N}" echo
ran_unpartitioned || leaked="${leaked} count-only $(got);"
ambient seam "${out_of_range}" "${MATRIX_N}" echo
ran_unpartitioned || leaked="${leaked} ${out_of_range}/${MATRIX_N} $(got);"
if [ -z "${leaked}" ]; then
	ok "an inherited half pair or bad index leaves run unpartitioned" "index-only, count-only, ${out_of_range}/${MATRIX_N}"
else
	bad "an inherited half pair or bad index reached the run seam" "${leaked# }"
fi

# (n) CONTROL POSITIVE: a bare child refuses each of those states, for its own reason.
dead=""
ambient bare 1 - echo
{ [ "$(rc)" = 2 ] && errhas "PAIRED"; } || dead="${dead} index-only $(got);"
ambient bare - "${MATRIX_N}" echo
{ [ "$(rc)" = 2 ] && errhas "PAIRED"; } || dead="${dead} count-only $(got);"
ambient bare "${out_of_range}" "${MATRIX_N}" echo
{ [ "$(rc)" = 2 ] && errhas "outside 1..${MATRIX_N}"; } || dead="${dead} ${out_of_range}/${MATRIX_N} $(got);"
if [ -z "${dead}" ]; then
	ok "control positive: a bare child refuses each of those states" "rc=2 three times, each for its reason"
else
	bad "a half pair or bad index did not reach a bare child" "${dead# } (m) proves nothing for it"
fi

# ── verdict ──────────────────────────────────────────────────────────────────────────────
total=$((pass + fail))
if [ "${total}" -lt "${DECLARED_CASES}" ]; then
	printf 'test-core-race-partition: COULD NOT LOOK — %d case(s) ran, %d declared. A run that measures less than it claims is a FAILED run.\n' \
		"${total}" "${DECLARED_CASES}" >&2
	exit 2
fi
printf 'test-core-race-partition: %d passed, %d failed (%d declared)\n' "${pass}" "${fail}" "${DECLARED_CASES}"
[ "${fail}" -eq 0 ] || exit 1
exit 0
