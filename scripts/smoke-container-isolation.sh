#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# smoke-container-isolation.sh — proves isolation=container is wired end to end on the real
# `olivares serve`: the engine finds a container runtime, `olivares session start --isolation
# container` is accepted, the module hands the container runner its mounts, the runner starts the
# runtime CLI with a hardened `run`, bridges stdio, keeps the secret out of argv and removes the
# container when the session ends.
#
# HERMETIC, AND HONEST ABOUT WHAT THAT MEANS. A fake `podman` (a shell script) and a fake `claude`
# stand in. What this proves is everything on Olivares' side of the CLI: arguments, environment,
# mounts, the stdio bridge, the lifecycle and the cleanup call. It does NOT prove that podman or
# Docker accept those arguments; that needs a real runtime (docs: Topology 4).
#
# Usage:  scripts/smoke-container-isolation.sh
#         SMOKE_REAL_RUNTIME=podman|docker [SMOKE_IMAGE=<image with /bin/sh>] scripts/smoke-container-isolation.sh
# The second form uses the real runtime on this host instead of the stand-in: the proof that podman
# or Docker accept the arguments. It still uses the stand-in claude, which needs only /bin/sh in the image.
# Requires: go (to build if ./bin/olivares is absent), curl, python3.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
. "$ROOT/scripts/lib/build-bin.sh"
BIN="${OLIVARES_BIN:-$ROOT/bin/olivares}"
PORT="${SMOKE_PORT:-8971}"
GRPC_PORT="$((PORT + 1))"

PID=""
WORK=""
BINDIR=""
EXEC_TMP=""
cleanup() {
  [ -n "$PID" ] && kill "$PID" 2>/dev/null || true
  [ -n "$PID" ] && wait "$PID" 2>/dev/null || true
  rm -rf "${WORK:-}" "${BINDIR:-}" ${EXEC_TMP:+"$EXEC_TMP"}
}
trap cleanup EXIT

WORK="$(mktemp -d)"
BASE="http://127.0.0.1:$PORT"

# A directory the fake tools can be EXECUTED from: some hosts mount /tmp noexec.
pick_execdir() {
  for d in "${OLIVARES_SMOKE_EXECDIR:-}" /var/tmp "${HOME:-}" "$ROOT/bin"; do
    [ -n "$d" ] && [ -d "$d" ] || continue
    t="$d/.olv-exec-test.$$"
    if printf '#!/bin/sh\nexit 0\n' >"$t" 2>/dev/null && chmod +x "$t" 2>/dev/null && "$t" 2>/dev/null; then
      rm -f "$t"; echo "$d"; return 0
    fi
    rm -f "$t" 2>/dev/null || true
  done
  return 1
}
EXECDIR="$(pick_execdir)" || { echo "FAIL: no exec-able dir for the fake tools (set OLIVARES_SMOKE_EXECDIR)"; exit 1; }
BINDIR="$(mktemp -d "$EXECDIR/olivares-container-bin.XXXXXX")"

# The engine extracts its connector plugins to $TMPDIR and runs them: it needs one that executes.
# shellcheck source=/dev/null
. "$ROOT/scripts/lib/exec-tmpdir.sh"
EXEC_TMP="$(olivares_exec_tmpdir)" || {
  echo "$(basename "$0"): CANNOT START: no temporary directory allows execution (set OLIVARES_EXEC_TMPDIR)." >&2
  exit 2
}

fail() { echo "FAIL: $*" >&2; exit 1; }
note() { echo "==> $*"; }
assert_eq() { if [ "$2" != "$3" ]; then fail "$1: got '$2', want '$3'"; fi; echo "    ok: $1 = $2"; }
assert_nonempty() { if [ -z "$2" ]; then fail "$1: empty"; fi; echo "    ok: $1 = $2"; }
jget() { python3 -c "import sys,json;print(json.load(sys.stdin).get('$1',''))"; }

wait_health() {
  for _ in $(seq 1 80); do
    curl -sf "$BASE/healthz" >/dev/null 2>&1 && return 0
    sleep 0.25
  done
  return 1
}

# --- a fake `claude`: answers the sign-in question, then speaks stream-json -----------------
cat >"$BINDIR/fake-claude" <<'SH'
#!/bin/sh
# Shell only, so the same file runs inside any image that has /bin/sh.
if [ "$1" = auth ] && [ "$2" = status ]; then
  # The engine asks the tool whether it is signed in before it offers it for a session.
  printf '{"loggedIn":true,"authMethod":"claude.ai","email":"smoke@example.test","subscriptionType":"max"}\n'
  exit 0
fi
printf '{"type":"system","subtype":"init","session_id":"smoke-session-0001"}\n'
while IFS= read -r line; do
  printf '{"type":"assistant","message":{"role":"assistant","content":"ack"}}\n'
done
exit 0
SH
chmod +x "$BINDIR/fake-claude"
ln -s fake-claude "$BINDIR/claude"   # the engine finds the tool by name, as on a real host

# --- the runtime: a stand-in `podman` that records what it was asked and runs the program as a
# container would (the environment the engine gave the CLI IS the program's environment, because `-e
# NAME` copies it from there), or, with SMOKE_REAL_RUNTIME, the real one on this host.
REAL_RT="${SMOKE_REAL_RUNTIME:-}"
LOGD="$WORK/podman-log"
mkdir -p "$LOGD"
if [ -n "$REAL_RT" ]; then
  RUNTIME_NAME="$REAL_RT"
  IMAGE="${SMOKE_IMAGE:-docker.io/library/debian:13-slim}"
  command -v "$REAL_RT" >/dev/null || { echo "FAIL: SMOKE_REAL_RUNTIME=$REAL_RT is not installed"; exit 2; }
  # The engine runs with a throwaway HOME, and rootless podman keeps its images and containers under
  # it: the session would start in an empty store this script never queries. Keep the caller's own.
  export XDG_DATA_HOME="${XDG_DATA_HOME:-$HOME/.local/share}" XDG_CONFIG_HOME="${XDG_CONFIG_HOME:-$HOME/.config}"
else
  RUNTIME_NAME=podman
  IMAGE="smoke.invalid/olivares-session:test"
  cat >"$BINDIR/podman" <<SH
#!/bin/sh
LOG='$LOGD'
case "\$1" in
  rm) printf '%s\n' "\$@" >> "\$LOG/rm.log"; exit 0 ;;
  run)
    printf '%s\n' "\$@" > "\$LOG/argv"
    shift
    while [ "\$1" != '$IMAGE' ]; do shift; done
    shift
    exec "\$@" ;;
esac
exit 64
SH
  chmod +x "$BINDIR/podman"
fi

printf 'smoke-dummy-bearer-not-a-real-secret' >"$WORK/session-token"
mkdir -p "$WORK/ws" "$WORK/home/.claude"   # the folder, and the signed-in tool's configuration home
REAL_WS="$(cd "$WORK/ws" && pwd -P)"

if [ ! -x "$BIN" ]; then
  note "building $BIN (CGO_ENABLED=0 go build ./cmd/olivares)"
  build_olivares_bin "$BIN"
fi

note "serve (insecure) with a stand-in podman first on PATH"
PATH="$BINDIR:$PATH" \
HOME="$WORK/home" \
OLIVARES_SESSION_CONTAINER_RUNTIME="$RUNTIME_NAME" \
OLIVARES_SESSION_CONTAINER_IMAGE="$IMAGE" \
OLIVARES_SESSION_RUNTIME_TOKEN_FILE="$WORK/session-token" \
OLIVARES_SESSION_RUNTIME_TOKEN_TTL="15m" \
TMPDIR="${EXEC_TMP:-${TMPDIR:-/tmp}}" \
"$BIN" serve --insecure \
  --listen "127.0.0.1:$PORT" --grpc-listen "127.0.0.1:$GRPC_PORT" \
  --data-dir "$WORK/data" >"$WORK/boot.log" 2>&1 &
PID=$!
wait_health || { cat "$WORK/boot.log" >&2; fail "engine never became healthy"; }
grep -q "isolation=container is available" "$WORK/boot.log" || { cat "$WORK/boot.log" >&2; fail "the engine did not announce container isolation"; }
echo "    ok: the engine found the runtime and announced isolation=container"

SETUP_TOKEN="$(grep -oE 'olst_[A-Z0-9]+' "$WORK/boot.log" | head -1)"
[ -n "$SETUP_TOKEN" ] || { cat "$WORK/boot.log" >&2; fail "no one-time setup token on stdout"; }
curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' \
  -d "{\"token\":\"$SETUP_TOKEN\",\"email\":\"admin@local\",\"password\":\"correct-horse-battery-staple\"}" >/dev/null \
  || fail "setup failed"
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' | jget token)"
[ -n "$TOKEN" ] || fail "login returned no token"
TENANT="$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sf "$BASE/v1/auth/whoami" -H @- \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["grants"][0]["tenant"])')"
[ -n "$TENANT" ] || fail "could not resolve the setup organization"

export OLIVARES_SERVER_URL="$BASE" OLIVARES_TOKEN="$TOKEN" OLIVARES_TENANT="$TENANT"
olv() { "$BIN" agent "$@"; }   # the operator CLI for session state

# A small client for what the CLI has no verb for: the second administrator and the negative call.
api() { python3 - "$@" <<'PY'
import json, os, sys, urllib.error, urllib.request, uuid
method, path, body, token = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
req = urllib.request.Request(os.environ["OLIVARES_SERVER_URL"] + path, method=method,
                             data=body.encode() if body else None)
req.add_header("Authorization", "Bearer " + token)
req.add_header("X-Olivares-Tenant", os.environ["OLIVARES_TENANT"])
req.add_header("Idempotency-Key", str(uuid.uuid4()))
if body:
    req.add_header("Content-Type", "application/json")
try:
    with urllib.request.urlopen(req, timeout=30) as r:
        print(r.status); print(r.read().decode())
except urllib.error.HTTPError as e:
    print(e.code); print(e.read().decode())
PY
}

note "start a session in a container the way a person does: olivares session start --isolation container"
START_OUT="$("$BIN" session start "$WORK/ws" --isolation container --permission full -o json 2>"$WORK/start.err")" \
  || { cat "$WORK/start.err" >&2; echo "$START_OUT" >&2; tail -30 "$WORK/boot.log" >&2; fail "session start was refused"; }
RUN_REF="$(printf '%s' "$START_OUT" | jget run_ref)"
assert_nonempty "run_ref" "$RUN_REF"

STATE="$(olv session get "$RUN_REF" | jget state)"
if [ "$STATE" = "waiting_approval" ]; then
  note "the launch waits for a second administrator: approve it as one"
  api POST /v1/users "{\"email\":\"second@local\",\"display_name\":\"Second\",\"password\":\"correct-horse-battery-staple\",\"role\":\"admin\",\"tenant\":\"$TENANT\",\"superadmin\":false}" "$TOKEN" >/dev/null
  TOKEN2="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
    -d '{"email":"second@local","password":"correct-horse-battery-staple"}' | jget token)"
  [ -n "$TOKEN2" ] || fail "the second administrator could not log in"
  APPROVAL="$(api GET /v1/m/governance/approvals "" "$TOKEN2" | tail -1 | python3 -c '
import sys, json
for a in json.load(sys.stdin).get("items", []):
    if a.get("action") == "sessions.run.launch" and (a.get("effective_status") or a.get("status")) == "pending":
        print(a["id"]); break')"
  [ -n "$APPROVAL" ] || fail "no pending launch approval"
  api POST "/v1/m/governance/approvals/$APPROVAL/decisions" '{"decision":"approve","note":"smoke"}' "$TOKEN2" >/dev/null
fi

SID=""; STATE=""; ISO=""
for _ in $(seq 1 60); do
  J="$(olv session get "$RUN_REF")"
  STATE="$(printf '%s' "$J" | jget state)"
  SID="$(printf '%s' "$J" | jget claude_session_id)"
  ISO="$(printf '%s' "$J" | jget isolation)"
  { [ "$STATE" = "running" ] || [ "$STATE" = "idle" ]; } && [ -n "$SID" ] && break
  case "$STATE" in failed|stopped) echo "$J" >&2; tail -30 "$WORK/boot.log" >&2; fail "the session ended as $STATE" ;; esac
  sleep 0.5
done
assert_eq "session state" "$STATE" "running"
assert_eq "isolation on the row" "$ISO" "container"
assert_eq "captured session id (stdio bridged through the CLI)" "$SID" "smoke-session-0001"

if [ -n "$REAL_RT" ]; then
  note "the real $REAL_RT runs the session"
  SEEN=""
  for _ in $(seq 1 20); do
    SEEN="$("$REAL_RT" ps --filter label=ai.olivares.session --format '{{.Names}}' | head -1)"
    [ -n "$SEEN" ] && break
    sleep 0.5
  done
  case "$SEEN" in olivares-session-*) echo "    ok: container $SEEN is running under $REAL_RT" ;; *) fail "no running container carries the session label" ;; esac
  NAME="$SEEN"
else
  note "what Olivares asked of the container CLI"
  ARGV="$LOGD/argv"
  [ -s "$ARGV" ] || fail "the container CLI was never started"
  for want in run --rm -i --read-only --cap-drop=ALL --security-opt=no-new-privileges "$IMAGE" "$REAL_WS:/workspace"; do
    grep -qxF -- "$want" "$ARGV" || { cat "$ARGV" >&2; fail "argv lacks '$want'"; }
  done
  # the host's program, by the path the engine resolved it to (the link or its target)
  grep -qE "^$BINDIR/(fake-)?claude\$" "$ARGV" || { cat "$ARGV" >&2; fail "argv lacks the host's tool program"; }
  # The stock engine governs tool calls with hooks that call it on its loopback: the container
  # carries the hook program and shares the host network to reach it.
  grep -qxF -- "--network=host" "$ARGV" || { cat "$ARGV" >&2; fail "a hook-governed container does not share the host network"; }
  grep -qE '^.*/olivares:.*/olivares:ro$' "$ARGV" || { cat "$ARGV" >&2; fail "the hook program is not mounted read-only"; }
  echo "    ok: hardened run, image, the host's program, the folder mount, the hook program and the network it needs"
  if grep -qF 'smoke-dummy-bearer-not-a-real-secret' "$ARGV"; then fail "the session secret is in argv"; fi
  echo "    ok: no secret value in argv"
  NAME_LINE="$(grep -n -x -- '--name' "$ARGV" | head -1 | cut -d: -f1)"
  NAME="$(sed -n "$((NAME_LINE + 1))p" "$ARGV")"
  case "$NAME" in olivares-session-*) echo "    ok: container name $NAME" ;; *) fail "container name '$NAME'" ;; esac

fi

note "events, input through the bridge, stop, cleanup"
olv session events "$RUN_REF" | python3 -c '
import sys, json
events = {it.get("event"): it for it in json.load(sys.stdin).get("items", [])}
for want in ("created", "launched"):
    assert want in events and events[want].get("payload_hash"), f"event {want} missing or not anchored"
print("    ok: lifecycle anchored")
'
olv session input "$RUN_REF" --line '{"type":"user","message":{"role":"user","content":"hi"}}'
sleep 0.5
if command -v timeout >/dev/null 2>&1; then
  timeout 4 "$BIN" agent session attach "$RUN_REF" >"$WORK/attach.out" 2>/dev/null || true
  grep -q 'ack' "$WORK/attach.out" || fail "the echoed frame did not come back through the container CLI"
  echo "    ok: input reached the program and its answer came back"
fi
olv session stop "$RUN_REF" >/dev/null
STATE=""
for _ in $(seq 1 40); do
  STATE="$(olv session get "$RUN_REF" | jget state)"
  [ "$STATE" = "stopped" ] && break
  sleep 0.25
done
assert_eq "stopped state" "$STATE" "stopped"
if [ -n "$REAL_RT" ]; then
  GONE=""
  for _ in $(seq 1 40); do
    "$REAL_RT" ps -a --format '{{.Names}}' | grep -qx "$NAME" || { GONE=yes; break; }
    sleep 0.5
  done
  [ -n "$GONE" ] || fail "the container $NAME still exists after the session ended"
else
  for _ in $(seq 1 20); do [ -s "$LOGD/rm.log" ] && break; sleep 0.25; done
  grep -qxF "$NAME" "$LOGD/rm.log" || fail "the container $NAME was not removed after the session ended"
fi
echo "    ok: the container is gone after the session ended"

note "sandbox stays refused"
RESP="$(api POST /v1/m/sessions/runs '{"transport":"stream-json","isolation":"sandbox"}' "$TOKEN")"
assert_eq "sandbox launch status" "$(printf '%s' "$RESP" | head -1)" "422"

kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; PID=""
echo
echo "PASS — isolation=container is wired on the real binary (against a stand-in CLI):"
echo "  runtime found · session in a container · hardened run · no secret in argv · stdio bridged · container removed."
