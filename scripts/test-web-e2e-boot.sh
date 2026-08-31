#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-web-e2e-boot.sh — battery for scripts/lib/engine-boot.sh.
#
# It sources the SUBJECT ITSELF and drives it with fake engines: a stranger already on the
# port, a binary that dies on start, one that never answers, one healthy but tokenless, and
# one that works. No pnpm, no web bundle, no Go build, no real engine — which is the whole
# point, because inline in web-e2e.sh these paths could not be reached in under ten minutes
# and so were never reached at all.
#
# WHAT IT REFUSES TO DO: pass by accident. Every case asserts on the MESSAGE as well as the
# status, because the defect being closed produced the right exit code eventually and the
# wrong explanation always — a run that died saying "could not read the one-time setup
# token" when the cause was a squatter from a session closed a day earlier.
#
# WHAT THIS BATTERY DOES NOT COVER, said here rather than discovered later. The subject's
# THIRD check — the post-flight "health passed but our child is gone" — has NO case, and a
# mutation run on 2026-08-08 confirmed it: replacing it with `if false` leaves this battery
# GREEN. It is not covered because it cannot be reached deterministically: the loop tests
# liveness BEFORE health on every iteration, so the only way to arrive at the post-flight
# with a dead child is for that child to die inside the one curl call between them. Every
# construction attempted lands in the liveness leg instead.
#
# It is KEPT — a race you cannot schedule is still a race, and the cost is one kill -0 —
# but it is UNVERIFIED, and a leg that has only ever been seen passing has never been seen
# working. Do not read this battery's green as covering it.
#
# Usage: scripts/test-web-e2e-boot.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=lib/engine-boot.sh
. "${ROOT}/scripts/lib/engine-boot.sh"

# This battery stands up FAKE engine binaries (bin-ok, bin-exits, …) and runs them, so its
# scratch space needs a real execute bit. A plain `mktemp -d` lands on /tmp, which is
# mounted noexec on this host: every shim died with rc=126 "Permission denied" and the
# battery reported 11 passed / 7 failed with messages about the parent not seeing the child
# — a diagnosis of the code under test for a fault in the directory it ran from. Since it
# is a pre-push fast lint, that rejected every push from this host (measured 2026-08-11 by
# on an idle box). lib/exec-workdir.sh has owned this fact since 2026-08-04; this is
# the copy of `mktemp -d` that never got the fix.
# shellcheck source=lib/exec-workdir.sh
. "${ROOT}/scripts/lib/exec-workdir.sh" || {
	echo "test-web-e2e-boot: cannot source lib/exec-workdir.sh — could not run. NOT a pass." >&2
	exit 2
}

PASS=0
FAIL=0
WORK="$(olivares_pick_exec_workdir web-e2e-boot-tests)" || {
	echo "test-web-e2e-boot: no scratch directory has a REAL execute bit, so the engine" >&2
	echo "  shims this battery depends on could not run. NOTHING was measured — not a pass." >&2
	exit 2
}
KIDS=()
cleanup() {
	for p in ${KIDS+"${KIDS[@]}"}; do kill "${p}" 2>/dev/null || true; done
	rm -rf "${WORK}"
}
trap cleanup EXIT

# ⛔ THE OLD LINE HERE CLAIMED "derived from the pid so two batteries on this shared host
#    never collide". IT WAS FALSE IN TWO WAYS, both measured 2026-08-24 after this battery
#    reported 3 spurious failures while a second copy of it ran alongside:
#
#      1. STRIDE 4, SPAN 42. Consecutive BASE_PORT values were 4 apart (`* 4`) but ONE
#         battery uses offsets up to +40, plus a gRPC neighbour — it spans 42 ports. Two
#         batteries whose pids differ by 1 shared THIRTY-EIGHT of those forty-two. Not a
#         rare collision — a collision BY CONSTRUCTION, and concurrently launched processes
#         have close pids, so it was the common case, not the corner.
#         (The first draft of this note said "18 of 22" from a span counted by hand. Case 0c
#          derives it and said 42. The hand count was wrong by a whole case.)
#      2. INSIDE THE EPHEMERAL RANGE. 41000..42996 sits within the kernel's
#         ip_local_port_range (32768-60999 here), so any random outbound socket could be
#         holding one of these ports with nothing to do with us.
#
#    Both are fixed by construction below rather than by a comment promising they will not
#    happen: the stride is now WIDER than the span, and the whole band sits ABOVE the
#    ephemeral range. Case 0c asserts the first invariant against the file itself.
#    The stride was FIRST set to 32 from a span I counted by hand as 22. Case 0c — which
#    derives the span from this file instead of trusting that count — immediately reported
#    `stride 32 < span 42`: there is a `BASE_PORT + 40` the hand count never saw. That is the
#    whole reason the case reads the source rather than a number somebody typed.
BATTERY_PORT_STRIDE=64
BASE_PORT=$(( 21000 + ($$ % 180) * BATTERY_PORT_STRIDE ))

ok() { PASS=$((PASS + 1)); printf '  ok   %s\n' "$1"; }
no() { FAIL=$((FAIL + 1)); printf '  FAIL %s\n     %s\n' "$1" "${2:-}"; }

# check <name> <expected-rc> <expected-substring> <actual-rc> <output>
check() {
	local name="$1" want_rc="$2" want_txt="$3" got_rc="$4" out="$5"
	if [ "${got_rc}" != "${want_rc}" ]; then
		no "${name}" "expected rc=${want_rc}, got ${got_rc}"
		return
	fi
	case "${out}" in
	*"${want_txt}"*) ok "${name}" ;;
	*) no "${name}" "rc was right but the message never said: ${want_txt}" ;;
	esac
}

# fake_engine <file> <mode> — a stand-in for `olivares serve`. It ignores its flags exactly
# as the real binary's caller does not care about them here; what matters is the shape of
# the process: does it stay up, does it answer /healthz, does it print a token.
fake_engine() {
	local path="$1" mode="$2"
	cat >"${path}" <<EOF
#!/usr/bin/env bash
# a fake engine in mode: ${mode}
port=""
while [ \$# -gt 0 ]; do
  case "\$1" in --listen) port="\${2##*:}"; shift 2 ;; *) shift ;; esac
done
case "${mode}" in
die)      echo "listen tcp 127.0.0.1:\${port}: bind: address already in use"; exit 1 ;;
deaf)     sleep 30 ;;
notoken)  exec python3 -c "
import http.server,sys
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(s): s.send_response(200); s.end_headers(); s.wfile.write(b'ok')
    def log_message(s,*a): pass
http.server.HTTPServer(('127.0.0.1', int('\${port}')), H).serve_forever()" ;;
good)     echo "one-time setup token: olst_TESTTOKEN42"
          exec python3 -c "
import http.server,sys
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(s): s.send_response(200); s.end_headers(); s.wfile.write(b'ok')
    def log_message(s,*a): pass
http.server.HTTPServer(('127.0.0.1', int('\${port}')), H).serve_forever()" ;;
esac
EOF
	chmod +x "${path}"
}

# squatter <port> — a stranger holding the port, standing in for the engines that leaked
# from a closed session and held 8458/8460/8462 for a day and five hours.
squatter() {
	python3 -c "
import socket,time
s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
s.bind(('127.0.0.1',$1)); s.listen(1); time.sleep(120)
" 2>/dev/null &
	KIDS+=($!)
	sleep 0.4
}

echo "==> engine-boot battery"

# --- 0. the instrument itself ---------------------------------------------------------
# A battery whose probe is broken reports every port free and passes case 1 for the wrong
# reason. This is asserted FIRST, and a failure here voids the run rather than colouring it.
if engine_probe_usable; then
	ok "the /dev/tcp probe detects a listener we bound ourselves"
else
	no "the /dev/tcp probe cannot see a listener we bound ourselves" \
		"every port would read as free; the rest of this battery would be meaningless"
	echo "==> ABORTING: the instrument is not usable, so nothing below was measured." >&2
	exit 2
fi

# --- 0c. one battery's port span must FIT INSIDE the stride between two batteries --------
# Derived from THIS FILE, never typed: a case that hardcodes "22" certifies the number it
# was written with and goes quiet the day somebody adds `BASE_PORT + 40`. The span is read
# back out of the source, +1 for the gRPC port the engine binds next to each HTTP one.
_span=$(grep -oE 'BASE_PORT \+ [0-9]+' "$0" | grep -oE '[0-9]+$' | sort -n | tail -1)
_span=$(( _span + 2 ))   # +1 for the gRPC neighbour, +1 because the range is inclusive
if [ "${BATTERY_PORT_STRIDE}" -ge "${_span}" ]; then
	ok "the port stride (${BATTERY_PORT_STRIDE}) covers one battery's span (${_span})"
else
	no "two concurrent batteries overlap by construction" \
		"stride ${BATTERY_PORT_STRIDE} < span ${_span}: pid and pid+1 share $(( _span - BATTERY_PORT_STRIDE )) port(s)"
fi
# And the WHOLE span must sit clear of the kernel's ephemeral range — above it or below it,
# either is fine — or a random outbound socket can be squatting a port we are about to call
# "held by a stranger". Checking only the low end would pass a band that starts below and
# runs into it.
_eph_lo=$(cut -f1 /proc/sys/net/ipv4/ip_local_port_range 2>/dev/null || echo 32768)
_eph_hi=$(cut -f2 /proc/sys/net/ipv4/ip_local_port_range 2>/dev/null || echo 60999)
_band_hi=$(( BASE_PORT + _span ))
if [ "${_band_hi}" -lt "${_eph_lo}" ] || [ "${BASE_PORT}" -gt "${_eph_hi}" ]; then
	ok "the battery's band ${BASE_PORT}-${_band_hi} is clear of the ephemeral range ${_eph_lo}-${_eph_hi}"
else
	no "the battery binds inside the kernel's ephemeral range" \
		"band ${BASE_PORT}-${_band_hi} overlaps ${_eph_lo}-${_eph_hi}: any outbound socket can hold one"
fi

# --- 0b. the instrument check must not accept a STRANGER's listener as its own --------
# MEASURED 2026-08-24, and it is this file's own header shape turned inward: until that day
# the control's bind error went to /dev/null, so `engine_probe_usable` could not tell "I
# bound the port" from "somebody else holds it". With a squatter on the hint port it
# returned 0/usable — validating the instrument against a foreign listener, which is
# exactly "our engine died on bind, the stranger answered the probe". The mirror case cost
# a real push: nobody answered, it concluded "/dev/tcp is broken" and exited 2 for what was
# only a busy port. The hint is `$$ % 900` and five lanes share this host, so collisions
# are ordinary — three colliding pairs were measured among live shells that morning.
#
# The assertion is NOT the return code: 0 is what the broken version returned too. It is
# WHICH PORT gets interrogated. A spy on engine_port_listener is the only thing that can
# tell the two apart.
#
# ⚠ The squatter is spawned OUT HERE, not inside the subshell below: `KIDS+=($!)` inside a
#   subshell mutates a COPY that dies with it, so the trap would never kill it and this
#   case would leak a python holding a port for two minutes. That trap is documented at the
#   foot of this file and was walked into again while writing this case.
P0="$(engine_free_port_hint 0)"
P1="$(engine_free_port_hint 1)"
SPY="${WORK}/probe-ports.txt"; : >"${SPY}"
squatter "${P0}"
(
	eval "orig_$(declare -f engine_port_listener)"
	engine_port_listener() { echo "$1" >>"${SPY}"; orig_engine_port_listener "$@"; }
	if engine_probe_usable && [ "$(tr -d '\n' <"${SPY}")" = "${P1}" ]; then
		exit 0
	fi
	printf '     interrogated: %s (P0=%s P1=%s)\n' "$(tr '\n' ' ' <"${SPY}")" "${P0}" "${P1}" >&2
	exit 1
)
if [ $? -eq 0 ]; then
	ok "with a stranger on its hint port, the probe binds a DIFFERENT port of its own"
else
	no "the probe accepted a listener it did not bind" \
		"a success would be a fact about the stranger, not about /dev/tcp"
fi

# --- 1. a stranger already on the port ------------------------------------------------
P=$((BASE_PORT))
squatter "${P}"
fake_engine "${WORK}/bin-good" good
PF="${WORK}/pids.$RANDOM"; : >"$PF"
out="$(boot_engine_at "${WORK}/bin-good" "${P}" "${WORK}/d1" "$PF" 2>&1)"
rc=$?
check "a squatter on the port is REFUSED before spawning" 1 "ALREADY has a listener" "${rc}" "${out}"
check "and the refusal names the measured cause"          1 "1 day 5 hours"           "${rc}" "${out}"
if [ ! -s "$PF" ]; then
	ok "nothing was spawned onto the occupied port"
else
	no "it spawned an engine anyway" "the pidfile has $(wc -l <"$PF") entr(y/ies)"
fi

# --- 1b. a stranger on the gRPC port ONLY -----------------------------------------------
# THE HALF THE OTHER LANE CONTRIBUTED, and the half a port-only pre-flight cannot see. The
# engine binds ${port} AND ${port}+1. With a squatter on the gRPC port alone, the old
# pre-flight found the HTTP port free, spawned, and the engine died on its SECOND bind —
# leaving a run that reports a token problem about the log of an engine that never started.
# The battery had no case for it because the behaviour did not exist here until the merge.
P=$((BASE_PORT + 20))
squatter "$((P + 1))"
PF="${WORK}/pids.$RANDOM"; : >"$PF"
out="$(boot_engine_at "${WORK}/bin-good" "${P}" "${WORK}/d1b" "$PF" 2>&1)"
rc=$?
check "a squatter on the gRPC port alone is REFUSED too" 1 "ALREADY has a listener" "${rc}" "${out}"
if printf '%s' "${out}" | grep -q "$((P + 1))"; then
	ok "and the refusal names the gRPC port, not the free one it checked first"
else
	no "the refusal does not name the busy port" "a message pointing at ${P} would send the reader to the wrong port"
fi
if [ ! -s "$PF" ]; then
	ok "nothing was spawned with the gRPC port occupied"
else
	no "it spawned an engine that will die on its second bind" "the pidfile has $(wc -l <"$PF") entr(y/ies)"
fi

# --- 2. our engine dies during boot ----------------------------------------------------
P=$((BASE_PORT + 2))
fake_engine "${WORK}/bin-die" die
PF="${WORK}/pids.$RANDOM"; : >"$PF"
start=$SECONDS
out="$(boot_engine_at "${WORK}/bin-die" "${P}" "${WORK}/d2" "$PF" 2>&1)"
rc=$?
elapsed=$((SECONDS - start))
check "a dead child is reported as a dead child"   1 "EXITED during boot"           "${rc}" "${out}"
check "and its bind error reaches the reader"      1 "address already in use"       "${rc}" "${out}"
if [ "${elapsed}" -lt 10 ]; then
	ok "it gives up at once (${elapsed}s) instead of burning the full 20 s wait"
else
	no "it waited ${elapsed}s for a process it could have seen was gone" "liveness check not effective"
fi

# --- 3. a stranger holds the port and our binary would die on bind --------------------
# THE 2026-08-08 SHAPE, and what this case can and cannot prove — rewritten after an
# adversarial review caught it passing for the wrong reason and costing 120 s to do it.
#
# WHAT IT PROVES: with a stranger already on the port, the run is refused and the operator is
# never handed "could not read the one-time setup token", which is the message that sent this
# defect to be diagnosed from scratch more than once.
#
# WHAT IT DOES NOT PROVE, said here instead of implied by the old title ("our engine dies AND
# a stranger answers"): the child never runs. The stranger binds BEFORE boot_engine_at, so the
# PRE-FLIGHT refuses and bin-die2 is never executed. The original shape — our engine dying on
# bind while a stranger answers the health probe — is now UNREACHABLE BY CONSTRUCTION, which
# is exactly what the pre-flight buys; reproducing it would need the stranger to appear in the
# window between the pre-flight and the bind, and that window cannot be scheduled from a
# shell. It is declared, not covered. (Same honesty as the post-flight note at the top.)
#
# TWO DEFECTS IN THE OLD FORM, both from the squatter living INSIDE the command substitution:
#   1. `KIDS+=($!)` mutated a copy that died with the subshell, so the trap never killed that
#      squatter — this suite leaked a process, which is the very defect it exists to close.
#   2. The backgrounded python inherited the write end of the substitution's pipe, so `$( )`
#      could not return until its `time.sleep(120)` ran out. That single line was 120 s of the
#      142 s this battery was said to cost, and that number was used to argue it into the
#      heavy lane.
P=$((BASE_PORT + 4))
fake_engine "${WORK}/bin-die2" die
PF="${WORK}/pids.$RANDOM"; : >"$PF"
squatter "${P}"                     # OUTSIDE the substitution: registered in KIDS, no pipe held
out="$(boot_engine_at "${WORK}/bin-die2" "${P}" "${WORK}/d3" "$PF" 2>&1)"
rc=$?
if [ "${rc}" = "1" ]; then
	case "${out}" in
	*"could not read the one-time setup token"*)
		no "a stranger on the port is still blamed on the token" "this is the 2026-08-08 message"
		;;
	*) ok "a stranger on the port is never blamed on the token" ;;
	esac
else
	no "a stranger on the port returned rc=${rc}" "expected 1"
fi
# And the child really did not run, which is what makes the note above a measurement rather
# than a claim: nothing was spawned, so the pidfile is empty.
if [ ! -s "$PF" ]; then
	ok "the pre-flight refused before the child ran, so the dying binary was never reached"
else
	no "a child was spawned onto the occupied port" "pidfile has $(wc -l <"$PF") entr(y/ies)"
fi

# --- 4. alive but never healthy --------------------------------------------------------
P=$((BASE_PORT + 6))
fake_engine "${WORK}/bin-deaf" deaf
PF="${WORK}/pids.$RANDOM"; : >"$PF"
out="$(boot_engine_at "${WORK}/bin-deaf" "${P}" "${WORK}/d4" "$PF" 2>&1)"
rc=$?
check "alive but deaf is reported as never accepting connections" 1 "never accepted connections" "${rc}" "${out}"
while read -r p _rest; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done <"$PF"

# --- 5. healthy, ours, but no token ----------------------------------------------------
P=$((BASE_PORT + 8))
fake_engine "${WORK}/bin-notoken" notoken
PF="${WORK}/pids.$RANDOM"; : >"$PF"
out="$(boot_engine_at "${WORK}/bin-notoken" "${P}" "${WORK}/d5" "$PF" 2>&1)"
rc=$?
check "a real token problem still says so"        1 "could not read the one-time setup token" "${rc}" "${out}"
check "and now that message can be trusted"       1 "The engine IS ours and IS healthy"       "${rc}" "${out}"
while read -r p _rest; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done <"$PF"

# --- 6. the happy path, because a gate tested in one direction is half a gate ----------
P=$((BASE_PORT + 10))
fake_engine "${WORK}/bin-ok" good
PF="${WORK}/pids.$RANDOM"; : >"$PF"
out="$(boot_engine_at "${WORK}/bin-ok" "${P}" "${WORK}/d6" "$PF" 2>&1)"
rc=$?
if [ "${rc}" = "0" ] && [ "${out}" = "olst_TESTTOKEN42" ]; then
	ok "a healthy engine of ours yields its token and nothing else on stdout"
else
	no "the happy path did not return the token" "rc=${rc} out=[${out}]"
fi
# THE COUNTERFACTUAL FOR THE LEAK. This is read from the PARENT after a command
# substitution, which is precisely where the old array append was lost. An empty pidfile
# here means the caller's trap has nothing to kill and the engine outlives the script —
# the shape that left three of them squatting ports for a day and five hours.
# FIRST FIELD ONLY. The line is `<pid> <starttime>` since the two lanes were merged, and
# `kill -0 "$(cat "$PF")"` would signal BOTH numbers — the second is not a pid of ours and
# on this shared host could be somebody's. It also went red for the WRONG reason: kill
# rejected the second word, not the child being absent.
recorded_pid="$(awk 'NR==1{print $1}' "$PF")"
recorded_start="$(awk 'NR==1{print $2}' "$PF")"
if [ "$(wc -l <"$PF")" = "1" ] && [ -n "${recorded_pid}" ] && kill -0 "${recorded_pid}" 2>/dev/null; then
	ok "the parent can see the child across the command substitution, so its trap owns it"
else
	no "the parent cannot see the child" "pidfile has $(wc -l <"$PF") line(s); cleanup would leak"
fi
# THE IDENTITY IS ASSERTED, not merely recorded. A line carrying a pid and an EMPTY second
# field passes a line count and is exactly the state web-e2e.sh's cleanup refuses to signal
# — so a battery that only counted lines would call the leak fixed while the trap declined
# to touch it. This is the case that covers what #625 contributed to the merged library.
#
# IT RUNS BEFORE THE KILL BELOW, and that is not style: placed after it, /proc has nothing
# left to compare against and the case fails reporting an empty start time for a child that
# was recorded perfectly. It WAS written after it, and this battery said so.
if [ -n "${recorded_start}" ] && [ "${recorded_start}" = "$(engine_proc_starttime "${recorded_pid}")" ]; then
	ok "the line carries the child's start time, so cleanup can prove the pid is still ours"
else
	no "the recorded identity is missing or wrong" "got '${recorded_start}', /proc says '$(engine_proc_starttime "${recorded_pid}")'"
fi
while read -r p _rest; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done <"$PF"

echo
echo "engine-boot battery: ${PASS} passed, ${FAIL} failed"
[ "${FAIL}" -eq 0 ] || exit 1
