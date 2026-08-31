#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# quickstart-argv-smoke.sh — RUN the first command this product tells a new user to run, as a
# user runs it: `./bin/olivares quickstart`, by argv, on a built binary, from THIS tree.
#
# ⛔ WHY IT EXISTS, and it is not a duplicate of scripts/quickstart-smoke.sh. That script
# reproduces the documentation page's hero, which boots the engine through
# `serve --insecure --seed-demo`. It is a different VERB with different defaults, and measured
# 2026-08-27 the tree contained ZERO executions of `olivares quickstart` — the command README.md
# and INSTALL.md name as the first thing to run. The value matrix marked that row NOT MEASURED
# and was right.
#
# ⛔ AND WHY IT IS A SCRIPT AND NOT ONLY A GO TEST. The Go test next to the command
# (cmd/olivares/cmd_quickstart_e2e_test.go) proves the verb in-process, which is the fast,
# hermetic half. This one proves the OTHER half, and it is the half the export needs: that the
# tree a stranger clones BUILDS a binary and that THAT BINARY starts and reaches first-run
# setup. Those are different claims — the acceptance of the public export ran neither until
# today (it ran `task lint:spdx lint:boundary` and nothing else), so "a user who clones the
# public repo has a product that starts" was an inference from the hub, never a measurement of
# the export.
#
# Usage:  bash scripts/quickstart-argv-smoke.sh
# Env:    OLIVARES_BIN (default ./bin/olivares — built if absent)
#         QUICKSTART_SMOKE_PORT (default: a free port chosen at run time)
#
# Three answers, and the third is the point: 0 CLEAN · 1 BROKEN · 2 COULD NOT LOOK (no
# toolchain, no free port, no curl — nothing was measured, and that is not a pass).

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
say() { printf '%s\n' "$*"; }
fail() { say "quickstart-argv-smoke: BROKEN — $*" >&2; exit 1; }
cannot() { say "quickstart-argv-smoke: COULD NOT LOOK — $*" >&2; exit 2; }
note() { printf '==> %s\n' "$*"; }

BIN="${OLIVARES_BIN:-$ROOT/bin/olivares}"
WORK=""
PID=""
cleanup() {
  # ⛔ BY PID, NEVER BY COMMAND PATTERN. A pattern kill matches this script's own command line,
  # and on a shared box it matches other lanes' processes too; this repository has a measured
  # case of a session killing its own shell that way.
  #
  # ⛔ AND IT ESCALATES. The first version sent ONE TERM and then `wait` with no deadline, so a
  # process that ignored the signal — or that took its time draining — hung the trap forever,
  # which on a shared box means an engine holding a port and a work directory nobody reclaims.
  # An exit path with an unbounded wait is not a cleanup. Grace, then KILL, then give up loudly.
  if [ -n "$PID" ]; then
    kill -TERM "$PID" 2>/dev/null || true
    for _ in $(seq 1 40); do
      kill -0 "$PID" 2>/dev/null || break
      sleep 0.25
    done
    if kill -0 "$PID" 2>/dev/null; then
      say "quickstart-argv-smoke: pid $PID ignored TERM after 10s; sending KILL." >&2
      kill -KILL "$PID" 2>/dev/null || true
    fi
    wait "$PID" 2>/dev/null || true
  fi
  [ -n "$WORK" ] && rm -rf "$WORK"
}
trap cleanup EXIT HUP INT TERM

command -v curl >/dev/null 2>&1 || cannot "curl is not on PATH; the engine's liveness cannot be checked"
command -v python3 >/dev/null 2>&1 || cannot "python3 is not on PATH; free ports cannot be chosen"

if [ ! -x "$BIN" ]; then
  command -v go >/dev/null 2>&1 || cannot "no $BIN and no go toolchain to build one"
  note "building $BIN (the same flag set every other build path uses)"
  # ONE place owns the build flags — writing them out longhand here is what let five scripts
  # drift from build:bin.
  # shellcheck source=/dev/null
  . "$ROOT/scripts/lib/build-bin.sh" || cannot "cannot source scripts/lib/build-bin.sh"
  # ⛔ WITH A DEADLINE. Every poll below is bounded and this was not, so the one step that can
  # genuinely hang — a compile on a saturated box, a toolchain fetch that stalls — was the one
  # with no limit. `timeout` reports 124, which is a build that did not finish: a finding about
  # the tree only if the tree is what took too long, so it is named as what it is.
  bld_rc=0
  ( cd "$ROOT" && timeout "${QUICKSTART_BUILD_TIMEOUT_S:-1800}" bash -c '. scripts/lib/build-bin.sh && build_olivares_bin "$1"' _ "$BIN" ) || bld_rc=$?
  if [ "$bld_rc" = 124 ]; then
    cannot "the build did not finish within ${QUICKSTART_BUILD_TIMEOUT_S:-1800}s; nothing was measured"
  fi
  [ "$bld_rc" = 0 ] || fail "the tree does not build its own binary (exit $bld_rc)"
fi
# ⛔ A BUILD THAT REPORTED SUCCESS AND LEFT NO EXECUTABLE IS BROKEN, NOT UNMEASURED. This said
# `cannot` and that is the wrong answer: the contract was measured and violated — the compiler
# said yes and the artifact is not there. Reserving 2 for it would file a proven defect under
# "I could not look".
[ -x "$BIN" ] || fail "the build reported success and produced no executable at $BIN"

# A free port, asked of the kernel rather than assumed: a hardcoded one turns another lane's
# listener into a failure of this script.
pick_port() {
  python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()' 2>/dev/null
}
PORT="${QUICKSTART_SMOKE_PORT:-$(pick_port)}"
[ -n "$PORT" ] || cannot "could not obtain a free TCP port"
GRPC_PORT="$(pick_port)"
{ [ -n "$GRPC_PORT" ] && [ "$GRPC_PORT" != "$PORT" ]; } || cannot "could not obtain two distinct free ports"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/quickstart-argv.XXXXXX")" || cannot "cannot create a work directory"
LOG="$WORK/quickstart.log"
DATA="$WORK/data"

note "running: $BIN quickstart --quiet --data-dir $DATA --listen 127.0.0.1:$PORT"
"$BIN" quickstart --quiet \
  --data-dir "$DATA" \
  --listen "127.0.0.1:$PORT" \
  --grpc-listen "127.0.0.1:$GRPC_PORT" >"$LOG" 2>&1 &
PID=$!

# The setup token is the LAST thing the panel prints, so waiting for it is waiting for the whole
# first-run path: header, boot, engine up, announce. Its shape is the one core/secure/setup.go
# mints (olst_ + unpadded base32 over 32 bytes = 52 characters).
TOKEN_RE='olst_[A-Z2-7]{52}'
deadline=$(( $(date +%s) + 420 ))
token=""
while [ "$(date +%s)" -lt "$deadline" ]; do
  if ! kill -0 "$PID" 2>/dev/null; then
    say "--- quickstart output ---" >&2; cat "$LOG" >&2
    fail "quickstart exited before it printed a setup token"
  fi
  token="$(command grep -oE "$TOKEN_RE" "$LOG" 2>/dev/null | head -1)"
  [ -n "$token" ] && break
  sleep 0.25
done
if [ -z "$token" ]; then
  say "--- quickstart output ---" >&2; cat "$LOG" >&2
  fail "no setup token of the shape core/secure mints appeared within 420s (the first boot of a fresh data directory builds the whole module schema before the panel can be minted; if this is a slow shared box, that is what timed out)"
fi

# What a first-time operator must actually see, each assertion a distinct way this rots.
command grep -qF '=== OLIVARES AI — FIRST RUN ===' "$LOG" || {
  cat "$LOG" >&2; fail "the first-run header is missing"
}
command grep -qF '=== WELCOME TO OLIVARES AI ===' "$LOG" || {
  cat "$LOG" >&2; fail "the welcome panel is missing"
}
command grep -qF "https://127.0.0.1:$PORT" "$LOG" || {
  cat "$LOG" >&2; fail "the panel does not point at the loopback HTTPS console it started"
}
command grep -qF 'one-time token' "$LOG" || {
  cat "$LOG" >&2; fail "the panel does not tell the operator what the token is for"
}
[ -s "$DATA/setup.token" ] || fail "the engine printed a token but persisted no setup-token store"

# And the engine is genuinely SERVING, not merely printing: quickstart is TLS-on with a
# self-signed certificate on first boot, so -k is the operator's own first experience.
health=""
deadline=$(( $(date +%s) + 60 ))
while [ "$(date +%s)" -lt "$deadline" ]; do
  health="$(curl -sk -o /dev/null -w "%{http_code}" --max-time 5 "https://127.0.0.1:$PORT/healthz" 2>/dev/null)"
  [ "$health" = "200" ] && break
  sleep 0.5
done
[ "$health" = "200" ] || {
  cat "$LOG" >&2
  fail "the console did not answer /healthz over HTTPS (last status: ${health:-none})"
}

say "quickstart-argv-smoke: CLEAN — olivares quickstart started, printed its header and welcome"
say "  panel, minted a setup token (${token:0:9}..., stored in $DATA/setup.token) and served"
say "  https://127.0.0.1:$PORT/healthz with 200."
exit 0
