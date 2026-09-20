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
# ATTRIBUTION, added after the first hosted reading. "12.74 GiB of 15.61" said HOW MUCH and
# not WHOSE, and the next decision (a smaller ceiling? fewer test binaries at once?) depends
# on whose. Beside each host figure the sampler keeps the five processes with the largest
# resident set — pid, start time in clock ticks (a pid alone is reused), the kernel's short
# name, RSS — at the sample where host memory peaked and at the LAST sample, and the
# cgroup's own memory figures when the job's membership can be read.
#   · RSS is a SAMPLED SUBSET, not an account and not a bound: it counts shared pages in
#     EVERY process that maps them, so the five do not sum to host memory and their sum
#     can exceed the unique memory they hold. Five is a subset; the sampling sequence is
#     not an atomic snapshot of the machine.
#   · Only the short name is recorded. Command lines and environments can carry credentials
#     and are never read.
#   · A row is bound to ONE process incarnation: the start ticks are read before AND after
#     the name and RSS, and a row whose identity changed in between (a pid reused mid-read)
#     is not attributed — it is COUNTED as incoherent, beside the processes that could not
#     be read at all. A process that vanished between the listing and the read is skipped.
#   · The cgroup figures are printed only for a POSITIVELY observed v2 membership (a `0::`
#     line in the job's own cgroup file whose directory exists). No membership, a v1-only
#     file or an unreadable one: every figure is `unavailable`, and nothing falls back to
#     the root of the tree, whose counters belong to someone else.
#   · The cgroup's `memory.current` includes the page cache charged to the group, so it is
#     NOT comparable with the host figure above (which excludes reclaimable cache). Its
#     `memory.events` counters are LIFETIME counters of the cgroup; the witness keeps the
#     first sample's snapshot with its cgroup path so that `report` can say what moved
#     DURING the run, and says `unavailable` when the cgroup changed or a side is missing.
#   · Every attribution belongs to its own host sample: both files carry the sample number,
#     and a sample whose attribution could not be read gets an EXPLICIT `attribution
#     unavailable` block for that sample — a previous block is never carried forward or
#     relabelled as a new peak. A figure that could not be read is printed as
#     `unavailable`, never as zero: zero would say "idle".
#
#   start <state-file> [interval-seconds] [directory]   sample until killed; print the pid
#   report <state-file>                                 print the maxima, or say there are none
set -euo pipefail

cannot() { printf 'resource witness: COULD NOT LOOK — %s\n' "$*" >&2; }

# The two roots are variables so that the bench can point them at fixtures. Nothing else
# reads them, and the workflows never set them.
PROC_ROOT="${OLIVARES_WITNESS_PROC_ROOT:-/proc}"
CGROUP_ROOT="${OLIVARES_WITNESS_CGROUP_ROOT:-/sys/fs/cgroup}"
TOP_N=5

# One sample, printed as five whitespace-separated fields:
#   used-memory-KiB  total-memory-KiB  process-count  free-disk-KiB  total-disk-KiB
#
# Memory in USE is MemTotal - MemAvailable: MemFree alone calls the page cache "used" and
# would report a machine with 12 GiB of cache as exhausted when it is idle.
# The process count comes from a glob over /proc and not from `ps`, so it costs no fork.
sample() {
	local dir="$1" mem_total mem_avail procs disk_free disk_total
	mem_total="$(awk '/^MemTotal:/ {print $2; exit}' "${PROC_ROOT}/meminfo")" || return 1
	mem_avail="$(awk '/^MemAvailable:/ {print $2; exit}' "${PROC_ROOT}/meminfo")" || return 1
	[ -n "${mem_total}" ] && [ -n "${mem_avail}" ] || return 1
	local pids=("${PROC_ROOT}"/[0-9]*)
	procs="${#pids[@]}"
	# Two fields from one df: a second call could land on a different second.
	local df_line
	df_line="$(df -Pk "${dir}" 2>/dev/null | awk 'NR==2 {print $4, $2}')" || return 1
	disk_free="${df_line%% *}"
	disk_total="${df_line##* }"
	[ -n "${disk_free}" ] && [ -n "${disk_total}" ] || return 1
	printf '%s %s %s %s %s\n' "$(( mem_total - mem_avail ))" "${mem_total}" "${procs}" "${disk_free}" "${disk_total}"
}

# Start ticks of one process, into TICKS (a variable and not a print: no fork per process).
#   · field 22 of stat, counted AFTER the last `)`: the name sits in parentheses and may
#     itself contain spaces and parentheses.
# Returns 1 when stat cannot be read or the field is not a number.
TICKS=""
start_ticks() {
	local line rest
	TICKS=""
	IFS= read -r line < "$1/stat" 2>/dev/null || return 1
	rest="${line##*) }"
	# shellcheck disable=SC2086
	set -- ${rest}
	# rest starts at field 3 (state), so field 22 is the 20th word of it.
	case "${20:-}" in '' | *[!0-9]*) return 1 ;; *) TICKS="${20}" ;; esac
}

# The TOP_N processes by resident set, one per line: `rss-KiB pid start-ticks name`, then
# `unreadable <n>` and `incoherent <n>`. Shell builtins only — no fork per process, because
# this runs every few seconds beside a suite that is already short of memory.
#   · Identity is read BEFORE and AFTER the name and RSS; a row whose start ticks changed
#     in between belonged to two processes and is counted as incoherent, not attributed.
#   · A kernel thread has no VmRSS line and is not a candidate. A process whose files
#     vanish mid-read has exited and is skipped. One whose status or stat cannot be read
#     while it still exists is counted as unreadable.
top_processes() {
	local d pid name rss line key val t1 unreadable=0 incoherent=0
	local -a rows=()
	for d in "${PROC_ROOT}"/[0-9]*; do
		[ -d "${d}" ] || continue
		pid="${d##*/}"
		name="" rss=""
		[ -e "${d}/status" ] || continue
		if [ ! -r "${d}/status" ]; then
			unreadable=$(( unreadable + 1 ))
			continue
		fi
		if ! start_ticks "${d}"; then
			[ -d "${d}" ] || continue
			unreadable=$(( unreadable + 1 ))
			continue
		fi
		t1="${TICKS}"
		if ! { while IFS= read -r line; do
			key="${line%%:*}"
			case "${key}" in
				Name) val="${line#*:}"; name="${val#"${val%%[![:space:]]*}"}" ;;
				VmRSS) val="${line#*:}"; val="${val#"${val%%[![:space:]]*}"}"; rss="${val%% *}" ;;
			esac
		done < "${d}/status"; } 2>/dev/null; then
			[ -d "${d}" ] || continue
			unreadable=$(( unreadable + 1 ))
			continue
		fi
		# Every readable status begins with `Name:`. One that yielded no name was not read
		# (a directory or a socket in its place opens and then cannot be read): counted.
		if [ -z "${name}" ]; then
			[ -d "${d}" ] || continue
			unreadable=$(( unreadable + 1 ))
			continue
		fi
		case "${rss}" in '' | *[!0-9]*) continue ;; esac
		if ! start_ticks "${d}"; then
			[ -d "${d}" ] || continue
			incoherent=$(( incoherent + 1 ))
			continue
		fi
		if [ "${t1}" != "${TICKS}" ]; then
			incoherent=$(( incoherent + 1 ))
			continue
		fi
		# The name is the kernel's, at most 15 bytes; anything outside a plain set is
		# replaced so that one odd name cannot break the line format or a log reader.
		name="${name//[^A-Za-z0-9._:+-]/_}"
		rows+=("${rss} ${pid} ${t1} ${name:-unavailable}")
	done
	if [ "${#rows[@]}" -gt 0 ]; then
		printf '%s\n' "${rows[@]}" | LC_ALL=C sort -k1,1nr -k2,2n | head -n "${TOP_N}"
	fi
	printf 'unreadable %s\n' "${unreadable}"
	printf 'incoherent %s\n' "${incoherent}"
}

# The job's own cgroup (v2), as `key value` lines, starting with `path <rel>`. Figures are
# printed ONLY for a positively observed membership: a `0::` line in the job's cgroup file
# whose directory exists under the cgroup root. Otherwise every line says `unavailable`
# and nothing is read from the root of the tree. `max` is the kernel's own word for "no
# limit" and is kept as it is. The four event counters are always named; one the file does
# not carry is `unavailable`. An events file that exists but cannot be read fails the
# block (the caller then writes an explicit unavailable attribution for that sample).
cgroup_memory() {
	local rel="" bound=0 line dir f v
	local high=unavailable max=unavailable oom=unavailable oom_kill=unavailable
	if [ -r "${PROC_ROOT}/self/cgroup" ]; then
		while IFS= read -r line; do
			case "${line}" in 0::*) rel="${line#0::}"; bound=1 ;; esac
		done < "${PROC_ROOT}/self/cgroup" 2>/dev/null || bound=0
	fi
	if [ "${bound}" = 1 ]; then
		dir="${CGROUP_ROOT}${rel%/}"
		[ -d "${dir}" ] || bound=0
	fi
	if [ "${bound}" != 1 ]; then
		printf 'path unavailable\n'
		for f in memory.current memory.max memory.peak; do printf '%s unavailable\n' "${f}"; done
		printf 'events.high unavailable\nevents.max unavailable\nevents.oom unavailable\nevents.oom_kill unavailable\n'
		return 0
	fi
	printf 'path %s\n' "${rel:-/}"
	for f in memory.current memory.max memory.peak; do
		v="unavailable"
		if [ -r "${dir}/${f}" ] && IFS= read -r line < "${dir}/${f}" 2>/dev/null; then
			case "${line}" in max) v="max" ;; '' | *[!0-9]*) ;; *) v="${line}" ;; esac
		fi
		printf '%s %s\n' "${f}" "${v}"
	done
	if [ -r "${dir}/memory.events" ]; then
		while IFS= read -r line; do
			v="${line#* }"
			case "${v}" in '' | *[!0-9]*) v="unavailable" ;; esac
			case "${line}" in
				"high "*) high="${v}" ;;
				"max "*) max="${v}" ;;
				"oom "*) oom="${v}" ;;
				"oom_kill "*) oom_kill="${v}" ;;
			esac
		done < "${dir}/memory.events" 2>/dev/null || return 1
	fi
	printf 'events.high %s\nevents.max %s\nevents.oom %s\nevents.oom_kill %s\n' "${high}" "${max}" "${oom}" "${oom_kill}"
}

# One attribution block: a header naming the moment and the sample, the processes, the
# cgroup. Fails (non-zero) when a read fails; the caller then writes the unavailable block.
attribution_block() {
	printf '%s t+%s host-used-KiB %s sample %s\n' "$1" "$2" "$3" "$4"
	top_processes | sed 's/^/  proc /'
	cgroup_memory | sed 's/^/  cgroup /'
}
# The block written for a sample whose attribution could not be read: the host figure is
# kept, and the absence is said for THAT sample.
unavailable_block() {
	printf '%s t+%s host-used-KiB %s sample %s\n  attribution unavailable\n' "$1" "$2" "$3" "$4"
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
			peak_at=0 samples=1 seq=1
			# Attribution lives in its OWN file, so the first line of the state file keeps the
			# shape older readers know (a ninth field, the sample number, is appended).
			# Three blocks: the INITIAL sample (the baseline for the cgroup's lifetime
			# counters), the sample where host memory peaked, and the last one — a job that
			# dies is explained by the last, not by the peak.
			if block="$(attribution_block initial 0 "${peak_mem}" "${seq}")"; then
				init_block="${block}"
			else
				init_block="$(unavailable_block initial 0 "${peak_mem}" "${seq}")"
			fi
			last_block="${init_block/#initial /last }"
			peak_block="${init_block/#initial /peak }"
			while :; do
				# The file is rewritten through a temporary name so that a `report` that
				# lands mid-write reads the PREVIOUS complete sample instead of half a line.
				printf '%s %s %s %s %s %s %s %s %s\n' \
					"${peak_mem}" "${mem_total}" "${peak_procs}" "${low_disk}" \
					"${disk_total}" "${peak_at}" "${samples}" "$(( SECONDS - started ))" "${seq}" \
					> "${STATE}.partial"
				mv -f "${STATE}.partial" "${STATE}"
				printf '%s\n%s\n%s\n' "${init_block}" "${peak_block}" "${last_block}" > "${STATE}.attribution.partial"
				mv -f "${STATE}.attribution.partial" "${STATE}.attribution"
				sleep "${INTERVAL}"
				now="$(sample "${DIR}")" || continue
				# Misma razon que arriba: cinco campos a $1..$5.
				# shellcheck disable=SC2086
				set -- ${now}
				samples=$(( samples + 1 )); seq=$(( seq + 1 ))
				# The processes are read right after the host figure they explain. A failed
				# read writes THIS sample's explicit unavailable block: the previous block is
				# never kept as if it were this sample's.
				if block="$(attribution_block last "$(( SECONDS - started ))" "$1" "${seq}")"; then
					last_block="${block}"
				else
					last_block="$(unavailable_block last "$(( SECONDS - started ))" "$1" "${seq}")"
				fi
				[ "$1" -gt "${peak_mem}" ] && {
					peak_mem="$1"
					peak_at=$(( SECONDS - started ))
					peak_block="${last_block/#last /peak }"
				}
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
		read -r peak_mem mem_total peak_procs low_disk disk_total peak_at samples elapsed state_seq < "${STATE}" || {
			cannot "${STATE} does not hold a sample this reader knows"
			exit 0
		}
		case "${state_seq:-}" in '' | *[!0-9]*) state_seq="unavailable" ;; esac
		# LC_ALL=C so the decimal separator is a point wherever the runner is configured:
		# a figure that changes shape with the locale is a figure nothing can compare.
		LC_ALL=C awk -v pm="${peak_mem}" -v mt="${mem_total}" -v pp="${peak_procs}" \
			-v ld="${low_disk}" -v dt="${disk_total}" -v pa="${peak_at}" \
			-v n="${samples}" -v el="${elapsed}" -v sq="${state_seq}" 'BEGIN {
				g = 1048576
				printf "resource witness: peak memory %.2f GiB of %.2f GiB (at t+%ds) · peak processes %d · least free disk %.1f GiB of %.1f GiB · %d samples over %d s · state sample #%s\n",
					pm/g, mt/g, pa, pp, ld/g, dt/g, n, el, sq
			}'
		# WHOSE memory. No attribution file = a sampler older than attribution, or one that
		# never managed a read: said, not guessed. A figure that is not a number is printed
		# as the word it is.
		if [ -s "${STATE}.attribution" ]; then
			LC_ALL=C awk -v state_seq="${state_seq}" '
			function gib(kib) { return (kib ~ /^[0-9]+$/) ? sprintf("%.2f GiB", kib / 1048576) : "unavailable" }
			function gibb(b)  { return (b == "max") ? "no limit" : ((b ~ /^[0-9]+$/) ? sprintf("%.2f GiB", b / 1073741824) : "unavailable") }
			function isnum(x) { return x ~ /^[0-9]+$/ }
			function flush(   w) {
				if (moment == "") return
				if (moment == "initial") { ipath = path; ihigh = ev["high"]; imax = ev["max"]; ioom = ev["oom"]; ioomk = ev["oom_kill"]; iseq = seq; iunav = unav; return }
				w = (moment == "peak") ? "at the memory peak" : "at the last sample"
				if (moment == "last") { lpath = path; lhigh = ev["high"]; lmax = ev["max"]; loom = ev["oom"]; loomk = ev["oom_kill"]; lseq = seq; lunav = unav }
				if (unav) {
					printf "resource witness: %s (t+%ss, host used %s, sample #%s) — attribution unavailable at that sample; the host figure is kept, nothing is inferred\n", w, at, gib(used), seq
					return
				}
				printf "resource witness: %s (t+%ss, host used %s, sample #%s) — largest resident sets, a sampled subset (RSS counts shared pages in every process that maps them; not a bound on unique memory): %s · unreadable processes: %s · incoherent (pid reused mid-read): %s\n",
					w, at, gib(used), seq, (procs == "" ? "none readable" : procs), unread, incoh
				printf "resource witness: %s — cgroup %s: current %s · limit %s · peak %s · events since the cgroup was created: high=%s max=%s oom=%s oom_kill=%s\n",
					w, path, cur, lim, pk, ev["high"], ev["max"], ev["oom"], ev["oom_kill"]
			}
			$1 == "initial" || $1 == "peak" || $1 == "last" {
				flush(); moment = $1; at = substr($2, 3); used = $4; seq = $6; unav = 0
				procs = ""; unread = "unavailable"; incoh = "unavailable"; cur = lim = pk = "unavailable"; path = "unavailable"
				ev["high"] = ev["max"] = ev["oom"] = ev["oom_kill"] = "unavailable"
				next
			}
			$1 == "attribution" && $2 == "unavailable" { unav = 1; next }
			$1 == "proc" && $2 == "unreadable" { unread = $3; next }
			$1 == "proc" && $2 == "incoherent" { incoh = $3; next }
			$1 == "proc" {
				procs = procs (procs == "" ? "" : " · ") sprintf("%s %s (pid %s, start %s)", $5, gib($2), $3, $4)
				next
			}
			$1 == "cgroup" && $2 == "path"           { path = $3; next }
			$1 == "cgroup" && $2 == "memory.current" { cur = gibb($3); next }
			$1 == "cgroup" && $2 == "memory.max"     { lim = gibb($3); next }
			$1 == "cgroup" && $2 == "memory.peak"    { pk  = gibb($3); next }
			$1 == "cgroup" && $2 ~ /^events\./       { ev[substr($2, 8)] = $3; next }
			END {
				flush()
				# The state line and the blocks are two files from one sampling sequence, not
				# an atomic snapshot: say which sample each side is, and whether they agree.
				if (!isnum(lseq) || !isnum(state_seq))
					printf "resource witness: UNBOUND — the state line (sample #%s) and the last attribution block (sample #%s) cannot be tied to one moment because a sample number is missing; read them as two independent observations\n", (isnum(state_seq) ? state_seq : "unavailable"), (isnum(lseq) ? lseq : "unavailable")
				else if (lseq != state_seq)
					printf "resource witness: MISMATCH — the state line is sample #%s and the last attribution block is sample #%s (two files, written one after the other); read them as two moments\n", state_seq, lseq
				else
					printf "resource witness: state line and last attribution block are the same sample (#%s); the sampling sequence is not an atomic snapshot of the machine\n", state_seq
				# What moved DURING the run: last minus initial, only for the SAME cgroup
				# with both sides numeric. Anything else is unavailable, not zero.
				if (iunav || lunav) reason = "a side has no attribution"
				else if (ipath == "unavailable" || lpath == "unavailable") reason = "the cgroup membership was not observed on a side"
				else if (ipath != lpath) reason = sprintf("the cgroup changed (%s → %s)", ipath, lpath)
				else if (!(isnum(ihigh) && isnum(lhigh) && isnum(imax) && isnum(lmax) && isnum(ioom) && isnum(loom) && isnum(ioomk) && isnum(loomk))) reason = "a counter is unavailable on a side"
				else if (lhigh + 0 < ihigh + 0 || lmax + 0 < imax + 0 || loom + 0 < ioom + 0 || loomk + 0 < ioomk + 0) reason = sprintf("a counter went down between the samples (high %s→%s max %s→%s oom %s→%s oom_kill %s→%s): lifetime counters do not decrease, so this is not the same cgroup instance or the read is unreliable", ihigh, lhigh, imax, lmax, ioom, loom, ioomk, loomk)
				else reason = ""
				if (reason == "")
					printf "resource witness: cgroup events during the run (sample #%s → #%s, same cgroup %s): high +%d · max +%d · oom +%d · oom_kill +%d\n", iseq, lseq, ipath, lhigh - ihigh, lmax - imax, loom - ioom, loomk - ioomk
				else
					printf "resource witness: cgroup events during the run: delta unavailable — %s\n", reason
			}
			' "${STATE}.attribution"
		else
			cannot "no process attribution beside ${STATE}: memory is reported without saying whose"
		fi
		;;

	*)
		cannot "usage: $0 {start <state-file> [interval-seconds] [directory]|report <state-file>}"
		;;
esac
