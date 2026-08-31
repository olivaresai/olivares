#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# engine-boot.sh — boot an engine on a local port and PROVE it is ours before using it.
#
# WHY THIS IS A LIBRARY AND NOT SIX LINES INSIDE web-e2e.sh. It used to be inline, and
# inline meant untestable: exercising it required pnpm install, a web bundle and a Go
# build, so the only way it was ever run was the slow one, and the way it FAILED was
# never run at all. It is sourced by its caller and by its battery, so the shape that
# broke is the shape under test.
#
# THE DEFECT THIS EXISTS TO CLOSE, measured 2026-08-08 by another lane:
# the readiness probe was `curl -sf http://127.0.0.1:$port/healthz`, which asks THE PORT
# whether something is alive there — never whether that something is the engine we just
# started. Three engines leaked from a closed session had been holding 8458/8460/8462 for
# ONE DAY AND FIVE HOURS. Our engine died on bind, the stranger answered the probe, the
# boot was graded a success, and the run then died downstream with "could not read the
# one-time setup token" — a message about a log file, pointing nowhere near the cause.
# In the whole output the words "bind", "address already in use" and "listen" did not
# appear ONCE. The failure was loud and the diagnosis was silent.
#
# THREE CHECKS, because one is not enough and each catches what the others cannot:
#   1. PRE-FLIGHT: refuse a port that already has a listener, BEFORE spawning anything.
#      This is the case that actually happened. `/dev/tcp` is used rather than curl on
#      purpose: it detects ANY listener, including one that would 404 /healthz, and a
#      squatter that does not speak our protocol is exactly as fatal as one that does.
#   2. LIVENESS: poll the CHILD as well as the port. A dead child ends the wait at once
#      instead of burning twenty seconds and then blaming the log.
#   3. POST-FLIGHT: health passed but the child is gone ⇒ whatever answered is not ours.
#      Closes the race the pre-flight cannot: a port free at check time, taken at spawn.
#
# Answers follow the engine's own convention and stay distinguishable: 0 booted, 1 could
# not boot (and WHY, by name). Never a token from an engine we cannot account for.

# engine_port_listener <port> — 0 if something is already listening, 1 if free, 2 if the
# probe itself could not run. The third answer is not decoration: bash can be built
# without /dev/tcp (--disable-net-redirections), and a probe that cannot look must not be
# read as "free" — that is the same fail-open this file exists to remove.
engine_port_listener() {
	local port="$1"
	if (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null; then
		exec 3>&-
		return 0
	fi
	return 1
}

# engine_probe_usable — prove /dev/tcp works in THIS bash before trusting a negative from
# it. A gate whose instrument is absent reports every port free and every squatter gone.
# The control connects to a port we have just bound ourselves, so a success is a fact
# about the feature and not about the network.
engine_probe_usable() {
	local ctl_port ctl_pid rc=1 attempt up
	command -v python3 >/dev/null 2>&1 || return 2 # cannot build the control; say so

	# ⛔ THE CONTROL MUST PROVE IT BOUND. Until 2026-08-24 the control's stderr went to
	#    /dev/null, so a FAILED bind was indistinguishable from a successful one, and this
	#    function then asked "is anything listening on that port?" — a question a STRANGER
	#    can answer yes to. Measured that day: with a squatter on the hint port this
	#    returned 0/usable while our own bind had failed, i.e. it validated the instrument
	#    against somebody else's listener. That is the exact failure this file's header
	#    describes ("our engine died on bind, the stranger answered the probe") reproduced
	#    INSIDE the check meant to catch it. The other side of the same coin is a false
	#    RED — nobody answers, we conclude "/dev/tcp is broken", exit 2, and a push is
	#    rejected for a busy port. That one happened too: it killed a lane's push.
	#
	#    So the control now announces "up" on fd 3 AFTER its bind returns, and a probe is
	#    only trusted when we have that announcement. No announcement means OUR PORT WAS
	#    TAKEN, which is an ordinary event here — five lanes share this host and the hint
	#    is `$$ % 900`, so live shells collide (three colliding pairs measured the same
	#    day) — and an ordinary event must not be reported as a broken instrument.
	for attempt in 0 1 2 3; do
		ctl_port="$(engine_free_port_hint "${attempt}")"
		up=""
		# fd 3 carries the handshake; the traceback of a failed bind still goes nowhere.
		exec 3< <(python3 -c "
import socket,sys,time
s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
s.bind(('127.0.0.1',${ctl_port})); s.listen(1)
sys.stdout.write('up\n'); sys.stdout.flush()
time.sleep(3)
" 2>/dev/null)
		ctl_pid=$!
		read -r -t 3 -u 3 up || up=""
		exec 3<&-
		if [ "${up}" = "up" ]; then
			# OUR bind succeeded: a listener on this port is now ours, so the answer is
			# a fact about /dev/tcp and not about a stranger.
			if engine_port_listener "${ctl_port}"; then rc=0; else rc=1; fi
			kill "${ctl_pid}" 2>/dev/null || true
			wait "${ctl_pid}" 2>/dev/null || true
			return "${rc}"
		fi
		kill "${ctl_pid}" 2>/dev/null || true
		wait "${ctl_pid}" 2>/dev/null || true
	done
	# Four distinct ports and not one bind: that is no longer "busy", it is an environment
	# where we cannot build a control at all. Say THAT, which is the third answer.
	return 2
}

# engine_free_port_hint [attempt] — a high port for the control listener. `$$ % 900` is
# deterministic per shell, which makes a collision REPRODUCIBLE, not impossible: two live
# shells whose pids differ by a multiple of 900 land on the same port, and on this shared
# host that is ordinary (three colliding pairs measured among live shells, 2026-08-24).
# The optional attempt number walks the hint to a different port so a caller can retry
# instead of concluding that the instrument is broken. Kept deterministic on purpose: a
# random port would make a failure unreproducible, which is worse than a busy one.
engine_free_port_hint() {
	local attempt="${1:-0}"
	# 20000-20899: ABOVE the product's ports (18443/18444, 19090) and BELOW the kernel's
	# ephemeral range (32768-60999 here), so no random outbound socket can be holding one.
	# The battery's own band is 61000+, so the probe never collides with the cases it serves.
	echo $(( 20000 + (($$ + attempt * 211) % 900) ))
}

# engine_port_owner <port> — BEST EFFORT: the pid(s) holding the port, for a message a
# human can act on. ss, lsof and fuser are all absent from this container (measured), so
# this walks /proc: the listening socket's inode from /proc/net/tcp, then whichever
# /proc/<pid>/fd points at it. It prints nothing when it cannot tell, and the caller says
# "could not determine" rather than implying there is no owner.
engine_port_owner() {
	local port="$1" hexport inode pid link
	hexport="$(printf '%04X' "${port}")"
	[ -r /proc/net/tcp ] || return 0
	# state 0A is LISTEN; field 2 is local_address as HEX_IP:HEX_PORT, field 10 the inode.
	inode="$(awk -v p=":${hexport}" '$4 == "0A" && index($2, p) { print $10 }' /proc/net/tcp 2>/dev/null | head -1)"
	[ -n "${inode}" ] || return 0
	for pid in /proc/[0-9]*; do
		for link in "${pid}"/fd/*; do
			[ -e "${link}" ] || continue
			case "$(readlink "${link}" 2>/dev/null)" in
			"socket:[${inode}]")
				echo "${pid##*/}"
				return 0
				;;
			esac
		done
	done
	return 0
}

# engine_proc_starttime <pid> — field 22 of /proc/<pid>/stat, or empty if it is gone.
#
# A PID IS NOT AN IDENTITY, and this is the half that #625 contributed to this file.
# The pidfile used to hold a bare number. If an engine dies on its own and the kernel
# recycles its number before cleanup runs, signalling that number reaches WHOEVER holds it
# now — and on this host that is another lane's work, because three containers share it.
# Demonstrated by the contrast on the first version of that fix, with the real cleanup and
# no engine at all: `sleep 300 &`, its pid written to the file, cleanup → KILLED.
#
# starttime is what the kernel documents for exactly this: it distinguishes one incarnation
# of a pid from the next. Parsed from AFTER the last ')' because field 2 is the executable
# name and may itself contain spaces and parentheses.
engine_proc_starttime() {
	local stat rest
	stat="$(cat "/proc/$1/stat" 2>/dev/null)" || return 0
	rest="${stat##*) }"
	# $1 of `rest` is field 3 (state), so field 22 is the 20th of what remains.
	printf '%s' "$rest" | awk '{print $20}'
}

# engine_proc_gone <pid> — true when the pid is absent OR a zombie. A zombie is gone for
# every purpose a caller has: it holds no port and no file, and we cannot reap it (it is
# not our child — the subshell that started it exited and init inherited it), so waiting
# for its directory to vanish would burn the whole timeout and then send a SIGKILL that
# does nothing.
engine_proc_gone() {
	local stat rest state
	[ -d "/proc/$1" ] || return 0
	stat="$(cat "/proc/$1/stat" 2>/dev/null)" || return 0
	[ -n "$stat" ] || return 0
	rest="${stat##*) }"
	state="${rest%% *}"
	[ "$state" = "Z" ]
}

# boot_engine_at <bin> <port> <data-dir> <pidfile> — start an engine, prove it is OURS,
# print its one-time setup token on stdout, and record the child's pid in <pidfile> so the
# caller's cleanup trap can kill it.
#
# A PIDFILE, NOT AN ARRAY, AND THAT IS THE SECOND DEFECT THIS FILE CLOSES. The inline
# version appended to a `PIDS` array — and every caller invokes it as
# `token="$(boot_engine …)"`, which is a COMMAND SUBSTITUTION, which is a SUBSHELL. The
# append mutated a copy that died with the substitution, so the parent's array stayed
# EMPTY and `cleanup()` iterated nothing. This script has therefore never killed a single
# engine it started, since the multi-engine change of 2026-08-05.
#
# That is not a tidiness point: it is the ORIGIN of the leak that produced the bug above.
# The three engines another lane found squatting 8458/8460/8462 for a day and five hours
# were leaked BY THIS SCRIPT, and the port pre-flight would then refuse to run because of
# them. Fixing only the probe would have left the script generating its own blockers.
# Found by this file's own battery asserting that the caller can see the child — a case
# written to confirm the happy path, which failed instead.
boot_engine_at() {
	local bin="$1" port="$2" data="$3" pidfile="$4"
	local grpc log pid i owner token
	grpc="$((port + 1))"
	log="${data}/engine.log"
	mkdir -p "${data}"

	# --- 1. PRE-FLIGHT -------------------------------------------------------------
	# BOTH ports, which is what #625 added: the engine binds ${port} AND ${grpc}, so a
	# stranger on the gRPC port alone lets the HTTP probe succeed while the engine dies on
	# its second bind — the failure whose log says nothing this side can read.
	local busy=""
	engine_port_listener "${port}" && busy="${port}"
	engine_port_listener "${grpc}" && busy="${busy:+${busy} and }${grpc}"
	if [ -n "${busy}" ]; then
		owner="$(engine_port_owner "${port}")"
		[ -n "${owner}" ] || owner="$(engine_port_owner "${grpc}")"
		# One port and two ports get DIFFERENT sentences on purpose. The single-port
		# wording is the one the battery drives and asserts verbatim; a generic "port(s)"
		# line would have quietly loosened that assertion while looking like tidying.
		if [ "${busy}" = "${port}" ] || [ "${busy}" = "${grpc}" ]; then
			echo "ERROR: 127.0.0.1:${busy} ALREADY has a listener; refusing to boot on top of it." >&2
		else
			echo "ERROR: 127.0.0.1 ports ${busy} ALREADY have listeners; refusing to boot on top." >&2
		fi
		if [ -n "${owner}" ]; then
			echo "       It is held by pid ${owner}. Check it is yours, then kill it BY PID." >&2
		else
			echo "       Could not determine which process holds it (no ss/lsof/fuser here and" >&2
			echo "       /proc gave nothing) — that is 'could not look', not 'nobody owns it'." >&2
		fi
		echo "       Measured cause, 2026-08-08: engines leaked from a CLOSED session held" >&2
		echo "       8458/8460/8462 for 1 day 5 hours. The old probe accepted their /healthz" >&2
		echo "       as proof of our own boot and the run died later blaming the token." >&2
		echo "       NEVER pkill -f on this host: it is shared by three containers." >&2
		return 1
	fi

	"${bin}" serve --insecure --listen "127.0.0.1:${port}" --grpc-listen "127.0.0.1:${grpc}" \
		--data-dir "${data}" >"${log}" 2>&1 &
	pid=$!
	# Recorded BEFORE the first check that can return: a child we spawned must be killable
	# even when the boot fails, or a failed boot leaks exactly what the pre-flight refuses.
	# `pid starttime`, not a bare pid — read RIGHT NOW, while the pid is certainly still
	# ours. Read later it could already belong to a different process, which is the whole
	# point of recording it.
	if ! printf '%s %s\n' "${pid}" "$(engine_proc_starttime "${pid}")" >>"${pidfile}"; then
		kill "${pid}" 2>/dev/null || true
		echo "ERROR: could not record pid ${pid} in ${pidfile}; killed it rather than leak it." >&2
		return 1
	fi

	# --- 2. LIVENESS + readiness ---------------------------------------------------
	for i in $(seq 1 40); do
		if ! kill -0 "${pid}" 2>/dev/null; then
			echo "ERROR: the engine we started for 127.0.0.1:${port} EXITED during boot." >&2
			echo "       This is the case the old probe could not see: it asked the port, not" >&2
			echo "       the child, so a dead engine plus any squatter read as a healthy boot." >&2
			echo "       Its log follows; a bind failure appears here and nowhere else." >&2
			sed 's/^/         /' "${log}" >&2 || true
			return 1
		fi
		if curl -sf "http://127.0.0.1:${port}/healthz" >/dev/null 2>&1; then break; fi
		sleep 0.5
	done

	# --- 3. POST-FLIGHT ------------------------------------------------------------
	if ! kill -0 "${pid}" 2>/dev/null; then
		echo "ERROR: 127.0.0.1:${port} answers, but the engine we started is GONE." >&2
		echo "       Whatever is answering is not ours, so nothing measured against it counts." >&2
		sed 's/^/         /' "${log}" >&2 || true
		return 1
	fi
	if ! curl -sf "http://127.0.0.1:${port}/healthz" >/dev/null 2>&1; then
		echo "ERROR: engine on 127.0.0.1:${port} never accepted connections (pid ${pid} alive)." >&2
		sed 's/^/         /' "${log}" >&2 || true
		return 1
	fi

	token="$(grep -oE 'olst_[A-Z0-9]+' "${log}" | head -1 || true)"
	if [ -z "${token}" ]; then
		echo "ERROR: could not read the one-time setup token from ${log}." >&2
		echo "       The engine IS ours and IS healthy — so this is a real token problem," >&2
		echo "       which is exactly what this message could not be trusted to mean before." >&2
		sed 's/^/         /' "${log}" >&2 || true
		return 1
	fi
	printf '%s' "${token}"
}
