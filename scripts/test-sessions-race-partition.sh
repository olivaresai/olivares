#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-sessions-race-partition.sh — the battery for scripts/sessions-race-partition.sh and the
# live race-sessions wiring. Every case states what it would catch.
#
# Discovery is expensive here (it compiles the -race binary), so the union cases run on a
# SYNTHETIC inventory through `--assign`, which is the same placement code the real selection
# uses. One case pays for real discovery, once.
set -euo pipefail

# Fixture selectors must not inherit the CI matrix's pair. Otherwise the
# half-pair refusal becomes a valid pair and the unpartitioned argv case runs
# one shard. Only this battery process is changed; the CI job keeps its pair.
unset OLIVARES_SESSIONS_RACE_PARTITION OLIVARES_SESSIONS_RACE_PARTITIONS

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SUT="${ROOT}/scripts/sessions-race-partition.sh"
MOD="${ROOT}/scripts/modules-race-partition.sh"
WF="${ROOT}/.github/workflows/mainline-ci.yml"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/sessions-race-XXXXXX")"
trap 'rm -rf "${TMP}"' EXIT

PASSED=0
FAILED=0
DECLARED_CASES=39
ok() {
	printf 'ok    %-62s %s\n' "$1" "${2:-}"
	PASSED=$((PASSED + 1))
}
bad() {
	printf 'FAIL  %-62s %s\n' "$1" "${2:-}"
	FAILED=$((FAILED + 1))
}
blind() {
	printf 'test-sessions-race-partition: COULD NOT LOOK — %s\n' "$*" >&2
	exit 2
}

# A synthetic inventory that carries the shape that matters: PREFIX PAIRS. TestAlpha is a
# prefix of TestAlphaLonger, exactly as TestSessionsStream is of TestSessionsStreamConfinement
# in the real package. If the helper ever drops an anchor, a partition owning TestAlpha runs
# TestAlphaLonger too and both partitions report it.
INV="${TMP}/inv"
{
	printf 'TestAlpha\n'
	printf 'TestAlphaLonger\n'
	printf 'TestBeta\n'
	printf 'TestGamma\n'
	printf 'ExampleDelta\n'
	printf 'FuzzEpsilon\n'
	printf 'TestZeta\n'
} >"${INV}"
TOTAL=7

# ── A. UNION, OWNERSHIP AND ANCHORING ────────────────────────────────────────────────────
for n in 2 3 4; do
	all="${TMP}/all-${n}"
	: >"${all}"
	empty=0
	for i in $(seq 1 "${n}"); do
		if ! "${SUT}" --assign "${i}" "${n}" <"${INV}" >"${TMP}/p-${n}-${i}" 2>/dev/null; then
			empty=1
			break
		fi
		[ -s "${TMP}/p-${n}-${i}" ] || empty=1
		cat "${TMP}/p-${n}-${i}" >>"${all}"
	done
	if [ "${empty}" = 1 ]; then
		bad "n=${n}: a partition was empty or --assign failed" "coverage cannot be established"
		continue
	fi
	got="$(grep -c . "${all}" || true)"
	uniq_got="$(LC_ALL=C sort -u "${all}" | grep -c . || true)"
	missing="$(LC_ALL=C sort "${INV}" | comm -23 - <(LC_ALL=C sort -u "${all}") | grep -c . || true)"
	if [ "${got}" = "${TOTAL}" ] && [ "${uniq_got}" = "${TOTAL}" ] && [ "${missing}" = 0 ]; then
		ok "n=${n}: exact disjoint union of the inventory" "${got} entries, 0 duplicated, 0 unowned"
	else
		bad "n=${n}: not a partition" "got ${got}, unique ${uniq_got}, unowned ${missing}"
	fi
done

# Every entry KIND is owned: an Example or a Fuzz seed dropped here is coverage lost silently.
# The membership test reads the captured list WITHOUT a pipe. `… | grep -qx` leaves on the hit and
# closes the pipe, the producer dies of SIGPIPE, and under `pipefail` the pipeline returns 141
# exactly when the kind IS owned -- so the `if` would report an owned kind as dropped.
allkinds="$(cat "${TMP}/p-4-1" "${TMP}/p-4-2" "${TMP}/p-4-3" "${TMP}/p-4-4" 2>/dev/null || true)"
for kind in ExampleDelta FuzzEpsilon; do
	case "
${allkinds}
" in
	*"
${kind}
"*)
		ok "the ${kind%%[A-Z]*} entry ${kind} is owned" "kind preserved"
		;;
	*)
		bad "${kind} is owned by nobody" "a non-Test entry point was dropped"
		;;
	esac
done

# The regex the run mode would pass must anchor BOTH ends. This is the prefix-pair defence.
if OLIVARES_SESSIONS_RACE_PARTITION=1 OLIVARES_SESSIONS_RACE_PARTITIONS=4 \
	"${SUT}" --assign 1 4 <"${INV}" >"${TMP}/sel" 2>/dev/null; then
	if head -c 2 "${TMP}/sel" >/dev/null; then
		ok "--assign returns a selection for partition 1 of 4" "$(grep -c . "${TMP}/sel") entry(ies)"
	fi
else
	bad "--assign failed for partition 1 of 4" "the placement is unusable"
fi

# ── B. REFUSALS ──────────────────────────────────────────────────────────────────────────
rcof() { # <expected> <label> ...env/argv
	local want="$1" label="$2" rc=0
	shift 2
	"$@" >/dev/null 2>&1 || rc=$?
	if [ "${rc}" = "${want}" ]; then
		ok "${label}" "exit ${rc}"
	else
		bad "${label}" "exit ${rc}, wanted ${want}"
	fi
}
rcof 2 "half a selector pair is refused" env OLIVARES_SESSIONS_RACE_PARTITION=1 "${SUT}" --names
rcof 2 "a malformed selector (08) is refused" env OLIVARES_SESSIONS_RACE_PARTITION=08 OLIVARES_SESSIONS_RACE_PARTITIONS=4 "${SUT}" --names
rcof 2 "an out-of-range index is refused" env OLIVARES_SESSIONS_RACE_PARTITION=5 OLIVARES_SESSIONS_RACE_PARTITIONS=4 "${SUT}" --names
rcof 2 "an unknown mode is refused" "${SUT}" --nonsense
rcof 2 "no argv and no mode is refused" "${SUT}"

# An empty inventory is COULD NOT LOOK, never "nothing to run".
if printf '' | "${SUT}" --check 4 - >/dev/null 2>&1; then
	bad "an empty inventory was accepted" "a run that tests nothing would report success"
else
	rc=$?
	if [ "${rc}" = 2 ]; then
		ok "an empty inventory is COULD NOT LOOK, not an empty pass" "exit 2"
	else
		bad "an empty inventory exited ${rc}" "wanted 2"
	fi
fi

# More partitions than entries leaves one empty, and that is a property failure, not an input one.
if printf 'TestOnly\n' | "${SUT}" --check 4 - >/dev/null 2>&1; then
	bad "4 partitions over 1 entry was accepted" "three legs would test nothing and exit 0"
else
	rc=$?
	[ "${rc}" = 1 ] && ok "more partitions than entries is a property failure" "exit 1" ||
		bad "more partitions than entries exited ${rc}" "wanted 1"
fi

# ── B2. THE DEDICATED OWNER IS ONE RATIFIED PACKAGE, NOT "ANY PACKAGE THAT EXISTS" ───────
# Existing in the inventory is not the same as having a job that runs it. The earlier version
# accepted any real package, so excluding one whose owner does not exist would have dropped it
# with nobody reporting. The case below uses a REAL sibling package, not a typo.
rcof 2 "an EXISTING but unratified package is refused as dedicated" \
	env OLIVARES_MODULES_RACE_PARTITION=1 OLIVARES_MODULES_RACE_PARTITIONS=4 \
	OLIVARES_MODULES_RACE_DEDICATED=github.com/olivaresai/olivares/modules/governance \
	"${MOD}" --packages
rcof 2 "a nonexistent package is refused as dedicated" \
	env OLIVARES_MODULES_RACE_PARTITION=1 OLIVARES_MODULES_RACE_PARTITIONS=4 \
	OLIVARES_MODULES_RACE_DEDICATED=github.com/olivaresai/olivares/modules/nosuch \
	"${MOD}" --packages
# The REGIME is declared, not inherited: this case means "no selector", and race-modules
# exports the selector pair for its whole job, so without the `env -u` it runs inside a
# partition and the exclusion legitimately drops sessions. That is the defect that reddened
# job 103739990101 in the sibling battery; here it was masked, because that battery fails
# first and the wrapper never reached this line.
if env -u OLIVARES_MODULES_RACE_PARTITION -u OLIVARES_MODULES_RACE_PARTITIONS \
	OLIVARES_MODULES_RACE_DEDICATED=github.com/olivaresai/olivares/modules/sessions \
	"${MOD}" --packages >"${TMP}/full" 2>/dev/null &&
	grep -q 'modules/sessions$' "${TMP}/full"; then
	ok "no selector still sweeps the COMPLETE ./modules, sessions included" "$(grep -c . "${TMP}/full") package(s)"
else
	bad "the unpartitioned modules sweep lost a package" "the local recipe would run less than before"
fi

# ── B3. ANCHORING, DRIVEN THROUGH --regex AND A REAL RUN ─────────────────────────────────
# The previous battery claimed to test anchoring and did not: it called --assign and looked at
# two bytes. This builds a synthetic package carrying the exact shape that breaks — a name that
# is a prefix of another, plus an Example and a Fuzz — points a COPY of the helper at it, and
# runs the argv the helper would run.
SYN="${TMP}/syn"
mkdir -p "${SYN}/modules/anchorfix" || blind "the synthetic package could not be created"
{
	printf 'module anchorsyn

go 1.24
'
} >"${SYN}/modules/go.mod"
{
	printf 'package anchorfix

import (
	"fmt"
	"testing"
)

'
	printf 'func TestAlpha(t *testing.T) {}
'
	printf 'func TestAlphaLonger(t *testing.T) {}
'
	printf 'func TestBeta(t *testing.T) {}
'
	printf 'func TestXTestBeta(t *testing.T) {}
'
	printf 'func Example() {
    fmt.Println("x")
    // Output: x
}
'
	printf 'func FuzzThing(f *testing.F) { f.Add("s"); f.Fuzz(func(t *testing.T, s string) {}) }
'
} >"${SYN}/modules/anchorfix/a_test.go"
printf 'package anchorfix
' >"${SYN}/modules/anchorfix/a.go"

synhelper() { # <extra sed> -> path to a helper rooted at the synthetic tree
	local extra="${1:-}" out="${TMP}/syn-helper-${2:-base}.sh"
	sed -e "s|^ROOT=.*|ROOT=\"${SYN}\"|" -e 's|^PKG=.*|PKG=./anchorfix|' \
		-e 's|go test -race -count=1 -list|go test -count=1 -list|' ${extra:+-e "${extra}"} \
		"${SUT}" >"${out}"
	printf '%s' "${out}"
}

BASE_H="$(synhelper "" base)"
if OLIVARES_SESSIONS_RACE_PARTITION=1 OLIVARES_SESSIONS_RACE_PARTITIONS=2 \
	bash "${BASE_H}" --regex >"${TMP}/rx" 2>"${TMP}/rxerr"; then
	RX="$(cat "${TMP}/rx")"
	case "${RX}" in
	'^('*')$') ok "--regex anchors at BOTH ends" "${RX}" ;;
	*) bad "--regex is not anchored at both ends" "${RX}" ;;
	esac
else
	bad "--regex failed on the synthetic package" "$(tail -1 "${TMP}/rxerr")"
fi

# The partition that owns TestAlpha must run TestAlpha and NOT TestAlphaLonger.
# Checked capture, then a match without a pipe, for the same 141-on-success reason as the KIND
# case above. The helper's own status is still read: a `--names` that fails is not an owner, as
# before, and if neither partition owns TestAlpha the check below still calls the discovery
# unusable.
OWNER=""
for i in 1 2; do
	if ! names="$(OLIVARES_SESSIONS_RACE_PARTITION="${i}" OLIVARES_SESSIONS_RACE_PARTITIONS=2 \
		bash "${BASE_H}" --names 2>/dev/null)"; then
		continue
	fi
	case "
${names}
" in
	*"
TestAlpha
"*)
		OWNER="${i}"
		break
		;;
	esac
done
if [ -n "${OWNER}" ]; then
	RUN_RC=0
	OLIVARES_SESSIONS_RACE_PARTITION="${OWNER}" OLIVARES_SESSIONS_RACE_PARTITIONS=2 \
		bash "${BASE_H}" go test -count=1 >"${TMP}/run" 2>&1 || RUN_RC=$?
	RAN_A="$(grep -cE -- '^--- PASS: TestAlpha \(' "${TMP}/run" || true)"
	BLEED="$(grep -cE -- '^--- PASS: TestAlphaLonger \(' "${TMP}/run" || true)"
	if [ "${RUN_RC}" = 0 ] && [ "${RAN_A}" = 1 ] && [ "${BLEED}" = 0 ]; then
		ok "the anchored run selects TestAlpha with NO sibling bleed" "partition ${OWNER}: 1 selected, 0 bled"
	else
		bad "the anchored run bled into a prefix sibling" "TestAlpha=${RAN_A} TestAlphaLonger=${BLEED}"
	fi
else
	bad "no partition owned TestAlpha" "the synthetic discovery is unusable"
fi

# Mutate EACH anchor away and require this very check to catch it.
for m in start end; do
	if [ "${m}" = "start" ]; then
		H="$(synhelper 's|REGEX="\^(\${body})\\$"|REGEX="(${body})\\$"|' nostart)"
	else
		H="$(synhelper 's|REGEX="\^(\${body})\\$"|REGEX="^(${body})"|' noend)"
	fi
	if cmp -s "${BASE_H}" "${H}"; then
		bad "MUTATION DID NOT APPLY: the ${m} anchor" "the anchor moved; this case proves nothing"
		continue
	fi
	OLIVARES_SESSIONS_RACE_PARTITION="${OWNER:-1}" OLIVARES_SESSIONS_RACE_PARTITIONS=2 \
		bash "${H}" go test -count=1 >"${TMP}/mrun-${m}" 2>&1 || true
	# ^ defends against a name that ENDS with the selected one; $ against one that BEGINS
	# with it. Each mutant is checked against the sibling shape its anchor actually protects.
	if [ "${m}" = "start" ]; then
		MB="$(grep -cE -- '^--- PASS: TestXTestBeta \(' "${TMP}/mrun-${m}" || true)"
	else
		MB="$(grep -cE -- '^--- PASS: TestAlphaLonger \(' "${TMP}/mrun-${m}" || true)"
	fi
	if [ "${MB}" -gt 0 ]; then
		ok "mutant: dropping the ${m} anchor bleeds, and the check sees it" "TestAlphaLonger ran ${MB} time(s)"
	else
		bad "dropping the ${m} anchor did not bleed" "this case cannot detect an unanchored regex"
	fi
done

# The discovery command itself must distinguish an empty successful build from a failure.
# Stub only go's output; the production helper still executes discover() in its real root.
mkdir -p "${TMP}/empty-go" "${TMP}/failed-go"
printf '#!/bin/sh\nprintf "ok fixture 0.001s\\n"\n' >"${TMP}/empty-go/go"
printf '#!/bin/sh\nprintf "owned-discovery-build-failure\\n" >&2\nexit 23\n' >"${TMP}/failed-go/go"
chmod +x "${TMP}/empty-go/go" "${TMP}/failed-go/go"
for kind in empty failed; do
    DISCOVERY_RC=0
    PATH="${TMP}/${kind}-go:${PATH}" "${SUT}" --inventory >"${TMP}/discovery-${kind}" 2>&1 || DISCOVERY_RC=$?
    if [ "${kind}" = empty ]; then SIGNAL='SUCCEEDED.*NO Test/Example/Fuzz'; else SIGNAL='DISCOVERY/BUILD FAILED.*exit 23'; fi
    if [ "${DISCOVERY_RC}" = 2 ] && grep -q 'COULD NOT LOOK' "${TMP}/discovery-${kind}" && grep -qE "${SIGNAL}" "${TMP}/discovery-${kind}"; then
        ok "${kind} discovery has the exact observation-failure status" "exit 2 with named cause"
    else
        bad "${kind} discovery lost its status or diagnostic" "exit ${DISCOVERY_RC}"
    fi
done

# Both runnable non-Test kinds must be discovered and executed once across the partitions.
KINDS_OK=1
bash "${BASE_H}" --inventory >"${TMP}/synthetic-inventory" 2>&1 || KINDS_OK=0
: >"${TMP}/synthetic-runs"
for i in 1 2; do
    OLIVARES_SESSIONS_RACE_PARTITION="${i}" OLIVARES_SESSIONS_RACE_PARTITIONS=2 \
        bash "${BASE_H}" go test -count=1 >>"${TMP}/synthetic-runs" 2>&1 || KINDS_OK=0
done
for name in Example FuzzThing; do
    grep -qx "${name}" "${TMP}/synthetic-inventory" || KINDS_OK=0
    # Go prints the elapsed duration immediately after the opening parenthesis.
    COUNT="$(grep -cF -- "--- PASS: ${name} (" "${TMP}/synthetic-runs" || true)"
    [ "${COUNT}" = 1 ] || KINDS_OK=0
done
if [ "${KINDS_OK}" = 1 ]; then
    ok "runnable Example and Fuzz seed corpus execute exactly once" "both partitions exit 0"
else
    bad "Example or Fuzz ownership or execution is incomplete" "inspect synthetic inventory and real terminal output"
fi

# ── C. REAL DISCOVERY, PAID ONCE ─────────────────────────────────────────────────────────
if "${SUT}" --check 4 >"${TMP}/real" 2>&1; then
	n="$(grep -oE '[0-9]+ entry point' "${TMP}/real" | grep -oE '[0-9]+' | head -1 || true)"
	if [ -n "${n}" ] && [ "${n}" -gt 100 ]; then
		ok "real discovery under -race partitions the live package" "${n} entry points, union clean"
	else
		bad "real discovery returned an implausible inventory" "$(head -1 "${TMP}/real")"
	fi
else
	blind "real discovery failed: $(tail -1 "${TMP}/real")"
fi

# A DISCOVERY/BUILD failure must be exit 2 and say so, never an empty pass. Forced by pointing
# the helper's package at one that cannot build.
if OLIVARES_SESSIONS_RACE_PARTITION=1 OLIVARES_SESSIONS_RACE_PARTITIONS=4 \
	bash -c 'sed "s|^PKG=.*|PKG=./nosuchpackage|" "$1" >"$2/mut.sh"; bash "$2/mut.sh" --names' _ "${SUT}" "${TMP}" >/dev/null 2>&1; then
	bad "a package that cannot be discovered still produced a selection" "a build failure would read as coverage"
else
	rc=$?
	[ "${rc}" = 2 ] && ok "a discovery/build failure is exit 2, not an empty pass" "exit 2" ||
		bad "a discovery failure exited ${rc}" "wanted 2"
fi

# ── D. RUN MODE ──────────────────────────────────────────────────────────────────────────
rcof 1 "a failing argv propagates its status" "${SUT}" false
if "${SUT}" echo >"${TMP}/argv" 2>/dev/null && grep -q -- './sessions' "${TMP}/argv" && ! grep -q -- '-run' "${TMP}/argv"; then
	ok "no selector runs the COMPLETE package with no -run" "$(cat "${TMP}/argv")"
else
	bad "the unpartitioned argv is not the complete package" "$(cat "${TMP}/argv" 2>/dev/null)"
fi

# ── E. LIVE WIRING ───────────────────────────────────────────────────────────────────────
jobblock() { # <file> <job-id>
	awk -v job="  $2:" '
		$0 == job { inblock = 1; print; next }
		inblock && /^  [a-zA-Z0-9_-]+:/ { inblock = 0 }
		inblock { print }
	' "$1"
}
wiring() { # <workflow> -> findings on stdout
	local wf="$1" findings=0 sess agg mods
	sess="${TMP}/sess.txt"
	agg="${TMP}/agg.txt"
	mods="${TMP}/mods.txt"
	jobblock "${wf}" race-sessions >"${sess}"
	jobblock "${wf}" race-hot >"${agg}"
	jobblock "${wf}" race-modules >"${mods}"
	[ -s "${sess}" ] || {
		printf 'there is NO race-sessions job: modules/sessions has no owner\n'
		return 1
	}
	grep -q 'partition: \[1, 2, 3, 4\]' "${sess}" || {
		printf 'race-sessions declares no four-way partition matrix\n'
		findings=$((findings + 1))
	}
	# Anchored at its exact indentation: a bare `grep fail-fast: false` also matches the
	# COMMENT above the setting, so the mutant that flips the setting passed while the prose
	# still said false. A check that reads the file's prose is not reading its configuration.
	grep -qE '^      fail-fast: false$' "${sess}" || {
		printf 'the matrix is fail-fast: one partition failing would CANCEL the other three\n'
		findings=$((findings + 1))
	}
	grep -q 'OLIVARES_SESSIONS_RACE_PARTITIONS: ${{ strategy.job-total }}' "${sess}" || {
		printf 'the declared count is not strategy.job-total: it can drift from the matrix length\n'
		findings=$((findings + 1))
	}
	# The GROUP line itself, not "matrix.partition appears somewhere in the job": deleting it
	# from the group alone left the name, the selector and the log carrying it, so a job-wide
	# grep passed while all four legs shared one cancelling group.
	grep -qE '^      group: .*\$\{\{ matrix\.partition \}\}' "${sess}" || {
		printf 'the concurrency GROUP does not name the partition: the four legs would cancel each other\n'
		findings=$((findings + 1))
	}
	# The selector index must be the matrix value, and the teed log must be per partition.
	grep -qE '^      OLIVARES_SESSIONS_RACE_PARTITION: \$\{\{ matrix\.partition \}\}' "${sess}" || {
		printf 'the partition INDEX selector is not the matrix value: every leg would run the same partition\n'
		findings=$((findings + 1))
	}
	grep -q 'ci-fail-race-sessions-\${{ matrix.partition }}' "${sess}" || {
		printf 'the failure log is not per partition: one leg would overwrite another evidence\n'
		findings=$((findings + 1))
	}
	# The verdict-bearing step must not hide a red; the advisory census keeps its own.
	awk '
		/^      - name:/ { instep = ($0 ~ /race \(modules\/sessions/) }
		instep { print }
	' "${sess}" >"${TMP}/verdict.txt"
	[ -s "${TMP}/verdict.txt" ] || {
		printf 'the verdict-bearing race step could not be found in race-sessions\n'
		findings=$((findings + 1))
	}
	grep -q 'continue-on-error' "${TMP}/verdict.txt" && {
		printf 'the race-sessions verdict step is continue-on-error: a red would be hidden\n'
		findings=$((findings + 1))
	}
	grep -q 'continue-on-error' "${sess}" || {
		printf 'the advisory census lost its continue-on-error: the two tests above would pass vacuously\n'
		findings=$((findings + 1))
	}
	grep -qE '^    continue-on-error' "${sess}" && {
		printf 'the race-sessions JOB is continue-on-error: every step red would be hidden\n'
		findings=$((findings + 1))
	}
	grep -q 'needs:.*race-sessions' "${agg}" || {
		printf 'the race-hot aggregator does not depend on race-sessions\n'
		findings=$((findings + 1))
	}
	grep -q 'RESULT_SESSIONS: ${{ needs.race-sessions.result }}' "${agg}" || {
		printf 'the aggregator does not read race-sessions.result\n'
		findings=$((findings + 1))
	}
	grep -q 'race-sessions=$RESULT_SESSIONS' "${agg}" || {
		printf 'the aggregator does not judge race-sessions in its verdict loop\n'
		findings=$((findings + 1))
	}
	# The exclusion and its owner are ONE decision. A modules job that drops sessions without
	# the dedicated job existing would lose the package with nobody reporting it.
	grep -q 'OLIVARES_MODULES_RACE_DEDICATED: github.com/olivaresai/olivares/modules/sessions' "${mods}" || {
		printf 'race-modules does not declare the dedicated owner: the split is not wired\n'
		findings=$((findings + 1))
	}
	[ "${findings}" = 0 ] || return 1
	return 0
}

if wiring "${WF}" >"${TMP}/wiring" 2>&1; then
	ok "the live mainline-ci wiring owns sessions exactly once" "matrix, selectors, aggregate and exclusion"
else
	bad "the real mainline-ci wiring is incomplete" "$(tr '\n' ' ' <"${TMP}/wiring")"
fi

# The optional 4th argument is the line the mutation must LEAVE BEHIND, and it exists because
# a mutation is only a control if it removes the ONE thing it names. A sed broad enough to
# also take out an unrelated dependency still reddens the wiring check, and still reddens it
# for the expected reason, so `want` alone cannot tell a targeted mutant from a blunt one.
wf_mutant() { # <label> <sed> <expected substring> [<line the mutation must PRESERVE>]
	local label="$1" expr="$2" want="$3" keep="${4:-}" mut="${TMP}/wf.yml"
	sed "${expr}" "${WF}" >"${mut}"
	if cmp -s "${WF}" "${mut}"; then
		bad "MUTATION DID NOT APPLY: ${label}" "the anchor moved; this case proves nothing"
		return
	fi
	if [ -n "${keep}" ]; then
		if grep -qxF -- "${keep}" "${mut}"; then
			ok "mutant is TARGETED: ${label}" "unrelated dependencies survive"
		else
			bad "mutant is NOT targeted: ${label}" "the mutation did not leave: ${keep}"
		fi
	fi
	if wiring "${mut}" >"${TMP}/mout" 2>&1; then
		bad "${label}" "the mutant PASSED the wiring check"
	elif grep -q "${want}" "${TMP}/mout"; then
		ok "mutant: ${label}" "caught"
	else
		bad "${label}" "caught for the wrong reason: $(tr '\n' ' ' <"${TMP}/mout")"
	fi
}
wf_mutant "a missing dedicated owner declaration is caught" \
	's|OLIVARES_MODULES_RACE_DEDICATED: github.com/olivaresai/olivares/modules/sessions||' \
	'does not declare the dedicated owner'
# THE AGGREGATOR DEPENDENCY. The anchor is derived from the live file instead of typed here:
# the previous literal froze a five-job list, the real one grew a sixth (race-hot-manifest),
# and from then on the sed matched nothing — the mutant was the workflow, and the case proved
# only that an UNMUTATED workflow passes. The sed now edits the element, not the whole line,
# and the derived `keep` line asserts that the other five dependencies are still there byte
# for byte, so "removes race-sessions" and "removes race-sessions ONLY" are both measured.
AGG_NEEDS="$(grep -m1 -E '^    needs: \[.*, race-sessions[],]' "${WF}" || true)"
[ -n "${AGG_NEEDS}" ] ||
	blind "no aggregator 'needs:' line names race-sessions in ${WF}; the dependency mutation would have nothing to remove"
wf_mutant "an aggregator that stops depending on race-sessions is caught" \
	'/^    needs: \[.*, race-sessions[],]/ s|, race-sessions||' \
	'does not depend on race-sessions' \
	"${AGG_NEEDS/, race-sessions/}"
wf_mutant "an aggregator that stops judging race-sessions is caught" \
	's|"race-sessions=$RESULT_SESSIONS"||' \
	'does not judge race-sessions'
wf_mutant "a stale declared count is caught" \
	's|OLIVARES_SESSIONS_RACE_PARTITIONS: ${{ strategy.job-total }}|OLIVARES_SESSIONS_RACE_PARTITIONS: "3"|' \
	'not strategy.job-total'
wf_mutant "removing the partition from the concurrency GROUP only is caught" \
	's|^      group: mainline-ci-race-sessions-\${{ matrix.partition }}-|      group: mainline-ci-race-sessions-|' \
	'concurrency GROUP does not name the partition'
wf_mutant "replacing the selector index with a literal is caught" \
	's|^      OLIVARES_SESSIONS_RACE_PARTITION: \${{ matrix.partition }}$|      OLIVARES_SESSIONS_RACE_PARTITION: "1"|' \
	'INDEX selector is not the matrix value'
wf_mutant "a shared failure-log name is caught" \
	's|ci-fail-race-sessions-\${{ matrix.partition }}|ci-fail-race-sessions|g' \
	'failure log is not per partition'
wf_mutant "continue-on-error on the verdict step is caught" \
	's|^        run: task test:race-hot:sessions \(.*\)$|        continue-on-error: true\n        run: task test:race-hot:sessions \1|' \
	'verdict step is continue-on-error'
wf_mutant "fail-fast: true is caught" \
	's|^      fail-fast: false$|      fail-fast: true|' \
	'fail-fast'
wf_mutant "a job-level continue-on-error is caught" \
	's|^  race-sessions:$|  race-sessions:\n    continue-on-error: true|' \
	'JOB is continue-on-error'

TOTAL_RUN=$((PASSED + FAILED))
if [ "${TOTAL_RUN}" != "${DECLARED_CASES}" ]; then
	printf 'test-sessions-race-partition: COULD NOT LOOK — %s case(s) ran, %s declared.\n' \
		"${TOTAL_RUN}" "${DECLARED_CASES}" >&2
	exit 2
fi
printf 'test-sessions-race-partition: %s passed, %s failed (%s declared)\n' "${PASSED}" "${FAILED}" "${DECLARED_CASES}"
[ "${FAILED}" = 0 ]
