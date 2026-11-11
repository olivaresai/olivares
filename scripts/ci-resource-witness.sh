#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ci-resource-witness.sh — the numbers a job that dies MUTE never printed.
#
# WHY IT EXISTS, measured 2026-09-19. Three hosted jobs died inside the functional suite
# with `The runner has received a shutdown signal` and exit 143 after 19, 29 and 45 minutes
# of silence. The log carried the two error lines and nothing else: no memory figure, no
# CPU figure, no free space — so every diagnosis had to start by REPRODUCING the run
# instead of by reading it.
#
# HOW IT READS. The kernel already keeps the job's numbers in its cgroup v2, so nothing runs
# beside the job (the first version sampled /proc in a loop and held 266 %, 109 % and 105 % of
# a core on the CI host, 2026-11-08). `start` stores one snapshot; `report` reads the
# cgroup again and prints the difference and the kernel's own peak:
#   · cpu.stat usage_usec           — CPU the cgroup used during the run (difference).
#   · memory.events high/max/oom/oom_kill — what the kernel did about memory during the run
#                                     (difference). oom_kill above zero is the OOM killer.
#   · memory.peak                   — highest memory the cgroup held SINCE IT WAS CREATED, not
#                                     since `start`: a cgroup that ran earlier jobs can show
#                                     an older peak. memory.peak and memory.current include page
#                                     cache, so they are not the host's MemAvailable-style "used".
#   · free disk of the working directory, at report time.
#
# WHAT IT IS NOT. Figures are printed only for a positively observed cgroup v2 membership (a
# `0::` line in the process's own cgroup file whose directory has memory.current, which the
# root of the tree does not): the root's counters belong to the whole host, so nothing falls
# back to it. The report needs the shell, the state file and the log to survive: an
# EXIT/TERM trap cannot guarantee a report after SIGKILL or host loss. No report means the
# resource state is unknown, not that memory exhaustion was established or excluded. The
# witness does not decide the test verdict; unavailable-data paths print COULD NOT LOOK and
# return zero. A figure that could not be read is printed as `unavailable`, never as zero.
#
#   start <state-file>    store the baseline; prints nothing and leaves nothing running
#   report <state-file>   print the run's figures, or say there are none
set -euo pipefail

cannot() { printf 'resource witness: COULD NOT LOOK — %s\n' "$*" >&2; }

# The two roots are variables so that the test can point them at fixtures. Nothing else
# reads them, and the workflows never set them.
PROC_ROOT="${OLIVARES_WITNESS_PROC_ROOT:-/proc}"
CGROUP_ROOT="${OLIVARES_WITNESS_CGROUP_ROOT:-/sys/fs/cgroup}"

# Sets REL (the cgroup path) and DIR (its directory). Returns 1 unless the membership is seen.
observe() {
	local line
	REL="" DIR=""
	[ -r "${PROC_ROOT}/self/cgroup" ] || return 1
	while IFS= read -r line; do
		case "${line}" in 0::*) REL="${line#0::}" ;; esac
	done < "${PROC_ROOT}/self/cgroup" || return 1
	[ -n "${REL}" ] || return 1
	DIR="${CGROUP_ROOT}${REL%/}"
	[ -r "${DIR}/memory.current" ] && [ -r "${DIR}/cpu.stat" ]
}

# The first line of a cgroup file, as it is: a number, or `max`; anything else is `unavailable`.
one() {
	local v=""
	IFS= read -r v < "${DIR}/$1" 2>/dev/null || true
	case "${v}" in max) ;; '' | *[!0-9]*) v="unavailable" ;; esac
	printf '%s' "${v}"
}

# `usage_usec high max oom oom_kill`, one line; `-` for a counter the files do not carry.
snapshot() {
	local key val usage=- high=- max=- oom=- oom_kill=-
	while read -r key val; do
		case "${key}:${val}" in usage_usec:*[!0-9]* | usage_usec:) ;; usage_usec:*) usage="${val}" ;; esac
	done < "${DIR}/cpu.stat" 2>/dev/null || true
	if [ -r "${DIR}/memory.events" ]; then
		while read -r key val; do
			case "${val}" in '' | *[!0-9]*) continue ;; esac
			case "${key}" in
				high) high="${val}" ;; max) max="${val}" ;; oom) oom="${val}" ;; oom_kill) oom_kill="${val}" ;;
			esac
		done < "${DIR}/memory.events" 2>/dev/null || true
	fi
	printf '%s %s %s %s %s' "${usage}" "${high}" "${max}" "${oom}" "${oom_kill}"
}

case "${1:-}" in
	start)
		STATE="${2:-}"
		[ -n "${STATE}" ] || { cannot "usage: $0 start <state-file>"; exit 0; }
		observe || { cannot "no readable cgroup v2 membership for this job; no witness this run"; exit 0; }
		# Written through a temporary name: a `report` landing mid-write never reads half a line.
		printf '%(%s)T %s %s\n' -1 "$(snapshot)" "${REL}" > "${STATE}.partial"
		mv -f "${STATE}.partial" "${STATE}"
		;;

	report)
		STATE="${2:-}"
		[ -n "${STATE}" ] || { cannot "usage: $0 report <state-file>"; exit 0; }
		# AN ABSENT FILE IS SAID OUT LOUD. Printing zeros would be worse than printing nothing:
		# the next reader would believe the machine was idle.
		[ -s "${STATE}" ] || { cannot "no sample was ever written to ${STATE}"; exit 0; }
		read -r t0 usage0 high0 max0 oom0 oom_kill0 rel0 < "${STATE}" || {
			cannot "${STATE} does not hold a sample this reader knows"
			exit 0
		}
		observe || { cannot "the job's cgroup cannot be read now"; exit 0; }
		[ "${REL}" = "${rel0}" ] || { cannot "the job moved from cgroup ${rel0} to ${REL}; the figures would mix two cgroups"; exit 0; }
		read -r usage1 high1 max1 oom1 oom_kill1 <<< "$(snapshot)"
		printf -v now '%(%s)T' -1
		# One df, two fields: a second call could land on a different second.
		disk="$(df -Pk "${PWD}" 2>/dev/null | awk 'NR==2 {print $4, $2}')" || disk=""
		# LC_ALL=C so the decimal separator is a point wherever the runner is configured:
		# a figure that changes shape with the locale is a figure nothing can compare.
		LC_ALL=C awk -v rel="${REL}" -v el="$(( now - t0 ))" -v disk="${disk}" \
			-v u0="${usage0}" -v u1="${usage1}" \
			-v e0="${high0} ${max0} ${oom0} ${oom_kill0}" -v e1="${high1} ${max1} ${oom1} ${oom_kill1}" \
			-v cur="$(one memory.current)" -v lim="$(one memory.max)" -v pk="$(one memory.peak)" '
			function isnum(x) { return x ~ /^[0-9]+$/ }
			function gib(b)   { return (b == "max") ? "no limit" : (isnum(b) ? sprintf("%.2f GiB", b / 1073741824) : "unavailable") }
			function delta(a, b) { return (isnum(a) && isnum(b)) ? sprintf("+%d", b - a) : "unavailable" }
			BEGIN {
				cpu = (isnum(u0) && isnum(u1)) ? sprintf("%.1f s (%.2f cores)", (u1 - u0) / 1e6, (el > 0 ? (u1 - u0) / 1e6 / el : 0)) : "unavailable"
				split(e0, a, " "); split(e1, b, " ")
				n = split(disk, d, " ")
				df = (n == 2) ? sprintf("%.1f GiB of %.1f GiB", d[1] / 1048576, d[2] / 1048576) : "unavailable"
				printf "resource witness: cgroup %s over %d s · cpu %s · memory peak %s (since the cgroup was created) · now %s · limit %s · events during the run: high %s max %s oom %s oom_kill %s · free disk %s\n",
					rel, el, cpu, gib(pk), gib(cur), gib(lim),
					delta(a[1], b[1]), delta(a[2], b[2]), delta(a[3], b[3]), delta(a[4], b[4]), df
			}'
		;;

	*)
		cannot "usage: $0 {start <state-file>|report <state-file>}"
		;;
esac
