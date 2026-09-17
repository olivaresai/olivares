#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# core-race-partition.sh — run an argv over the ./core packages, or over one
# deterministic partition of them.
#
# Background: on 2026-09-13 the race-core job (run 34743156449, job 103686207093,
# ci-runner-8) was killed by its 180-minute step ceiling. `core/auth` alone took 2529.5 s,
# `core/internal/store/sqlstore` was still running when the cap fired, and 22 of the 47 core
# test packages sort after it and NEVER RAN. The leg did not hang: it did not fit on one
# runner, and nothing reported the unreached packages at all. This helper adds a selector so
# CI can spend several runners on it. `1db8a2f4` makes the pressure larger, not smaller:
# sqlstore is +2047/-24 and auth +1669/-18 over the measured tree.
#
# Invariant: every package `go list ./...` discovers, except core/internal/store/sqlstore,
# has exactly one owner among the N partitions, including packages added later. sqlstore is
# split by ENTRY POINT across the same N jobs (scripts/sqlstore-race-entry-partition.sh),
# because one package binary does not fit one 90-minute -timeout. The cost table below
# therefore affects balance only, never coverage. scripts/test-core-race-partition.sh proves
# the package complement; scripts/test-sqlstore-race-entry-partition.sh proves the entries.
#
# Run mode — the argv is the command, and it runs from ./core:
#   scripts/core-race-partition.sh go test -race -count=1 -p 1 -timeout 90m
#     no selector  -> `./...`, byte for byte the pre-partition recipe (sqlstore included,
#                     no extra sqlstore invocation and no -run)
#     selector set -> sqlstore entry shard, then `-v -p 1` plus the other packages this
#                     partition owns. If either invocation fails, the other still runs;
#                     the status returned is the sqlstore status if nonzero, otherwise the
#                     package status. An empty or failed selection is never a green skip.
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
#   OLIVARES_CORE_RACE_PARTITION    1-based partition index
#   OLIVARES_CORE_RACE_PARTITIONS   number of partitions
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

say() { printf 'core-race-partition: %s\n' "$*" >&2; }
fail() { printf 'core-race-partition: FAIL — %s\n' "$*" >&2; exit 1; }
blind() { printf 'core-race-partition: COULD NOT LOOK — %s\n' "$*" >&2; exit 2; }

# Declared seconds for the ./core packages. BALANCE HINT ONLY: a package absent from the
# table takes DEFAULT_WEIGHT and is still owned by exactly one partition, so a stale table
# costs balance and can never cost coverage. One remaining package-level measurement:
#
#   auth=2529   MEASURED. core/auth reported 2529.5 s in job 103686207093.
#
# sqlstore is NOT in this table: partitioned run mode removes it from package placement and
# hands its Test/Example/Fuzz entries to scripts/sqlstore-race-entry-partition.sh. A stale
# weight here could not cost coverage; omitting it keeps the table from pretending a package
# that is no longer placed still has a package-level duration.
#
# Every other package is unmeasured under -race and takes DEFAULT_WEIGHT. Reproduce the split
# this table produces with `--census <n>`.
WEIGHTS=" auth=2529 "

# The weight an unmeasured package carries. Deliberately not 0 or 1: free would let a batch of
# new or unmeasured packages land on one partition unopposed, which is the same shape of
# failure an unfinished heavyweight causes. 389 carries over from the modules helper so the two
# reason on one scale; NO core package has been measured at that value. It is a placeholder for a
# measurement, not a claim about any core package.
DEFAULT_WEIGHT=389

MAX_SELECTOR_DIGITS=9

# The globals the functions below return through — see the shape constraints above.
SHORT=""
WEIGHT=""
INV=""
SIZE=""
PLACED=""
ASSIGNED=""
SELECTED=""

# The one ./core package that is split by entry point, not by package. Matched by the
# canonical import path and by the suffix, so a synthetic module path in a control still
# excludes the same package the live inventory names.
SQLSTORE_PKG="github.com/olivaresai/olivares/core/internal/store/sqlstore"

is_sqlstore_pkg() { # <import-path>
	local p="$1" prefix
	[ "${p}" = "${SQLSTORE_PKG}" ] && return 0
	# Suffix match without a `case` glob: checkpgwiring's walker would otherwise treat
	# `*/internal/store/sqlstore` as an unknown command head (UNVERIFIED).
	prefix="${p%/internal/store/sqlstore}"
	[ "${prefix}" != "${p}" ] && [ "${p}" = "${prefix}/internal/store/sqlstore" ]
}

# ── Discovery ────────────────────────────────────────────────────────────────────────────
# One enumeration, used by every mode. `go list ./...` inside ./core applies the same
# build constraints `go test ./...` applies, so the union of the partitions is exactly what
# the unpartitioned recipe runs. A tree walk or a glob would be a second definition of "the
# packages", and two definitions of one set is how a package stops being raced unnoticed.
discover_go_list() { # <Go executable> list [build flags] -> INV
	local out
	out="$(cd "${ROOT}/core" && "$@" ./... 2>/dev/null)" ||
		blind "'go list ./...' failed inside ${ROOT}/core; the inventory is unknown, so no partition can be honest about what it owns"
	[ -n "${out}" ] ||
		blind "'go list ./...' returned NO package inside ${ROOT}/core"
	INV="$(printf '%s\n' "${out}" | LC_ALL=C sort -u)"
}

discover() { # [validated caller go-test argv] -> INV
	if [ "$#" = 0 ]; then
		discover_go_list go list
		return
	fi
	# The entry helper's closed preflight has already validated every word. Retain all
	# supported build flags for package discovery too; omit only its three test-only
	# flags. In particular a package enabled by -tags must have a package owner.
	local executable="$1" word key
	local -a flags=()
	shift 2
	while [ "$#" -gt 0 ]; do
		word="$1"
		shift
		if [ "${word#-}" = "${word}" ]; then
			flags+=("${word}")
			continue
		fi
		key="${word#-}"
		key="${key#-}"
		key="${key%%=*}"
		if [ "${key}" = count ] || [ "${key}" = timeout ]; then
			if [ "${word#*=}" = "${word}" ]; then shift; fi
		elif [ "${key}" != v ]; then
			flags+=("${word}")
		fi
	done
	discover_go_list "${executable}" list "${flags[@]}"
}

# size_of counts the non-empty lines of an inventory or a selection.
size_of() { # <inventory> -> SIZE
	SIZE="$(printf '%s\n' "$1" | grep -c . || true)"
}

# short_of maps an import path to the cost table's key: what follows the last `/core/`,
# or `.` for the ./core package itself.
short_of() { # <import-path> -> SHORT
	local p="$1" s="${1##*/core/}"
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
	local i="${OLIVARES_CORE_RACE_PARTITION:-}" n="${OLIVARES_CORE_RACE_PARTITIONS:-}"
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

# exclude_sqlstore removes the entry-partitioned package from INV. Required on the live
# inventory: a missing sqlstore would mean the four jobs ran other packages and nobody ran
# sqlstore. Optional on a synthetic stdin inventory, which may not name it.
exclude_sqlstore() { # <required 0|1>, reads INV -> INV
	local required="$1" line out="" found=0
	while IFS= read -r line; do
		[ -n "${line}" ] || continue
		if is_sqlstore_pkg "${line}"; then
			found=1
			continue
		fi
		out="${out}${line}"$'\n'
	done <<<"${INV}"
	if [ "${found}" != 1 ]; then
		if [ "${required}" = 1 ]; then
			blind "the ./core inventory does not contain ${SQLSTORE_PKG}; refusing to partition sqlstore entries of a package that is not raced"
		fi
		return 0
	fi
	INV="$(printf '%s' "${out}" | LC_ALL=C sort)"
}

# select_packages collects the packages the current selector owns; unpartitioned that is the
# whole inventory, which is what makes `--packages` readable in both regimes. Partitioned it
# is the inventory WITHOUT sqlstore: that package is raced by the entry helper instead.
select_packages() { # [validated caller go-test argv], reads the selectors -> SELECTED
	discover "$@"
	if [ -z "${PARTITION_COUNT}" ]; then
		SELECTED="${INV}"
		return 0
	fi
	exclude_sqlstore 1
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
	# Stdin inventories used by the battery may omit sqlstore; the live inventory must
	# contain it, and then it is excluded so --check describes the package complement.
	if [ "${from}" = "-" ]; then
		exclude_sqlstore 0
	else
		exclude_sqlstore 1
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
		printf 'core-race-partition: FAIL — the %s partitions are NOT a partition of the inventory.\n' "${n}" >&2
		printf '  A package owned by exactly one partition appears TWICE below; 1 = owned by NOBODY, 3+ = owned TWICE.\n' >&2
		printf '%s\n' "${bad}" >&2
		exit 1
	fi

	if [ "${mode}" = "--census" ]; then
		census "${n}"
	fi
	printf 'core-race-partition: CLEAN — %s package(s), %s partition(s), every non-sqlstore package owned exactly once, no partition empty; sqlstore is entry-partitioned\n' \
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
		printf 'core-race-partition: partition %s/%s — %s package(s), declared cost %ss\n' \
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

	# Run mode. Unpartitioned the argv ends in `./...` from ./core, byte for byte what the
	# recipe ran before this selector existed. Partitioned it is TWO invocations of the same
	# argv: the sqlstore entry shard first, then `-v -p 1` plus the other packages this
	# index owns. Either failure still runs the other owned work. The status is the sqlstore
	# status if nonzero, otherwise the package status, so a later 0 cannot hide an earlier
	# red. Both write to this process's stdout/stderr, which the race-core step tees.
	#
	# `-p 1` is a bounded choice: the runner's CPU allocation is not observable in the failed
	# job's log, which carries no cgroup or nproc dump. What is observable is the width the
	# leg ran at (GOFLAGS='-p=5' in its own `go env` dump) and what that width cost — the
	# per-binary clock is what kills a leg of this shape. The three figures that showed it —
	# governance 1800.178s at a 30m cap, sessions 5400.0s at a 90m cap — are the MODULES leg's
	# measurements, cited as the mechanism, not as core numbers: no core package has been
	# measured under -race here. `-p 1` keeps each binary's clock closest to
	# its package's own cost and cannot overcommit a host that four partitions may share; the
	# accepted ./core split (7a3823420e) chose it on the same ground.
	#
	# `-v` is what makes a partition report named test progress, and it only works paired
	# with `-p 1`: measured on go1.26.6, `go test -p 1 -v` over two packages streams each
	# `=== RUN` / `--- PASS` as it happens, while `go test -p 5 -v` over the same two prints
	# every line in one burst after both finish. That burst is the failed run's pathology —
	# all 28 package results at 02:34:52 after two silent hours — so width hid the progress
	# and width alone does not restore it. No pipeline is introduced on either invocation, so
	# `go test`'s own status is the status that invocation contributes. `-json` streams
	# identically and was not chosen, because it needs a consumer between `go test` and the
	# log to stay readable, and that consumer is where a real exit status gets replaced by a
	# formatter's.
	local -a args
	local total p sql_rc=0 pkg_rc=0
	args=()
	if [ -n "${PARTITION_COUNT}" ]; then
		# Refuse selection overrides before either leg can run, including when discovery
		# would otherwise fail first. The validator also reads this Go's effective GOFLAGS.
		cd "${ROOT}" || blind "cannot enter ${ROOT}"
		bash scripts/sqlstore-race-entry-partition.sh --validate-argv "$@" || return $?
		select_packages "$@"
		size_of "${SELECTED}"
		total="${SIZE}"
		[ "${total}" -gt 0 ] || fail "partition ${PARTITION_INDEX}/${PARTITION_COUNT} owns NO package"
		say "partition ${PARTITION_INDEX} of ${PARTITION_COUNT}: ${total} package(s) plus the sqlstore entry shard, -v -p 1"
		args=(-v -p 1)
		while IFS= read -r p; do
			[ -n "${p}" ] || continue
			say "  ${p}"
			args+=("${p}")
		done <<<"${SELECTED}"

		cd "${ROOT}" || blind "cannot enter ${ROOT}"
		# Literal relative path so checkpgwiring can resolve this argvRunnerScripts entry.
		# The helper is a separate process; a failure here must not skip the package argv.
		bash scripts/sqlstore-race-entry-partition.sh "$@" || sql_rc=$?
		cd "${ROOT}/core" && "$@" "${args[@]}" || pkg_rc=$?
		if [ "${sql_rc}" != 0 ]; then
			say "sqlstore entry partition exited ${sql_rc}"
		fi
		if [ "${pkg_rc}" != 0 ]; then
			say "package partition exited ${pkg_rc}"
		fi
		if [ "${sql_rc}" != 0 ]; then
			exit "${sql_rc}"
		fi
		exit "${pkg_rc}"
	fi

	say "no partition selector: the complete ./core inventory (./...)"
	cd "${ROOT}/core" && "$@" ./...
}

main "$@"
