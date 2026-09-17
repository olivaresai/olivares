#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# modules-race-partition.sh — run an argv over the ./modules packages, or over one
# deterministic partition of them.
#
# Background: on 2026-09-11 the race-modules job (run 34545731818, job 103097824594,
# ci-runner-2) was killed by its 210-minute step ceiling with 28 packages reported green,
# modules/sessions still running and five packages never reported. Those 28 sum 31545 s of
# package wall inside 12600 s of step wall, so the leg did not hang: it did not fit on one
# runner. This helper adds a selector so CI can spend four runners on it.
#
# Invariant: every package `go list ./...` discovers has exactly one owner among the N
# partitions, including packages added later. The cost table below therefore affects balance
# only, never coverage. scripts/test-modules-race-partition.sh proves both.
#
# Run mode — the argv is the command, and it runs from ./modules:
#   scripts/modules-race-partition.sh go test -race -count=1 -timeout 150m
#     no selector  -> `./...`, byte for byte the pre-partition recipe
#     selector set -> `-v -p 1` plus the packages that partition owns
#
# Inspection modes, for the controls and for an operator:
#   --inventory         the canonical go-list inventory
#   --packages          the packages the current selector owns
#   --assign <i> <n>    stdin inventory -> the packages of partition i of n
#   --check <n> [-]     union / no duplicate / no empty partition
#   --census <n> [-]    --check plus the per-partition declared load
# `--assign` always reads the inventory from stdin, and `--check`/`--census` do when their
# third argument is `-`; that is how a control feeds a synthetic future inventory.
#
# Selectors, set both or neither:
#   OLIVARES_MODULES_RACE_PARTITION    1-based partition index
#   OLIVARES_MODULES_RACE_PARTITIONS   number of partitions
#
# Exit codes:
#   0  the selection was made, or the argv ran and its status is propagated
#   1  a property was violated: an empty partition, or a package with no owner or with two
#   2  an input could not be read: a malformed, oversized or out-of-range selector, half a
#      pair, an unknown mode, or a `go list` that failed or returned nothing
# A selector that cannot be read never degrades to running everything, and never to running
# nothing: an empty `go test` argv tests the current directory and exits 0.
#
# Shape constraints. This file is a reviewed entry of checkpgwiring's argvRunnerScripts
# table, so its body is read by the walker that judges the recipe, and `task lint:pg-env`
# answers UNVERIFIED for anything that walker cannot classify. Hence: no sed, awk, python3,
# here-document or process substitution (each arrives as an unreviewed command head), and no
# `$(local_function ...)` (the walker recurses with only the substitution's text, so a local
# call reaches it as an unknown program). Every function returns through a named global.
# A `<<<` here-string is fine, and is deliberate: `| while read` would run the loop in a
# subshell and discard the running totals it keeps.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

say() { printf 'modules-race-partition: %s\n' "$*" >&2; }
fail() { printf 'modules-race-partition: FAIL — %s\n' "$*" >&2; exit 1; }
blind() { printf 'modules-race-partition: COULD NOT LOOK — %s\n' "$*" >&2; exit 2; }

# Declared seconds, measured in job 103097824594 at -p=5 on ci-runner-2, rounded. This is a
# balance hint: a package absent from it takes DEFAULT_WEIGHT and is still owned by exactly
# one partition, so a stale table costs balance and cannot cost coverage. On the live
# 34-package inventory a cost-blind round robin splits 8212/15538/10513/3863 s (4.02x) and
# the rule below splits 9529/9511/9535/9551 s (1.0042x); reproduce either with `--census 4`.
# `sessions=5400` is a bound, not a measurement: it has never finished under -race, it
# exhausted the 90-minute per-binary cap in run 32598987269, and it was still alive when
# this step was cut. `.` and compliance/catalogexport carry no test files.
WEIGHTS=" governance=6292 finops=5284 sessions=5400 compliance=4415 eventing=2791 \
knowledge=2177 models=1672 catalog=1091 evals=1025 access-map=937 orchestration=931 \
capabilities=893 inventory=763 deploy=535 notify=413 health=365 claudeadoption=327 \
consoleviews=280 inferenceproxy=269 security=252 recording=248 observability=204 \
sandbox=134 redteam=98 liveingest=72 reporting=60 posture-export=27 example=1 \
sessioncockpit=1 .=1 compliance/catalogexport=1 "

# The median of the 28 packages that reported in that run (364.774 s and 413.453 s are its
# 14th and 15th values). A new package is assumed median-sized rather than free; free would
# let a batch of new packages land on one partition unopposed.
DEFAULT_WEIGHT=389

# Selector magnitude bound, applied before any arithmetic touches the value. `[ x -gt y ]`
# and $(( )) are signed 64-bit: a wider value makes the range test print "integer expression
# expected" and read as FALSE — letting an unusable selector through — while $(( )) wraps it
# silently. Nine digits keeps every accepted value below 2^31, which is safe as a comparison
# operand, an array subscript and an accumulator.
MAX_SELECTOR_DIGITS=9

# The globals the functions below return through — see the shape constraints above.
SHORT=""
WEIGHT=""
INV=""
SIZE=""
PLACED=""
ASSIGNED=""
SELECTED=""

# ── Discovery ────────────────────────────────────────────────────────────────────────────
# One enumeration, used by every mode. `go list ./...` inside ./modules applies the same
# build constraints `go test ./...` applies, so the union of the partitions is exactly what
# the unpartitioned recipe runs. A tree walk or a glob would be a second definition of "the
# packages", and two definitions of one set is how a package stops being raced unnoticed.
discover() { # -> INV
	local out
	out="$(cd "${ROOT}/modules" && go list ./... 2>/dev/null)" ||
		blind "'go list ./...' failed inside ${ROOT}/modules; the inventory is unknown, so no partition can be honest about what it owns"
	[ -n "${out}" ] ||
		blind "'go list ./...' returned NO package inside ${ROOT}/modules"
	INV="$(printf '%s\n' "${out}" | LC_ALL=C sort -u)"
}

# size_of counts the non-empty lines of an inventory or a selection.
size_of() { # <inventory> -> SIZE
	SIZE="$(printf '%s\n' "$1" | grep -c . || true)"
}

# short_of maps an import path to the cost table's key: what follows the last `/modules/`,
# or `.` for the ./modules package itself.
short_of() { # <import-path> -> SHORT
	local p="$1" s="${1##*/modules/}"
	if [ "${s}" = "${p}" ]; then SHORT="."; else SHORT="${s}"; fi
}

# weight_of is pure parameter expansion: the table is one line and every key is surrounded by
# spaces, so `${WEIGHTS#* key=}` finds the exact key or nothing at all.
#
# The inner expansion stays unquoted, against shellcheck SC2295, and that is measured rather
# than preferred: written as SC2295 asks, `task lint:pg-env` went from 142 passed / 0 failed
# to 89 / 53, because checkpgwiring's word reader tracks quotes across the file and the inner
# quote closed the outer string for it. The pattern risk SC2295 names cannot arise here —
# `*`, `?` and `[` are not legal in a Go import path.
weight_of() { # <import-path> -> WEIGHT
	local tail
	short_of "$1"
	tail="${WEIGHTS#* ${SHORT}=}"
	if [ "${tail}" = "${WEIGHTS}" ]; then
		WEIGHT="${DEFAULT_WEIGHT}"
		return 0
	fi
	WEIGHT="${tail%% *}"
}

# ── The assignment rule, in one place ────────────────────────────────────────────────────
# Longest-processing-time-first: sort by declared cost descending, import path ascending to
# break ties, and give each package to the partition with the smallest load so far, lowest
# index breaking that tie. It depends on the inventory alone — no clock, no hostname, no run
# id — so four jobs computing it independently compute the same answer, and so does a reader
# checking them afterwards. `place` is the only implementation: the selection, the census and
# the union control all read PLACED, because a rule written twice disagrees with itself the
# first time somebody tunes one copy.
place() { # <count>, reads INV -> PLACED, one "<index>:<cost>:<import-path>" per line
	local n="$1" entry p w i best line ordered
	local -a load
	i=1
	while [ "${i}" -le "${n}" ]; do
		load[i]=0
		i=$((i + 1))
	done
	line=""
	while IFS= read -r p; do
		[ -n "${p}" ] || continue
		weight_of "${p}"
		line="${line}${WEIGHT}:${p}"$'\n'
	done <<<"${INV}"
	[ -n "${line}" ] || blind "the inventory handed to the placement was EMPTY; refusing to answer that no package needs racing"
	# `:` separates because an import path cannot contain one. The sort is numeric on the
	# cost and lexical on the path, both under LC_ALL=C, so the answer does not depend on
	# the locale of whoever runs it.
	ordered="$(printf '%s' "${line}" | LC_ALL=C sort -t: -k1,1nr -k2,2)"
	PLACED=""
	while IFS= read -r entry; do
		[ -n "${entry}" ] || continue
		w="${entry%%:*}"
		p="${entry#*:}"
		best=1
		i=2
		while [ "${i}" -le "${n}" ]; do
			if [ "${load[i]}" -lt "${load[best]}" ]; then best="${i}"; fi
			i=$((i + 1))
		done
		load[best]=$((load[best] + w))
		PLACED="${PLACED}${best}:${w}:${p}"$'\n'
	done <<<"${ordered}"
}

# assign collects the import paths of one partition, alphabetically.
assign() { # <index> <count>, reads INV -> ASSIGNED
	local idx="$1" entry out=""
	place "$2"
	while IFS= read -r entry; do
		[ -n "${entry}" ] || continue
		if [ "${entry%%:*}" = "${idx}" ]; then
			out="${out}${entry##*:}"$'\n'
		fi
	done <<<"${PLACED}"
	ASSIGNED="$(printf '%s' "${out}" | LC_ALL=C sort)"
}

# ── Selector validation ──────────────────────────────────────────────────────────────────
# Both halves or neither; decimal digits only; short enough for shell arithmetic; and
# 1 <= index <= count. Every rejection is nonzero and names what it read, because a partition
# job that silently ran everything, or nothing, is indistinguishable from one that worked.
PARTITION_INDEX=""
PARTITION_COUNT=""
read_selectors() {
	local i="${OLIVARES_MODULES_RACE_PARTITION:-}" n="${OLIVARES_MODULES_RACE_PARTITIONS:-}"
	if [ -z "${i}" ] && [ -z "${n}" ]; then
		return 0
	fi
	if [ -z "${i}" ] || [ -z "${n}" ]; then
		blind "the selectors are PAIRED: the index read '${i}' and the count read '${n}' — set both, or neither and get the complete inventory"
	fi
	validate_pair "${i}" "${n}"
	PARTITION_INDEX="${i}"
	PARTITION_COUNT="${n}"
}

# is_decimal: a decimal integer >= 1, with no sign, space, padding or suffix. A leading zero
# is refused too — `08` is a number to a reader and an error to the shell's arithmetic, and a
# selector that means two things is not a selector.
is_decimal() {
	local v="${1:-}" digits
	[ -n "${v}" ] || return 1
	digits="${v//[0-9]/}"
	[ -z "${digits}" ] || return 1
	[ "${v#0}" = "${v}" ] || return 1
	return 0
}

# require_count refuses before the value reaches any arithmetic. The length test comes second
# so a non-decimal value is named as one rather than as an oversized one.
require_count() { # <what> <value>
	is_decimal "${2:-}" || blind "the partition $1 '${2:-}' is not a decimal integer >= 1"
	[ "${#2}" -le "${MAX_SELECTOR_DIGITS}" ] ||
		blind "the partition $1 '${2}' has ${#2} digits, over the ${MAX_SELECTOR_DIGITS}-digit limit: a wider value is not a shell integer, so the range test below would neither accept nor refuse it"
}

validate_pair() { # <index> <count>
	local i="${1:-}" n="${2:-}"
	require_count "COUNT" "${n}"
	require_count "INDEX" "${i}"
	if [ "${i}" -gt "${n}" ]; then
		blind "the partition INDEX ${i} is outside 1..${n}; a partition that does not exist owns no package, and no package is not a pass"
	fi
}

# refuse_empty is the third refusal, and the one that needs the inventory: more partitions
# than packages leaves at least one job with nothing to run. It also bounds every loop in
# this file by the inventory size.
refuse_empty() { # <count> <inventory-size>
	if [ "$2" -lt "$1" ]; then
		fail "$1 partition(s) over $2 package(s) leaves at least one partition EMPTY; an empty 'go test' argv tests the current directory and exits 0"
	fi
}

# DEDICATED OWNER (R111-SR1). One package may be handed to a job of its own — today
# modules/sessions, which consumed 149 of a 210-minute step on its own. The exclusion is
# EXPLICIT and NARROW, for three reasons written as code:
#
#   1. It applies ONLY when a partition selector is set. With no selector this helper still
#      runs the complete original ./modules sweep, so the local recipe never loses a package.
#   2. The value must name the package EXACTLY and that package must be in the inventory. A
#      name that matches nothing is a typo that would silently drop nothing today and the
#      wrong thing tomorrow, so it is refused rather than ignored.
#   3. Removing it from these partitions is only safe because a dedicated job owns it. That
#      wiring is enforced in the workflow and asserted by both selftests; this file cannot
#      check a workflow, so it refuses anything it cannot name exactly.
# The single package R111-SR1 ratified a dedicated owner for.
RATIFIED_DEDICATED="github.com/olivaresai/olivares/modules/sessions"
apply_dedicated() { # reads INV -> INV without the dedicated package
	local want="${OLIVARES_MODULES_RACE_DEDICATED:-}" line out="" found=0
	[ -n "${want}" ] || return 0
	# ONE package has a Root-ratified dedicated owner (R111-SR1: modules/sessions, which has
	# the race-sessions job). Any other value is refused even when it names a real package:
	# existing in the inventory is not the same as having a job that runs it, and excluding a
	# package whose owner does not exist is how a package stops being raced unnoticed.
	[ "${want}" = "${RATIFIED_DEDICATED}" ] ||
		blind "OLIVARES_MODULES_RACE_DEDICATED names '${want}', and the only package with a ratified dedicated owner is '${RATIFIED_DEDICATED}'; excluding anything else would drop a package no job runs"
	while IFS= read -r line; do
		[ -n "${line}" ] || continue
		if [ "${line}" = "${want}" ]; then
			found=1
			continue
		fi
		out="${out}${line}"$'\n'
	done <<<"${INV}"
	[ "${found}" = 1 ] ||
		blind "OLIVARES_MODULES_RACE_DEDICATED names '${want}', which is not in the ./modules inventory; refusing to drop a package by a name nothing matches"
	INV="$(printf '%s' "${out}" | LC_ALL=C sort)"
	say "dedicated owner: '${want}' is excluded from these partitions and MUST be run by its own job"
}

# select_packages collects the packages the current selector owns; unpartitioned that is the
# whole inventory, which is what makes `--packages` readable in both regimes.
select_packages() { # reads the selectors -> SELECTED
	discover
	if [ -z "${PARTITION_COUNT}" ]; then
		# No selector: the complete original sweep, dedicated owner included. The exclusion
		# is a CI parallelism device, never a reduction of what the local recipe runs.
		SELECTED="${INV}"
		return 0
	fi
	apply_dedicated
	size_of "${INV}"
	refuse_empty "${PARTITION_COUNT}" "${SIZE}"
	assign "${PARTITION_INDEX}" "${PARTITION_COUNT}"
	SELECTED="${ASSIGNED}"
}

# ── The union control ────────────────────────────────────────────────────────────────────
# Union, no duplicate, no omission, no empty partition — over whatever inventory it is given,
# so the same code answers for today's tree and for a synthetic future one. The comparison is
# one pass and needs no temporary file: concatenate the inventory with the union of the
# partitions and count. A package owned exactly once appears twice; once means owned by
# nobody (or owned while absent from the inventory) and three or more means owned twice.
check_union() { # <count> <--check|--census> [-]   ('-' = inventory on stdin)
	local n="$1" mode="$2" from="${3:-}" all i count bad
	# The source is declared, never guessed. An earlier draft decided by `[ -t 0 ]` and read
	# an empty stdin every time it ran with its input redirected from /dev/null; a control
	# that refuses to look because nobody piped it anything is a control nobody will run.
	if [ "${from}" = "-" ]; then
		INV="$(cat)"
	elif [ -n "${from}" ]; then
		blind "the only inventory source flag is '-' (read stdin); got '${from}'"
	else
		discover
	fi
	size_of "${INV}"
	[ "${SIZE}" -gt 0 ] || blind "the inventory is empty: there is nothing to prove about it"
	refuse_empty "${n}" "${SIZE}"

	all=""
	i=1
	while [ "${i}" -le "${n}" ]; do
		assign "${i}" "${n}"
		count="$(printf '%s\n' "${ASSIGNED}" | grep -c . || true)"
		[ "${count}" -gt 0 ] || fail "partition ${i} of ${n} owns NO package: its 'go test' argv would carry no package at all"
		all="${all}${ASSIGNED}"$'\n'
		i=$((i + 1))
	done

	bad="$(printf '%s\n%s' "${INV}" "${all}" | grep -v '^$' | LC_ALL=C sort | uniq -c | grep -vE '^[[:space:]]*2[[:space:]]' || true)"
	if [ -n "${bad}" ]; then
		printf 'modules-race-partition: FAIL — the %s partitions are NOT a partition of the inventory.\n' "${n}" >&2
		printf '  A package owned by exactly one partition appears TWICE below; 1 = owned by NOBODY, 3+ = owned TWICE.\n' >&2
		printf '%s\n' "${bad}" >&2
		exit 1
	fi

	if [ "${mode}" = "--census" ]; then
		census "${n}"
	fi
	printf 'modules-race-partition: CLEAN — %s package(s), %s partition(s), every package owned exactly once, no partition empty\n' \
		"${SIZE}" "${n}"
}

# census reports what each partition carries. The cost shown is the table's declared answer,
# not a measurement of this run: it is printed so a reader can see the balance the table
# produces today and re-measure when it stops matching.
census() { # <count>, reads INV
	local n="$1" i l c entry rest
	place "${n}"
	i=1
	while [ "${i}" -le "${n}" ]; do
		l=0
		c=0
		while IFS= read -r entry; do
			[ -n "${entry}" ] || continue
			if [ "${entry%%:*}" = "${i}" ]; then
				rest="${entry#*:}"
				l=$((l + ${rest%%:*}))
				c=$((c + 1))
			fi
		done <<<"${PLACED}"
		printf 'modules-race-partition: partition %s/%s — %s package(s), declared cost %ss\n' \
			"${i}" "${n}" "${c}" "${l}"
		i=$((i + 1))
	done
}

# ── Modes ────────────────────────────────────────────────────────────────────────────────
main() {
	local mode="${1:-}"

	if [ "${mode}" = "--inventory" ]; then
		discover
		printf '%s\n' "${INV}"
		return 0
	fi

	if [ "${mode}" = "--assign" ]; then
		validate_pair "${2:-}" "${3:-}"
		INV="$(cat)"
		[ -n "${INV}" ] || blind "--assign reads the inventory from stdin and got nothing"
		size_of "${INV}"
		refuse_empty "${3:-}" "${SIZE}"
		assign "${2:-}" "${3:-}"
		printf '%s\n' "${ASSIGNED}"
		return 0
	fi

	if [ "${mode}" = "--check" ] || [ "${mode}" = "--census" ]; then
		require_count "COUNT" "${2:-}"
		check_union "${2:-}" "${mode}" "${3:-}"
		return 0
	fi

	read_selectors

	if [ "${mode}" = "--packages" ]; then
		select_packages
		printf '%s\n' "${SELECTED}"
		return 0
	fi

	[ "$#" -gt 0 ] || blind "no argv to run and no mode given; see the usage block at the top of this file"
	if [ "${mode#--}" != "${mode}" ]; then
		blind "unknown mode '${mode}'; see the usage block at the top of this file"
	fi

	# Run mode. Unpartitioned the argv ends in `./...` from ./modules, byte for byte what the
	# recipe ran before this selector existed. Partitioned it ends in the packages this index
	# owns, preceded by `-v -p 1`.
	#
	# `-p 1` is a bounded choice: the runner's CPU allocation is not observable in the failed
	# job's log, which carries no cgroup or nproc dump. What is observable is the width the
	# leg ran at (GOFLAGS='-p=5' in its own `go env` dump) and what that width cost — the
	# per-binary clock is what has killed this leg three times (governance 1800.178s at the
	# 30m cap, sessions 5400.0s at the 90m cap). `-p 1` keeps each binary's clock closest to
	# its package's own cost and cannot overcommit a host that four partitions may share; the
	# accepted ./core split (7a3823420e) chose it on the same ground.
	#
	# `-v` is what makes a partition report named test progress, and it only works paired
	# with `-p 1`: measured on go1.26.6, `go test -p 1 -v` over two packages streams each
	# `=== RUN` / `--- PASS` as it happens, while `go test -p 5 -v` over the same two prints
	# every line in one burst after both finish. That burst is the failed run's pathology —
	# all 28 package results at 02:34:52 after two silent hours — so width hid the progress
	# and width alone does not restore it. No pipeline is introduced: the argv stays the last
	# command of this script, so `go test`'s own status is the status this script exits with.
	# `-json` streams identically and was not chosen, because it needs a consumer between
	# `go test` and the log to stay readable, and that consumer is where a real exit status
	# gets replaced by a formatter's.
	local -a args
	local total p
	args=()
	if [ -n "${PARTITION_COUNT}" ]; then
		select_packages
		size_of "${SELECTED}"
		total="${SIZE}"
		[ "${total}" -gt 0 ] || fail "partition ${PARTITION_INDEX}/${PARTITION_COUNT} owns NO package"
		say "partition ${PARTITION_INDEX} of ${PARTITION_COUNT}: ${total} package(s), -v -p 1"
		args=(-v -p 1)
		while IFS= read -r p; do
			[ -n "${p}" ] || continue
			say "  ${p}"
			args+=("${p}")
		done <<<"${SELECTED}"
	else
		say "no partition selector: the complete ./modules inventory (./...)"
		args=(./...)
	fi

	cd "${ROOT}/modules" && "$@" "${args[@]}"
}

main "$@"
