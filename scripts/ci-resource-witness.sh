#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ci-resource-witness.sh — the three numbers a job that dies MUTE never printed.
#
# WHY IT EXISTS, measured 2026-09-19. Three hosted jobs died inside the functional suite
# with `The runner has received a shutdown signal` and exit 143 after 19, 29 and 45 minutes
# of silence. The log carried the two error lines and nothing else: no memory figure, no
# process count, no free space — so every diagnosis had to start by REPRODUCING the run
# instead of by reading it. A death that carries no number costs a session; a death that
# carries three costs a paragraph.
#
# WHAT IT OBSERVES. Successful samples update a state file with the highest observed
# host memory use and process count, and the lowest observed free disk space. Sampling
# can miss intervening peaks. `report` reads the last complete file, not the sampler.
# Reporting still requires the shell, state file and log transport to remain available;
# an EXIT/TERM trap cannot guarantee a report after SIGKILL or host loss. No report means
# the resource state is unknown, not that memory exhaustion was established or excluded.
#
# WHAT IT IS NOT. Memory is host MemTotal - MemAvailable, not process RSS or Go-managed
# memory. It includes the runner and other workloads. The witness does not decide the
# test verdict; explicit unavailable-data paths print COULD NOT LOOK and return zero.
#
#   start <state-file> [interval-seconds] [directory]   sample until killed; print the pid
#   report <state-file>                                 print the maxima, or say there are none
set -euo pipefail

cannot() { printf 'resource witness: COULD NOT LOOK — %s\n' "$*" >&2; }

# One sample, printed as four whitespace-separated fields:
#   used-memory-KiB  total-memory-KiB  process-count  free-disk-KiB  total-disk-KiB
#
# Memory in USE is MemTotal - MemAvailable: MemFree alone calls the page cache "used" and
# would report a machine with 12 GiB of cache as exhausted when it is idle.
# The process count comes from a glob over /proc and not from `ps`, so it costs no fork.
sample() {
	local dir="$1" mem_total mem_avail procs disk_free disk_total
	mem_total="$(awk '/^MemTotal:/ {print $2; exit}' /proc/meminfo)" || return 1
	mem_avail="$(awk '/^MemAvailable:/ {print $2; exit}' /proc/meminfo)" || return 1
	[ -n "${mem_total}" ] && [ -n "${mem_avail}" ] || return 1
	local pids=(/proc/[0-9]*)
	procs="${#pids[@]}"
	# Two fields from one df: a second call could land on a different second.
	local df_line
	df_line="$(df -Pk "${dir}" 2>/dev/null | awk 'NR==2 {print $4, $2}')" || return 1
	disk_free="${df_line%% *}"
	disk_total="${df_line##* }"
	[ -n "${disk_free}" ] && [ -n "${disk_total}" ] || return 1
	printf '%s %s %s %s %s\n' "$(( mem_total - mem_avail ))" "${mem_total}" "${procs}" "${disk_free}" "${disk_total}"
}

case "${1:-}" in
	start)
		STATE="${2:-}"
		[ -n "${STATE}" ] || { cannot "usage: $0 start <state-file> [interval-seconds] [directory]"; exit 0; }
		INTERVAL="${3:-5}"
		DIR="${4:-$PWD}"
		# A first sample BEFORE forking: if the machine cannot be read at all, the caller
		# learns it here and not forty minutes later from an empty report.
		FIRST="$(sample "${DIR}")" || { cannot "cannot read /proc/meminfo or ${DIR}; no witness this run"; exit 0; }
		(
			started="${SECONDS}"
			# La division en palabras es el objetivo: `sample` imprime cinco campos
			# separados por espacios y aqui se vuelven $1..$5.
			# shellcheck disable=SC2086
			set -- ${FIRST}
			peak_mem="$1" mem_total="$2" peak_procs="$3" low_disk="$4" disk_total="$5"
			peak_at=0 samples=1
			while :; do
				# The file is rewritten through a temporary name so that a `report` that
				# lands mid-write reads the PREVIOUS complete sample instead of half a line.
				printf '%s %s %s %s %s %s %s %s\n' \
					"${peak_mem}" "${mem_total}" "${peak_procs}" "${low_disk}" \
					"${disk_total}" "${peak_at}" "${samples}" "$(( SECONDS - started ))" \
					> "${STATE}.partial"
				mv -f "${STATE}.partial" "${STATE}"
				sleep "${INTERVAL}"
				now="$(sample "${DIR}")" || continue
				# Misma razon que arriba: cinco campos a $1..$5.
				# shellcheck disable=SC2086
				set -- ${now}
				samples=$(( samples + 1 ))
				[ "$1" -gt "${peak_mem}" ] && { peak_mem="$1"; peak_at=$(( SECONDS - started )); }
				[ "$3" -gt "${peak_procs}" ] && peak_procs="$3"
				[ "$4" -lt "${low_disk}" ] && low_disk="$4"
				mem_total="$2"
				disk_total="$5"
			done
		) >/dev/null 2>&1 &
		printf '%s\n' "$!"
		;;

	report)
		STATE="${2:-}"
		[ -n "${STATE}" ] || { cannot "usage: $0 report <state-file>"; exit 0; }
		# AN ABSENT FILE IS SAID OUT LOUD. Printing three zeros would be worse than
		# printing nothing: the next reader would believe the machine was idle.
		[ -s "${STATE}" ] || { cannot "no sample was ever written to ${STATE}"; exit 0; }
		read -r peak_mem mem_total peak_procs low_disk disk_total peak_at samples elapsed < "${STATE}" || {
			cannot "${STATE} does not hold a sample this reader knows"
			exit 0
		}
		# LC_ALL=C so the decimal separator is a point wherever the runner is configured:
		# a figure that changes shape with the locale is a figure nothing can compare.
		LC_ALL=C awk -v pm="${peak_mem}" -v mt="${mem_total}" -v pp="${peak_procs}" \
			-v ld="${low_disk}" -v dt="${disk_total}" -v pa="${peak_at}" \
			-v n="${samples}" -v el="${elapsed}" 'BEGIN {
				g = 1048576
				printf "resource witness: peak memory %.2f GiB of %.2f GiB (at t+%ds) · peak processes %d · least free disk %.1f GiB of %.1f GiB · %d samples over %d s\n",
					pm/g, mt/g, pa, pp, ld/g, dt/g, n, el
			}'
		;;

	*)
		cannot "usage: $0 {start <state-file> [interval-seconds] [directory]|report <state-file>}"
		;;
esac
