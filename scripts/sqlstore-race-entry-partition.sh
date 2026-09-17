#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# sqlstore-race-entry-partition.sh — run core/internal/store/sqlstore, or one deterministic
# partition of its top-level entry points.
#
# Background: the 2026-09-14 race-core package timeout (CI 875, job 104010701828) spent
# 5361.37 s completing 652 serial sqlstore entries, then interrupted the next after 38 s;
# 498 entries had not started. That is aggregate package runtime under one 90-minute
# -timeout, not a hang in the interrupted test. Package placement cannot split one binary,
# so this helper partitions by ENTRY POINT. scripts/core-race-partition.sh invokes it from
# the same four core jobs when the selector pair is set, and keeps sqlstore out of the
# ordinary package argv so the package is never raced whole and in shards in the same job.
#
# Discovery uses the caller's Go executable and validated test flags, including build tags,
# with the SAME effective GOFLAGS as execution. `-list` enumerates
# TOP-LEVEL entries only; selecting an anchored top-level name retains all of its subtests,
# so no descendant is lost. Test, Example and Fuzz entries are all runnable and all owned;
# Benchmarks are not part of the default run and are not selected. A Fuzz entry selected by
# name runs its seed corpus exactly as the unpartitioned run does.
#
# Run mode — the argv is the command, and it runs from ./core:
#   scripts/sqlstore-race-entry-partition.sh go test -race -count=1 -p 1 -timeout 90m
#     no selector  -> ./internal/store/sqlstore with NO -run: the complete package run
#     selector set -> `-v -p 1 -run '^(A|B|...)$' ./internal/store/sqlstore`
#
# The regex is anchored at BOTH ends and that is load-bearing: Go matches an unanchored -run
# pattern as a substring, so a bare alternation would make one partition run another's tests
# and report them twice.
#
# Inspection modes, for the controls and for an operator:
#   --inventory         the entry points the compiled binary declares
#   --names             the entries the current selector owns
#   --regex             the anchored -run regex the current selector would pass
#   --assign <i> <n>    stdin inventory -> the entries of partition i of n
#   --check <n> [-]     union / no duplicate / no empty partition
#   --census <n> [-]    --check plus the per-partition entry count
#   --validate-argv <go> test [flags]  preflight only, before either core execution leg
# Inspection discovery defaults to `go test -race -count=1`.
#
# Partitioned argv is deliberately closed: -race, -trimpath, -tags, -mod, -modfile,
# -p, -count=1, -timeout (positive integer ms/s/m/h), and -v. One or two leading
# hyphens and =value are supported. No package, selection, runtime-filter, output,
# fuzzing, benchmark, compile-only or unknown flag is admitted. Effective GOFLAGS
# permits only those build flags, in unquoted space-separated -flag=value form.
# Unsupported input is exit 2 before discovery; it is never a coverage success.
#
# Selectors, set both or neither. They are the SAME pair the core jobs already export, so this
# helper does not introduce a second matrix or assign any OLIVARES_* variable of its own:
#   OLIVARES_CORE_RACE_PARTITION    1-based partition index
#   OLIVARES_CORE_RACE_PARTITIONS   number of partitions
#
# Balance is pure ENTRY COUNT. That is an explicitly UNMEASURED scheduling choice: completed
# CI timings cover only part of the inventory, so this helper predicts no wall time and claims
# no speed-up. It divides work deterministically; what that costs is a CI measurement.
#
# Exit codes:
#   0  the selection was made, or the argv ran and its status is propagated
#   1  a property was violated: an empty partition, or an entry with no owner or with two
#   2  an input could not be read: a malformed, oversized or out-of-range selector, half a
#      pair, an unknown mode, or a discovery that failed or returned nothing
# A DISCOVERY OR BUILD failure is exit 2 and says so: it is "I could not look", never "there
# is nothing to run". A selector that cannot be read never degrades to running everything,
# and never to running nothing.
#
# Shape constraints, inherited from the reviewed core/sessions helpers because this file is a
# checkpgwiring argvRunnerScripts entry too: no sed, awk, python3, here-document or process
# substitution, and no `$(local_function ...)`. Every function returns through a named global.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="core"
PKG="./internal/store/sqlstore"

say() { printf 'sqlstore-race-entry-partition: %s\n' "$*" >&2; }
fail() { printf 'sqlstore-race-entry-partition: FAIL — %s\n' "$*" >&2; exit 1; }
blind() { printf 'sqlstore-race-entry-partition: COULD NOT LOOK — %s\n' "$*" >&2; exit 2; }

MAX_SELECTOR_DIGITS=9

INV=""
SIZE=""
PLACED=""
ASSIGNED=""
SELECTED=""
REGEX=""
EFFECTIVE_GOFLAGS=""

# This helper owns selection. Forwarding an arbitrary go-test argv used to let -skip
# erase a complete shard and -tags compile entries the hardcoded discovery never saw.
# Validate before compiling, then pass the original supported argv to BOTH commands.
validate_flags() { # <argv|GOFLAGS> [flags]
	local source="$1" word key value has_value
	shift
	while [ "$#" -gt 0 ]; do
		word="$1"
		shift
		[ "${word#-}" != "${word}" ] || blind "unsupported ${source} positional argument; this helper owns the packages"
		key="${word#-}"
		key="${key#-}"
		value="${key#*=}"
		has_value=0
		if [ "${value}" != "${key}" ]; then has_value=1; fi
		key="${key%%=*}"
		if [ "${key}" = race ] || [ "${key}" = trimpath ] || { [ "${key}" = v ] && [ "${source}" = argv ]; }; then
			if [ "${has_value}" = 1 ] && [ "${value}" != true ] && [ "${value}" != false ]; then
				blind "unsupported ${source} boolean value for -${key}"
			fi
		elif [ "${key}" = tags ] || [ "${key}" = mod ] || [ "${key}" = modfile ] || [ "${key}" = p ] ||
			{ [ "${source}" = argv ] && { [ "${key}" = count ] || [ "${key}" = timeout ]; }; }; then
			if [ "${has_value}" = 0 ]; then
				[ "${source}" = argv ] && [ "$#" -gt 0 ] || blind "unsupported ${source} missing value for -${key}"
				value="$1"
				shift
			fi
			[ -n "${value}" ] && [ "${value#-}" = "${value}" ] || blind "unsupported ${source} value for -${key}"
			if [ "${key}" = count ] && [ "${value}" != 1 ]; then
				blind "unsupported ${source} -count; every owned entry must execute exactly once"
			elif [ "${key}" = p ] && ! is_decimal "${value}"; then
				blind "unsupported ${source} -p; expected a positive integer"
			elif [ "${key}" = timeout ] && ! printf '%s\n' "${value}" | grep -E '^[1-9][0-9]*(ms|s|m|h)$' >/dev/null; then
				blind "unsupported ${source} -timeout; expected a positive integer with ms/s/m/h unit"
			fi
		else
			blind "unsupported ${source} flag -${key}; partitioned execution owns selection and accepts only the documented flags"
		fi
	done
}

read_effective_flags() { # <selected Go executable> -> EFFECTIVE_GOFLAGS
	EFFECTIVE_GOFLAGS="$(cd "${ROOT}/${WORKDIR}" && "$@" env GOFLAGS 2>/dev/null)" ||
		blind "cannot read effective GOFLAGS from the selected Go executable"
}

validate_argv() { # <selected Go executable> test [supported flags]
	[ "$#" -ge 2 ] && [ "${1##*/}" = go ] && [ "$2" = test ] ||
		blind "unsupported partitioned command; expected a Go executable named go followed by test"
	local executable="$1" plain
	shift 2
	validate_flags argv "$@"
	read_effective_flags "${executable}"
	# Go's quoted.Split grammar is intentionally not reimplemented here. Reject its quoted
	# forms explicitly; never split a hidden second line or evaluate shell text.
	plain="${EFFECTIVE_GOFLAGS//\"/}"
	plain="${plain//\'/}"
	plain="${plain//\\/}"
	plain="${plain//$'\n'/}"
	plain="${plain//$'\r'/}"
	plain="${plain//$'\t'/}"
	if [ "${plain}" != "${EFFECTIVE_GOFLAGS}" ]; then
		blind "unsupported GOFLAGS quoting or control whitespace; use unquoted space-separated build flags"
	fi
	local -a flags=()
	IFS=' ' read -r -a flags <<<"${EFFECTIVE_GOFLAGS}"
	validate_flags GOFLAGS "${flags[@]}"
}

# ── Discovery ────────────────────────────────────────────────────────────────────────────
# `-list` compiles the test binary with the same flags the run uses and asks IT for its
# entries, so a name added by a build tag this configuration does not set is correctly
# absent. A failure here is a BUILD or DISCOVERY failure and is reported as one: the caller
# must not read it as an empty suite.
discover() { # [caller go-test argv] -> INV
	local out rc=0
	if [ "$#" = 0 ]; then set -- go test -race -count=1; fi
	validate_argv "$@"
	# The build/discovery failure and the empty-result case are SEPARATE answers, and the
	# filter below must not decide either of them. With a `go` that exits 0 and prints no
	# entry, `INV="$(... | grep ...)"` made grep return 1, `set -euo pipefail` aborted the
	# assignment, and the script exited 1 with NO diagnostic at all. `|| true` keeps the
	# no-match case in this function's hands, where it is named.
	out="$(cd "${ROOT}/${WORKDIR}" && "$@" -v -p 1 -list '.*' "${PKG}" 2>&1)" || rc=$?
	if [ "${rc}" != 0 ]; then
		blind "DISCOVERY/BUILD FAILED for ${PKG} with the caller's flags (exit ${rc}); the entry points are unknown, so no partition can be honest about what it owns. Output: ${out}"
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
	local i="${OLIVARES_CORE_RACE_PARTITION:-}" n="${OLIVARES_CORE_RACE_PARTITIONS:-}"
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

select_names() { # [caller go-test argv], reads the selectors -> SELECTED
	discover "$@"
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
		printf 'sqlstore-race-entry-partition: FAIL — the %s partitions are NOT a partition of the inventory.\n' "${n}" >&2
		printf '  An entry owned by exactly one partition appears TWICE below; 1 = owned by NOBODY, 3+ = owned TWICE.\n' >&2
		printf '%s\n' "${bad}" >&2
		exit 1
	fi

	if [ "${mode}" = "--census" ]; then
		census "${n}"
	fi
	printf 'sqlstore-race-entry-partition: CLEAN — %s entry point(s), %s partition(s), every entry owned exactly once, no partition empty, every selection anchored\n' \
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
		printf 'sqlstore-race-entry-partition: partition %s/%s — %s entry point(s) (count balance only; no entry has been timed)\n' \
			"${i}" "${n}" "${c}"
		i=$((i + 1))
	done
}

# ── Modes ────────────────────────────────────────────────────────────────────────────────
main() {
	local mode="${1:-}"
	if [ "${mode}" = --validate-argv ]; then
		shift
		validate_argv "$@"
		return 0
	fi

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
		select_names "$@"
		size_of "${SELECTED}"
		[ "${SIZE}" -gt 0 ] || fail "partition ${PARTITION_INDEX}/${PARTITION_COUNT} owns NO entry point"
		regex_of "${SELECTED}"
		say "partition ${PARTITION_INDEX} of ${PARTITION_COUNT}: ${SIZE} entry point(s), -v -p 1, anchored -run"
		args=(-v -p 1 -run "${REGEX}" "${PKG}")
	else
		say "no partition selector: the complete ${PKG} package (no -run)"
		args=("${PKG}")
	fi
	cd "${ROOT}/${WORKDIR}" && "$@" "${args[@]}"
}

main "$@"
