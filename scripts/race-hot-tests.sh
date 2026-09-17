#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# race-hot-tests.sh — emit the ANCHORED `go test -run` regex selecting the
# concurrency-hot tests of the cmd/olivares ROOT package (the push gate's -race
# leg; see Taskfile `test:race-hot` and the split rationale there).
#
# WHY a generated regex, not a hand-written allowlist: a hand list drifts (new
# tests in a hot FILE silently lose -race until the weekly sweep) and an
# unanchored list over-selects by prefix accident. This script derives the set
# from a versioned MANIFEST of hot files, anchors it, and fails CLOSED:
#   - a manifest glob that resolves no files is an ERROR (file renamed/moved);
#   - zero extracted tests is an ERROR (grep drift);
# so gate coverage can only shrink loudly, never silently.
#
# The deliberate long-poll e2e tail (…StoryE2E) is EXCLUDED here — the weekly
# race-full workflow races it, so nothing is permanently unraced.
#
# Output: the full anchored regex on stdout; the selected test count on stderr.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}/cmd/olivares"

# MANIFEST — concurrency-bearing surfaces of the root package. Keep each entry
# justified; classify new concurrency test files here (or as an explicit
# exemption in the weekly sweep) when they land.
HOT_GLOBS=(
	"inferenceproxy*_test.go"     # inline inference PEP / authorize chain
	"natsbus_integration_test.go" # cross-node bus bridge (embedded NATS)
	"*pump_test.go"               # leader pumps: eventing/orch cadence+workflow/DR
	"retentionsweep_test.go"      # retention sweep loop
	"killswitch_e2e_test.go"      # kill-switch PEP/MCP seams (StoryE2E -> weekly)
	"license_holder_test.go"      # license hot-apply — its contract REQUIRES -race
)

# Long-poll story e2e stays out of the push gate by design (weekly races it).
EXCLUDE_RE='StoryE2E'

# WHY THERE IS NO `sed` AND NO `compgen` BELOW, and it is not style (2026-08-05). This helper is on
# the reviewed recipeHelpers list, which is what lets a race recipe run it OUTSIDE with-pg-env.sh,
# and checkpgwiring enforces the premise that comes with that: every command head here must be one
# that runs no word of its own argv. `sed` and `compgen` are not: GNU sed's `e` command runs a shell
# command taken from the sed script, and `compgen -C` names a command bash runs. Both were briefly
# allowed by a reviewed table entry that NAMED that residual — and the seventeenth contrast reached
# a `go test` straight through the sed one, checker exit 0. A residual a review has written down is
# still a residual. Every command here now takes its arguments as data, so the premise is decided
# rather than asserted. Keep it that way when editing: a new head has to be classified in
# checkpgwiring's inertCommands before this file may use it.
names=""
for g in "${HOT_GLOBS[@]}"; do
	# shellcheck disable=SC2206 — glob expansion is the point.
	files=(${g})
	if [ ! -e "${files[0]}" ]; then
		echo "race-hot-tests: manifest glob '${g}' resolves NO files — renamed/moved? Update the manifest (fail-closed)." >&2
		exit 1
	fi
	# `grep -o` prints the whole match, `func TestX`; the second field is the name.
	# shellcheck disable=SC2086 — glob expansion is the point.
	found=$(grep -hoE '^func Test[A-Za-z0-9_]+' ${g} | cut -d' ' -f2) || true
	if [ -z "${found}" ]; then
		echo "race-hot-tests: no Test funcs extracted from '${g}' (fail-closed)." >&2
		exit 1
	fi
	names="${names}${found}"$'\n'
done

selected=$(printf '%s' "${names}" | sort -u | grep -vE "${EXCLUDE_RE}" || true)
count=$(printf '%s\n' "${selected}" | grep -c . || true)
if [ "${count}" -eq 0 ]; then
	echo "race-hot-tests: selection is EMPTY after exclusions (fail-closed)." >&2
	exit 1
fi

echo "race-hot-tests: ${count} hot tests selected" >&2

# ═════════════════════════════════════════════════════════════════════════════════════════
# R112/HR1 — COST-BALANCED PARTITIONING OF THE SAME SELECTION (2026-09-13)
# ═════════════════════════════════════════════════════════════════════════════════════════
#
# WHY. Job 103739990067 of run 34763378851 died at `panic: test timed out after 15m0s`
# naming TestOrchCadencePumpRunOncePassesAllBusinessTenants, which had been running 1m23s.
# The diagnosis (assessments/engineering/r112-race-rest-cadence/REPORT.md) measured the
# alarm dump and the binary's own internal timestamps: 28 goroutines, exactly two blocked as
# long as a minute, one runnable holder in flight, every live goroutine descended from the
# running test's goroutine — while roughly eight full composition-root estate boots at
# 67-80s each had already spent about two thirds of the 15-minute BINARY budget. The panic
# names the entry IN FLIGHT; the selection as a whole spends the budget.
#
# That evidence SUPPORTS cumulative cost. It does not categorically refute every possible
# leak or deadlock — it refutes the ones that dump and that run could exhibit. Splitting the
# selection is a scheduling correction, not a certificate that nothing here can ever hang:
# Go's 15-minute cap still fires, still dumps, and a partition that really hangs still fails.
#
# NOTHING IS RELAXED. Every one of the entries above still runs, exactly once, under the
# same -race -count=1 and the same 15-minute cap. No test is removed, skipped, quarantined
# or reordered, and no cap is raised.
#
# WHY THE LOGIC IS HERE AND NOT IN A SEPARATE HELPER, which was the first design and is
# worth recording because the checker is what rejected it. cmd/olivares/tools/checkpgwiring
# permits this file to run OUTSIDE the wrapper on one reviewed premise — it emits a
# selection and starts no program — and it enforces that premise by reading this body: a
# recipeHelpers entry may not hand work to another `.sh`, may not run a shell runner, and
# may not nest shell deeper than the walk goes. A `hot-race-partition.sh` that shelled back
# into this file satisfied none of those (measured 2026-09-13: it turned 66 fixtures of
# scripts/test-pg-test-env.sh red, first through an unresolvable `bash "${MANIFEST}"` and
# then through the nesting depth). The premises are the point of that gate, so the design
# moved instead of the gate: the partition reads `selected`, which is already in this
# process, so there is no second parser, no second glob list that can drift from this one,
# and no new command for the checker to classify. Every head below is on its reviewed inert
# list.
#
# AND IT DEFINES NO FUNCTION, which is the second thing that gate taught this file. A
# recipeHelpers body is read by helperShell, and that reader — unlike the argv reader used
# on the partition helpers of ./modules, ./core and ./sessions — does not collect function
# definitions, so a call to a function this file defines looks to it exactly like a call to
# an unreviewed program: `runs "is_positive" … whether it reaches a `go test` is UNKNOWN`.
# That refusal is correct for what it can see. Everything below is therefore straight-line
# code with inline loops. It is more repetitive than it would like to be, and the repetition
# is the price of a premise that can be checked rather than asserted.
#
# SELECTORS, SET BOTH OR NEITHER — and CI is the only thing that sets them:
#   OLIVARES_HOT_RACE_PARTITION    1-based partition index
#   OLIVARES_HOT_RACE_PARTITIONS   number of partitions
# With NEITHER set, every output of this script is byte for byte what it has always been.
# That is the property the local leg rests on, and scripts/test-hot-race-partition.sh
# compares the bytes rather than trusting this sentence.
#
# THE COST CENSUS, AND EXACTLY WHAT IT IS. scripts/hot-race-costs.tsv carries
# `<entry><TAB><milliseconds>` hints measured on ONE developer container on 2026-09-13,
# where this whole selection ran 343.6s; the same selection exceeded 900s on ci-runner-8.
# A hint is therefore a DECLARED SCHEDULING INPUT: a relative ordering used to divide work
# deterministically, NOT a prediction of remote wall time and NOT evidence about any runner.
# What a partition costs in CI is a CI measurement, and check-test-timeout-headroom.sh is
# what reports it.
#
# An entry the census does not name is still OWNED, at DEFAULT_COST_MS: ownership never
# depends on an allowlist, so a newly added hot test cannot fall through. The opposite
# direction fails CLOSED — a census row naming an entry this manifest no longer selects is
# STALE and exits 1, because a stale row silently unbalances every partition and nothing
# else would ever report it.
#
# BALANCE is longest-processing-time-first: by declared cost descending, then by name, each
# entry to the partition with the least accumulated cost, ties to the lowest index. It reads
# the selection and the census and nothing else — no clock, host or run id — so every job
# and every later reader computes the same answer.
#
# EXIT CODES beyond the manifest's own fail-closed 1s above:
#   1  a property was violated: an empty partition, a duplicate or stale census row
#   2  an input could not be read: a malformed, oversized or out-of-range selector, half a
#      pair, an unknown argument, or a malformed census row
# A selector that cannot be read never degrades to selecting everything (one partition
# silently doing every partition's work) and never to selecting nothing (a green that raced
# nothing at all).

COSTS_FILE="${ROOT}/scripts/hot-race-costs.tsv"
# The cost an entry gets when the census does not name it. Deliberately at the high end of
# the cheap band rather than at zero: an unknown new hot test is likelier to be an ordinary
# one than a free one, and under-costing it is what would pile several new entries onto one
# partition.
DEFAULT_COST_MS=3000
MAX_SELECTOR_DIGITS=9

# ── The argument, if any ─────────────────────────────────────────────────────────────────
# An UNKNOWN argument is refused rather than ignored. This script used to ignore its argv
# entirely, so a typo (`--name`, `--regexp`) would have silently produced the whole-manifest
# regex and a caller would have run every hot test believing it ran a subset.
MODE=""
MODE_N=""
# ⛔ `if`, NOT `case`, AND THAT IS THE GATE TALKING AGAIN. cmd/olivares/tools/checkpgwiring
# reads this body command by command and does not model `case` patterns, so it took the
# pattern label `--names)` for a command head and answered UNVERIFIED on it — correctly, for
# what it can see. Measured 2026-09-13, the same way the function ban above was.
if [ "$#" -gt 0 ]; then
	if [ "$1" = "--names" ]; then
		MODE="names"
	elif [ "$1" = "--census" ]; then
		MODE="census"
		MODE_N="${2:-}"
	else
		echo "race-hot-tests: unknown argument '$1'; the modes are (none) for the anchored regex, --names for the selection one per line, and --census <n> for the partition census (fail-closed)." >&2
		exit 2
	fi
fi

# ── The selector pair ────────────────────────────────────────────────────────────────────
SEL_I="${OLIVARES_HOT_RACE_PARTITION:-}"
SEL_N="${OLIVARES_HOT_RACE_PARTITIONS:-}"
if [ -n "${SEL_I}" ] || [ -n "${SEL_N}" ]; then
	if [ -z "${SEL_I}" ] || [ -z "${SEL_N}" ]; then
		echo "race-hot-tests: COULD NOT LOOK — the selectors are PAIRED: the index read '${SEL_I}' and the count read '${SEL_N}' — set both, or neither and get the complete manifest." >&2
		exit 2
	fi
	digits="${SEL_N//[0-9]/}"
	if [ -z "${SEL_N}" ] || [ -n "${digits}" ] || [ "${SEL_N#0}" != "${SEL_N}" ] || [ "${#SEL_N}" -gt "${MAX_SELECTOR_DIGITS}" ]; then
		echo "race-hot-tests: COULD NOT LOOK — the partition COUNT '${SEL_N}' is not a decimal integer >= 1 of at most ${MAX_SELECTOR_DIGITS} digits." >&2
		exit 2
	fi
	digits="${SEL_I//[0-9]/}"
	if [ -z "${SEL_I}" ] || [ -n "${digits}" ] || [ "${SEL_I#0}" != "${SEL_I}" ] || [ "${#SEL_I}" -gt "${MAX_SELECTOR_DIGITS}" ]; then
		echo "race-hot-tests: COULD NOT LOOK — the partition INDEX '${SEL_I}' is not a decimal integer >= 1 of at most ${MAX_SELECTOR_DIGITS} digits." >&2
		exit 2
	fi
	if [ "${SEL_I}" -gt "${SEL_N}" ]; then
		echo "race-hot-tests: COULD NOT LOOK — the partition INDEX ${SEL_I} is outside 1..${SEL_N}; a partition that does not exist owns no entry, and no entry is not a pass." >&2
		exit 2
	fi
fi

# The census mode carries its own count and answers about EVERY partition, so it does not
# read the pair; the run modes take theirs from the pair.
PCOUNT=""
if [ "${MODE}" = "census" ]; then
	digits="${MODE_N//[0-9]/}"
	if [ -z "${MODE_N}" ] || [ -n "${digits}" ] || [ "${MODE_N#0}" != "${MODE_N}" ] || [ "${#MODE_N}" -gt "${MAX_SELECTOR_DIGITS}" ]; then
		echo "race-hot-tests: COULD NOT LOOK — --census needs a partition COUNT that is a decimal integer >= 1 of at most ${MAX_SELECTOR_DIGITS} digits; got '${MODE_N}'." >&2
		exit 2
	fi
	PCOUNT="${MODE_N}"
elif [ -n "${SEL_N}" ]; then
	PCOUNT="${SEL_N}"
fi

if [ -n "${PCOUNT}" ] && [ "${PCOUNT}" -gt "${count}" ]; then
	echo "race-hot-tests: FAIL — ${PCOUNT} partition(s) over ${count} entry point(s) leaves at least one partition EMPTY." >&2
	exit 1
fi

# ── The census, read and validated in ONE pass ───────────────────────────────────────────
# A census parsed here and checked somewhere else is a census whose two readers can disagree.
COSTED=""
if [ -n "${PCOUNT}" ]; then
	if [ ! -f "${COSTS_FILE}" ]; then
		echo "race-hot-tests: COULD NOT LOOK — the cost census scripts/hot-race-costs.tsv is MISSING; a partition built on silence is not balanced, it is arbitrary." >&2
		exit 2
	fi
	census_raw="$(cat "${COSTS_FILE}")"
	census_seen=""
	census_stale=""
	census_rows=0
	while IFS= read -r row; do
		[ -n "${row}" ] || continue
		[ "${row#\#}" = "${row}" ] || continue
		if [ "${row%%	*}" = "${row}" ]; then
			echo "race-hot-tests: COULD NOT LOOK — a cost census row is MALFORMED: '${row}' has no TAB, so its name and its cost cannot be told apart." >&2
			exit 2
		fi
		cname="${row%%	*}"
		ccost="${row#*	}"
		if [ "${ccost%%	*}" != "${ccost}" ]; then
			echo "race-hot-tests: COULD NOT LOOK — the cost census row for '${cname}' is MALFORMED: more than two TAB-separated fields." >&2
			exit 2
		fi
		digits="${ccost//[0-9]/}"
		if [ -z "${ccost}" ] || [ -n "${digits}" ] || [ "${ccost#0}" != "${ccost}" ]; then
			echo "race-hot-tests: COULD NOT LOOK — the cost census row for '${cname}' has cost '${ccost}', which is not a decimal integer >= 1 of milliseconds." >&2
			exit 2
		fi
		if [ "${cname#Test}" = "${cname}" ] && [ "${cname#Example}" = "${cname}" ] && [ "${cname#Fuzz}" = "${cname}" ]; then
			echo "race-hot-tests: COULD NOT LOOK — the cost census names '${cname}', which is not a Test/Example/Fuzz entry point." >&2
			exit 2
		fi
		# Membership is decided WITHOUT a pipe. `… | grep -qxF` returns 141 when it MATCHES:
		# the consumer leaves on the first hit, the producer dies of SIGPIPE, and under
		# `pipefail` the `if` reads the success as "absent". Feed the complete string
		# through a here-string: no producer pipeline, and no `case` grammar that the
		# reviewed-helper reader cannot inspect. Keep literal whole-line matching.
		if grep -qxF -- "${cname}" <<<"${census_seen}"; then
			echo "race-hot-tests: FAIL — the cost census names '${cname}' TWICE; two rows for one entry make the balance depend on which is read first." >&2
			exit 1
		fi
		if ! grep -qxF -- "${cname}" <<<"${selected}"; then
			census_stale="${census_stale}${cname}"$'\n'
		fi
		census_seen="${census_seen}${cname}"$'\n'
		COSTED="${COSTED}${cname}	${ccost}"$'\n'
		census_rows=$((census_rows + 1))
	done <<<"${census_raw}"
	if [ "${census_rows}" -eq 0 ]; then
		echo "race-hot-tests: COULD NOT LOOK — the cost census scripts/hot-race-costs.tsv names no entry at all." >&2
		exit 2
	fi
	if [ -n "${census_stale}" ]; then
		echo "race-hot-tests: FAIL — the cost census is STALE. It names entry point(s) this manifest no longer selects:" >&2
		printf '%s' "${census_stale}" >&2
		echo "  A stale row silently unbalances every partition and nothing else would report it. Remove the row(s), or restore the test." >&2
		exit 1
	fi
fi

# ── The placement ────────────────────────────────────────────────────────────────────────
# Longest-processing-time-first. Costs are attached in one inline pass over the census; the
# order is by cost DESCENDING then by name, so it never depends on the order the selection
# happened to arrive in; then each entry goes to the partition with the least accumulated
# cost, ties to the lowest index.
PLACED=""
COST_TOTALS=""
if [ -n "${PCOUNT}" ]; then
	ordered=""
	while IFS= read -r name; do
		[ -n "${name}" ] || continue
		this_cost="${DEFAULT_COST_MS}"
		while IFS= read -r row; do
			[ -n "${row}" ] || continue
			if [ "${row%%	*}" = "${name}" ]; then
				this_cost="${row#*	}"
				break
			fi
		done <<<"${COSTED}"
		ordered="${ordered}${this_cost} ${name}"$'\n'
	done <<<"${selected}"
	ordered="$(printf '%s' "${ordered}" | grep -v '^$' | LC_ALL=C sort -k1,1nr -k2,2)"
	if [ -z "${ordered}" ]; then
		echo "race-hot-tests: COULD NOT LOOK — the selection handed to the placement was EMPTY." >&2
		exit 2
	fi

	totals=()
	i=1
	while [ "${i}" -le "${PCOUNT}" ]; do
		totals[i]=0
		i=$((i + 1))
	done
	while IFS= read -r row; do
		[ -n "${row}" ] || continue
		name="${row#* }"
		best=1
		bestload="${totals[1]}"
		i=2
		while [ "${i}" -le "${PCOUNT}" ]; do
			load="${totals[i]}"
			if [ "${load}" -lt "${bestload}" ]; then
				best="${i}"
				bestload="${load}"
			fi
			i=$((i + 1))
		done
		totals[best]=$((bestload + ${row%% *}))
		PLACED="${PLACED}${best}:${name}"$'\n'
	done <<<"${ordered}"
	i=1
	while [ "${i}" -le "${PCOUNT}" ]; do
		COST_TOTALS="${COST_TOTALS}${i} ${totals[i]}"$'\n'
		i=$((i + 1))
	done
fi

# ── The census mode ──────────────────────────────────────────────────────────────────────
# It is a CHECK as well as a report: the union control below is what would catch an entry
# owned by nobody or by two, and it has already read the census, so a stale or malformed row
# is reported here instead of on the next CI run.
if [ "${MODE}" = "census" ]; then
	union=""
	i=1
	while [ "${i}" -le "${PCOUNT}" ]; do
		part_n=0
		while IFS= read -r entry; do
			[ -n "${entry}" ] || continue
			if [ "${entry%%:*}" = "${i}" ]; then
				union="${union}${entry#*:}"$'\n'
				part_n=$((part_n + 1))
			fi
		done <<<"${PLACED}"
		if [ "${part_n}" -eq 0 ]; then
			echo "race-hot-tests: FAIL — partition ${i} of ${PCOUNT} owns NO entry point." >&2
			exit 1
		fi
		while IFS= read -r row; do
			[ -n "${row}" ] || continue
			if [ "${row%% *}" = "${i}" ]; then
				echo "race-hot-tests: partition ${i}/${PCOUNT} — ${part_n} entry point(s), ${row#* } ms of DECLARED cost (a scheduling hint measured on one developer container, not a prediction of runner wall time)"
			fi
		done <<<"${COST_TOTALS}"
		i=$((i + 1))
	done
	bad="$(printf '%s\n%s' "${selected}" "${union}" | grep -v '^$' | LC_ALL=C sort | uniq -c | grep -vE '^[[:space:]]*2[[:space:]]' || true)"
	if [ -n "${bad}" ]; then
		echo "race-hot-tests: FAIL — the ${PCOUNT} partitions are NOT a partition of the selection." >&2
		echo "  An entry owned by exactly one partition appears TWICE below; 1 = owned by NOBODY, 3+ = owned TWICE." >&2
		printf '%s\n' "${bad}" >&2
		exit 1
	fi
	echo "race-hot-tests: CLEAN — ${count} entry point(s), ${PCOUNT} partition(s), every entry owned exactly once, no partition empty, no stale census row, every selection anchored"
	exit 0
fi

# ── The selection this invocation emits ──────────────────────────────────────────────────
# With NEITHER selector set — every developer shell — this is the complete list and every
# output below is byte for byte what it has always been.
SELECTION="${selected}"
if [ -n "${SEL_N}" ]; then
	part_out=""
	while IFS= read -r entry; do
		[ -n "${entry}" ] || continue
		if [ "${entry%%:*}" = "${SEL_I}" ]; then
			part_out="${part_out}${entry#*:}"$'\n'
		fi
	done <<<"${PLACED}"
	SELECTION="$(printf '%s' "${part_out}" | LC_ALL=C sort)"
	if [ -z "${SELECTION}" ]; then
		echo "race-hot-tests: FAIL — partition ${SEL_I} of ${SEL_N} owns NO entry point; an empty -run would select the whole package and report it as this partition's." >&2
		exit 1
	fi
	echo "race-hot-tests: partition ${SEL_I} of ${SEL_N}: $(printf '%s\n' "${SELECTION}" | grep -c .) entry point(s), anchored" >&2
fi

if [ "${MODE}" = "names" ]; then
	# The selection, one name per line, unanchored, for a caller that anchors it itself and
	# for the controls.
	printf '%s\n' "${SELECTION}" | grep .
	exit 0
fi

printf '^(%s)$\n' "$(printf '%s\n' "${SELECTION}" | grep . | paste -sd'|')"
