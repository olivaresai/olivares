#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-ci-resource-witness.sh — scripts/ci-resource-witness.sh must cost a job nothing while it
# runs and must report the job's own cgroup v2 figures.
#
# Why: on 2026-11-08 three witness loops walking /proc held 266 %, 109 % and 105 % of a core on
# the CI host while its test jobs ran. The kernel already keeps the numbers in the
# job's cgroup (memory.peak, memory.events, cpu.stat), so nothing has to run beside the job.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WITNESS="$ROOT/scripts/ci-resource-witness.sh"
fails=0
ok() { printf '  ok    %s\n' "$1"; }
bad() { printf '  FAIL  %s — %s\n' "$1" "$2"; fails=$((fails + 1)); }

[ -r "$WITNESS" ] || { echo "test-ci-resource-witness: 2 COULD NOT LOOK — missing $WITNESS" >&2; exit 2; }
for tool in setsid pgrep pkill; do
	command -v "$tool" >/dev/null 2>&1 || { echo "test-ci-resource-witness: 2 COULD NOT LOOK — missing $tool" >&2; exit 2; }
done
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT

# 1. No resident sampler. `start` runs in its own session; two seconds after it returns, nothing
#    of that session may still be alive (the old sampler was a detached loop).
# shellcheck disable=SC2016 # the inner script expands its own $1, $2 and $$
sid="$(setsid bash -c 'bash "$1" start "$2" >/dev/null 2>&1; echo $$' _ "$WITNESS" "$T/live.state")"
sleep 2
if pgrep -s "$sid" >/dev/null 2>&1; then
	pkill -s "$sid" >/dev/null 2>&1
	bad "no loop after start" "a process of the witness is still running 2 s after start returned"
else
	ok "no loop after start"
fi

# 2. report reads the job's cgroup: cpu and oom_kill are the difference since start, the
#    memory peak is the kernel's own memory.peak.
P="$T/proc" C="$T/cg"
mkdir -p "$P/self" "$C/job"
echo '0::/job' >"$P/self/cgroup"
cgroup() { # cgroup <usage_usec> <oom_kill> <memory.peak>
	printf 'usage_usec %s\nuser_usec 0\nsystem_usec 0\n' "$1" >"$C/job/cpu.stat"
	printf 'low 0\nhigh 0\nmax 0\noom 0\noom_kill %s\n' "$2" >"$C/job/memory.events"
	echo 1073741824 >"$C/job/memory.current"
	echo max >"$C/job/memory.max"
	echo "$3" >"$C/job/memory.peak"
}
witness() { OLIVARES_WITNESS_PROC_ROOT="$P" OLIVARES_WITNESS_CGROUP_ROOT="$C" bash "$WITNESS" "$@"; }
cgroup 1000000 0 1073741824
witness start "$T/fx.state" >/dev/null 2>&1
cgroup 4000000 1 2147483648
out="$(witness report "$T/fx.state" 2>&1)"
for want in 'cgroup /job' 'cpu 3.0 s' 'peak 2.00 GiB' 'oom_kill +1'; do
	case "$out" in *"$want"*) ok "report says '$want'" ;; *) bad "report says '$want'" "got: $out" ;; esac
done

# 3. The root of the tree has no memory.current and its cpu.stat is the whole host's: say so
#    and write no state, never print someone else's counters.
R="$T/root"
mkdir -p "$R/self" "$R/cg"
echo '0::/' >"$R/self/cgroup"
printf 'usage_usec 9\n' >"$R/cg/cpu.stat"
err="$(OLIVARES_WITNESS_PROC_ROOT="$R" OLIVARES_WITNESS_CGROUP_ROOT="$R/cg" bash "$WITNESS" start "$T/root.state" 2>&1 >/dev/null)"
case "$err" in *"COULD NOT LOOK"*) ok "root cgroup: COULD NOT LOOK" ;; *) bad "root cgroup: COULD NOT LOOK" "got: $err" ;; esac
if [ -e "$T/root.state" ]; then
	bad "root cgroup: no state written" "$T/root.state exists"
else
	ok "root cgroup: no state written"
fi

[ "$fails" -eq 0 ] && { echo "test-ci-resource-witness: PASS"; exit 0; }
echo "test-ci-resource-witness: $fails FAIL" >&2
exit 1
