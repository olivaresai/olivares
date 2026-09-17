#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# sessions-race-partition.sh — run the modules/sessions race suite, or one deterministic
# partition of its top-level entry points.
#
# Background: job 103686207033 consumed its 210 minutes with modules/sessions still active
# after 149 of them and NO assertion or race failure. One package does not fit one runner.
# This helper splits that package by ENTRY POINT, not by package, because there is only one
# package to split.
#
# Discovery is `go test -race -count=1 -list` under the SAME build and race configuration the
# run uses, so the names are the ones the compiled binary actually has. `-list` enumerates
# TOP-LEVEL entries only; selecting an anchored top-level name retains all of its subtests,
# so no descendant is lost. Test, Example and Fuzz entries are all runnable and all owned;
# Benchmarks are not part of the default run and are not selected. A Fuzz entry selected by
# name runs its seed corpus exactly as the unpartitioned run does.
#
# Run mode — the argv is the command, and it runs from ./modules:
#   scripts/sessions-race-partition.sh go test -race -count=1 -timeout 150m
#     no selector  -> ./sessions with NO -run: byte for byte the complete original package run
#     selector set -> `-v -p 1 -run '^(A|B|...)$' ./sessions`
#
# The regex is anchored at BOTH ends and that is load-bearing: this package has 8 names that
# are prefixes of another (TestSessionsStream and TestSessionsStreamConfinement among them),
# and Go matches an unanchored -run pattern as a substring, so a bare alternation would make
# one partition run another's tests and report them twice.
#
# Inspection modes, for the controls and for an operator:
#   --inventory         the entry points the compiled binary declares
#   --names             the entries the current selector owns
#   --regex             the anchored -run regex the current selector would pass
#   --assign <i> <n>    stdin inventory -> the entries of partition i of n
#   --check <n> [-]     union / no duplicate / no empty partition / no prefix ambiguity
#   --census <n> [-]    --check plus the per-partition entry count
#
# Selectors, set both or neither:
#   OLIVARES_SESSIONS_RACE_PARTITION    1-based partition index
#   OLIVARES_SESSIONS_RACE_PARTITIONS   number of partitions
#
# Balance is pure ENTRY COUNT. That is an explicitly UNMEASURED scheduling choice: no entry
# point of this package has been timed, so this helper predicts no wall time and claims no
# speed-up. It divides work deterministically; what that costs is a CI measurement.
#
# Exit codes:
#   0  the selection was made, or the argv ran and its status is propagated
#   1  a property was violated: an empty partition, an entry with no owner or with two, or a
#      selection whose anchored regex would still be ambiguous
#   2  an input could not be read: a malformed, oversized or out-of-range selector, half a
#      pair, an unknown mode, or a discovery that failed or returned nothing
# A DISCOVERY OR BUILD failure is exit 2 and says so: it is "I could not look", never "there
# is nothing to run". A selector that cannot be read never degrades to running everything,
# and never to running nothing.
#
# Shape constraints, inherited from the reviewed modules/core helpers because this file is a
# checkpgwiring argvRunnerScripts entry too: no sed, awk, python3, here-document or process
# substitution, and no `$(local_function ...)`. Every function returns through a named global.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PKG="./sessions"

say() { printf 'sessions-race-partition: %s\n' "$*" >&2; }
fail() { printf 'sessions-race-partition: FAIL — %s\n' "$*" >&2; exit 1; }
blind() { printf 'sessions-race-partition: COULD NOT LOOK — %s\n' "$*" >&2; exit 2; }

MAX_SELECTOR_DIGITS=9

INV=""
SIZE=""
PLACED=""
ASSIGNED=""
SELECTED=""
REGEX=""

# ── Discovery ────────────────────────────────────────────────────────────────────────────
# `-list` compiles the test binary with the same flags the run uses and asks IT for its
# entries, so a name added by a build tag this configuration does not set is correctly
# absent. A failure here is a BUILD or DISCOVERY failure and is reported as one: the caller
# must not read it as an empty suite.
discover() { # -> INV
	local out rc=0
	# The build/discovery failure and the empty-result case are SEPARATE answers, and the
	# filter below must not decide either of them. Root reproduced the defect this shape
	# fixes: with a `go` that exits 0 and prints no entry, `INV="$(... | grep ...)"` made grep
	# return 1, `set -euo pipefail` aborted the assignment, and the script exited 1 with NO
	# diagnostic at all — the one outcome a discovery helper must never produce, because a
	# caller cannot tell it from an ordinary failure. `|| true` keeps the no-match case in
	# this function's hands, where it is named.
	out="$(cd "${ROOT}/modules" && go test -race -count=1 -list '.*' "${PKG}" 2>&1)" || rc=$?
	if [ "${rc}" != 0 ]; then
		blind "DISCOVERY/BUILD FAILED for ${PKG} under -race (exit ${rc}); the entry points are unknown, so no partition can be honest about what it owns. Output: ${out}"
	fi
	INV="$(printf '%s\n' "${out}" | grep -E '^(Test|Example|Fuzz)' | LC_ALL=C sort -u || true)"
	[ -n "${INV}" ] ||
		blind "'go test -list' SUCCEEDED for ${PKG} but named NO Test/Example/Fuzz entry; refusing to answer that the package has nothing to run. Output: ${out}"
}

size_of() { # <text> -> SIZE
	SIZE="$(printf '%s\n' "$1" | grep -c . || true)"
}

# ── The assignment rule ──────────────────────────────────────────────────────────────────
# Round robin over the sorted inventory. It depends on the inventory alone — no clock, host
# or run id — so every job and every later reader computes the same answer, and a new entry
# point is owned automatically by whichever index its sorted position falls on.
place() { # <count>, reads INV -> PLACED, one "<index>:<name>" per line
	local n="$1" name i=0 idx
	PLACED=""
	while IFS= read -r name; do
		[ -n "${name}" ] || continue
		idx=$((i % n + 1))
		PLACED="${PLACED}${idx}:${name}"$'\n'
		i=$((i + 1))
	done <<<"${INV}"
	[ -n "${PLACED}" ] || blind "the inventory handed to the placement was EMPTY"
}

assign() { # <index> <count>, reads INV -> ASSIGNED
	local idx="$1" entry out=""
	place "$2"
	while IFS= read -r entry; do
		[ -n "${entry}" ] || continue
		if [ "${entry%%:*}" = "${idx}" ]; then
			out="${out}${entry#*:}"$'\n'
		fi
	done <<<"${PLACED}"
	ASSIGNED="$(printf '%s' "${out}" | LC_ALL=C sort)"
}

# regex_of builds the anchored alternation. Both anchors, every time.
regex_of() { # <names> -> REGEX
	local name body=""
	while IFS= read -r name; do
		[ -n "${name}" ] || continue
		if [ -z "${body}" ]; then body="${name}"; else body="${body}|${name}"; fi
	done <<<"$1"
	[ -n "${body}" ] || fail "no entry point to select: an empty -run would run the whole package and report it as this partition's"
	REGEX="^(${body})\$"
}

# ── Selector validation ──────────────────────────────────────────────────────────────────
PARTITION_INDEX=""
PARTITION_COUNT=""
read_selectors() {
	local i="${OLIVARES_SESSIONS_RACE_PARTITION:-}" n="${OLIVARES_SESSIONS_RACE_PARTITIONS:-}"
	if [ -z "${i}" ] && [ -z "${n}" ]; then
		return 0
	fi
	if [ -z "${i}" ] || [ -z "${n}" ]; then
		blind "the selectors are PAIRED: the index read '${i}' and the count read '${n}' — set both, or neither and get the complete package run"
	fi
	validate_pair "${i}" "${n}"
	PARTITION_INDEX="${i}"
	PARTITION_COUNT="${n}"
}

is_decimal() {
	local v="${1:-}" digits
	[ -n "${v}" ] || return 1
	digits="${v//[0-9]/}"
	[ -z "${digits}" ] || return 1
	[ "${v#0}" = "${v}" ] || return 1
	return 0
}

require_count() { # <what> <value>
	is_decimal "${2:-}" || blind "the partition $1 '${2:-}' is not a decimal integer >= 1"
	[ "${#2}" -le "${MAX_SELECTOR_DIGITS}" ] ||
		blind "the partition $1 '${2}' has ${#2} digits, over the ${MAX_SELECTOR_DIGITS}-digit limit"
}

validate_pair() { # <index> <count>
	local i="${1:-}" n="${2:-}"
	require_count "COUNT" "${n}"
	require_count "INDEX" "${i}"
	if [ "${i}" -gt "${n}" ]; then
		blind "the partition INDEX ${i} is outside 1..${n}; a partition that does not exist owns no entry, and no entry is not a pass"
	fi
}

refuse_empty() { # <count> <inventory-size>
	if [ "$2" -lt "$1" ]; then
		fail "$1 partition(s) over $2 entry point(s) leaves at least one partition EMPTY"
	fi
}

select_names() { # reads the selectors -> SELECTED
	discover
	if [ -z "${PARTITION_COUNT}" ]; then
		SELECTED="${INV}"
		return 0
	fi
	size_of "${INV}"
	refuse_empty "${PARTITION_COUNT}" "${SIZE}"
	assign "${PARTITION_INDEX}" "${PARTITION_COUNT}"
	SELECTED="${ASSIGNED}"
}

# ── The union control ────────────────────────────────────────────────────────────────────
check_union() { # <count> <--check|--census> [-]
	local n="$1" mode="$2" from="${3:-}" all i count bad
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
		[ "${count}" -gt 0 ] || fail "partition ${i} of ${n} owns NO entry point"
		all="${all}${ASSIGNED}"$'\n'
		i=$((i + 1))
	done

	bad="$(printf '%s\n%s' "${INV}" "${all}" | grep -v '^$' | LC_ALL=C sort | uniq -c | grep -vE '^[[:space:]]*2[[:space:]]' || true)"
	if [ -n "${bad}" ]; then
		printf 'sessions-race-partition: FAIL — the %s partitions are NOT a partition of the inventory.\n' "${n}" >&2
		printf '  An entry owned by exactly one partition appears TWICE below; 1 = owned by NOBODY, 3+ = owned TWICE.\n' >&2
		printf '%s\n' "${bad}" >&2
		exit 1
	fi

	if [ "${mode}" = "--census" ]; then
		census "${n}"
	fi
	printf 'sessions-race-partition: CLEAN — %s entry point(s), %s partition(s), every entry owned exactly once, no partition empty, every selection anchored\n' \
		"${SIZE}" "${n}"
}

census() { # <count>, reads INV
	local n="$1" i c entry
	place "${n}"
	i=1
	while [ "${i}" -le "${n}" ]; do
		c=0
		while IFS= read -r entry; do
			[ -n "${entry}" ] || continue
			if [ "${entry%%:*}" = "${i}" ]; then c=$((c + 1)); fi
		done <<<"${PLACED}"
		printf 'sessions-race-partition: partition %s/%s — %s entry point(s) (count balance only; no entry has been timed)\n' \
			"${i}" "${n}" "${c}"
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

	if [ "${mode}" = "--names" ]; then
		select_names
		printf '%s\n' "${SELECTED}"
		return 0
	fi

	if [ "${mode}" = "--regex" ]; then
		select_names
		regex_of "${SELECTED}"
		printf '%s\n' "${REGEX}"
		return 0
	fi

	[ "$#" -gt 0 ] || blind "no argv to run and no mode given; see the usage block at the top of this file"
	if [ "${mode#--}" != "${mode}" ]; then
		blind "unknown mode '${mode}'; see the usage block at the top of this file"
	fi

	local -a args
	args=()
	if [ -n "${PARTITION_COUNT}" ]; then
		select_names
		size_of "${SELECTED}"
		[ "${SIZE}" -gt 0 ] || fail "partition ${PARTITION_INDEX}/${PARTITION_COUNT} owns NO entry point"
		regex_of "${SELECTED}"
		say "partition ${PARTITION_INDEX} of ${PARTITION_COUNT}: ${SIZE} entry point(s), -v -p 1, anchored -run"
		args=(-v -p 1 -run "${REGEX}" "${PKG}")
	else
		say "no partition selector: the complete ${PKG} package (no -run)"
		args=("${PKG}")
	fi
	cd "${ROOT}/modules" && "$@" "${args[@]}"
}

main "$@"
