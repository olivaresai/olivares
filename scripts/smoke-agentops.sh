#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# smoke-agentops.sh — proves the "Operate Claude Code" CO-DEPLOYMENT brings up a
# GOVERNED session end to end (FASE V). It is the bring-up reproducibility
# contract: if the native co-deployment stops being governable — the engine can't
# conduct claude, the lifecycle ledger stops anchoring, or attach stops bridging —
# this fails, so the topology can never quietly rot.
#
# It runs HERMETICALLY: a fake `claude` that speaks stream-json (NO network, NO real
# auth, NO Anthropic account) stands in for the real binary behind the SAME procRunner
# seam the production image uses. What it proves, against the real `olivares serve`:
#   1. the engine launches a governed session (deny-closed credential satisfied);
#   2. the stream-json init is BRIDGED + parsed → the resumable claude_session_id is captured;
#   3. the lifecycle is LEDGERED + anchored (created/launched events carry a payload_hash);
#   4. attach replays the bridged I/O (the governed stream, not a lossy view);
#   5. stop tears the process group down → state `stopped`, ledgered.
#
# This is topology (2) native-native, runnable in CI without secrets. Topology (1)
# docker-docker uses the SAME flow against the agentops image + a real claude with
# operator auth — see the how-to (run-claude-code-with-olivares.md); it is not
# auto-run here because it needs a Docker daemon and a real inference credential.
#
# Usage:  scripts/smoke-agentops.sh
# Requires: go (to build if ./bin/olivares is absent), curl, python3.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# The engine binary is built ONE way outside a release: see scripts/lib/build-bin.sh.
# Writing the flags out longhand here is what let five scripts drift from build:bin.
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/build-bin.sh"
BIN="${OLIVARES_BIN:-$ROOT/bin/olivares}"
PORT="${SMOKE_PORT:-8961}"
GRPC_PORT="$((PORT + 1))"
# ⛔ EL TRAP SE ARMA ANTES DEL PRIMER TEMPORAL (the reviewer, A-02). Estaba DESPUES en los cuatro, y
#    un fallo entre el `mktemp` y el `trap` —medido con rc 7— dejaba los cuatro temporales
#    vivos, sin dueño y sin nadie que los borrara. La funcion de limpieza tolera que sus
#    variables aun no existan (todas se leen con `${VAR:-}`), asi que armarla temprano no
#    cuesta nada y cierra la ventana entera.
cleanup() {
  [ -n "$PID" ] && kill "$PID" 2>/dev/null || true
  [ -n "$PID" ] && wait "$PID" 2>/dev/null || true
  rm -rf "$WORK" "${BINDIR:-}" ${EXEC_TMP:+"$EXEC_TMP"}
}
trap cleanup EXIT

WORK="$(mktemp -d)"
BASE="http://127.0.0.1:$PORT"
PID=""

# Pick a directory we can actually EXEC from: some hardened hosts mount /tmp (where
# mktemp lands) noexec, which would block launching the fake claude. Real deployments
# put claude under /usr/bin, so this only affects the test fixture's location.
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
EXECDIR="$(pick_execdir)" || { echo "FAIL: no exec-able dir for the fake claude (set OLIVARES_SMOKE_EXECDIR)"; exit 1; }
BINDIR="$(mktemp -d "$EXECDIR/olivares-agentops-bin.XXXXXX")"


# ⛔ ESTO VA DESPUES DEL `trap`, y no es orden estetico (the reviewer, A-02). El scratch se crea
#    aqui; si se creara ANTES de que la limpieza este armada, cualquier fallo en el setup
#    intermedio —otro `mktemp`, un `curl`— lo dejaria colgado sin dueño. La ventana era de
#    18 a 41 lineas segun el guion. Ahora no hay ventana.
# ⛔ EL MOTOR EXTRAE SUS PLUGINS A `$TMPDIR` Y LOS EJECUTA (`cmd/olivares/boot.go:1423`), y en estos
#    contenedores `/tmp` esta montado noexec, asi que el `fork/exec` falla con el bit puesto y la
#    fuente se rechaza en la recarga. Se SONDEA un directorio que ejecute —no se deduce de la ruta—
#    y se le pasa al motor. Si ninguno ejecuta se DICE, porque el silencio aqui se lee como «no
#    hacia falta». Detalle y alcance honesto, en la cabecera de la libreria.
# shellcheck source=/dev/null
. "$ROOT/scripts/lib/exec-tmpdir.sh"
# ⛔ SE REHUSA, NO SE AVISA (the reviewer, A-01). La version anterior imprimia el aviso, vaciaba
#    `EXEC_TMP` y ARRANCABA IGUAL con `/tmp`: es decir, el seguro no aseguraba nada — el motor
#    volvia a extraer sus plugins donde no puede ejecutarlos y la fuente se rechazaba en la
#    recarga, en silencio y deny-closed, que es exactamente el fallo que este guion existe para
#    cerrar. Un control que avisa y sigue no es un control: es un comentario con `echo`.
EXEC_TMP="$(olivares_exec_tmpdir)" || {
  echo "$(basename "$0"): ⛔ NO ARRANCO: ningun directorio temporal EJECUTA." >&2
  echo "   El motor extrae sus plugins de conector a \$TMPDIR y los LANZA" >&2
  echo "   (cmd/olivares/boot.go), y aqui ninguno de los candidatos permite execve —" >&2
  echo "   tipicamente porque /tmp esta montado noexec. Arrancar igual dejaria el plano de" >&2
  echo "   conectores muerto SIN decirlo." >&2
  echo "   Remedio: exporta OLIVARES_EXEC_TMPDIR a un directorio que ejecute." >&2
  exit 2
}

fail() { echo "FAIL: $*" >&2; exit 1; }
note() { echo "==> $*"; }
assert_eq() { if [ "$2" != "$3" ]; then fail "$1: got '$2', want '$3'"; fi; echo "    ok: $1 = $2"; }
assert_nonempty() { if [ -z "$2" ]; then fail "$1: empty"; fi; echo "    ok: $1 = $2"; }

wait_health() {
  for _ in $(seq 1 80); do
    curl -sf "$BASE/healthz" >/dev/null 2>&1 && return 0
    sleep 0.25
  done
  return 1
}

# jget FIELD < json  — extract a top-level string field with python3 (no jq dep).
jget() { python3 -c "import sys,json;print(json.load(sys.stdin).get('$1',''))"; }

# --- a fake `claude` that speaks the headless stream-json protocol -----------------
# Emits the session-establishing init line (the bridge captures session_id from it),
# then echoes one assistant frame per stdin line, unbuffered, until stdin closes (the
# engine's stop closes stdin → the loop ends → clean exit). readline() avoids the
# read-ahead a `for line in stdin` would buffer, so the echo appears immediately.
cat >"$BINDIR/fake-claude" <<'PY'
#!/usr/bin/env python3
import sys, json
sys.stdout.write(json.dumps({"type": "system", "subtype": "init", "session_id": "smoke-session-0001"}) + "\n")
sys.stdout.flush()
while True:
    line = sys.stdin.readline()
    if not line:
        break
    sys.stdout.write(json.dumps({"type": "assistant", "message": {"role": "assistant", "content": "ack"}}) + "\n")
    sys.stdout.flush()
sys.exit(0)
PY
chmod +x "$BINDIR/fake-claude"

# A dummy short-lived inference token (the fake claude ignores ANTHROPIC_AUTH_TOKEN;
# its presence satisfies the engine's DENY-CLOSED CredentialSource so a launch is
# allowed). NEVER a real secret — this is the hermetic stand-in for a WIF refresher.
printf 'smoke-dummy-bearer-not-a-real-secret' >"$WORK/session-token"
mkdir -p "$WORK/ws"   # the governed workspace root (must exist; the server canonicalizes it)

if [ ! -x "$BIN" ]; then
  note "building $BIN (CGO_ENABLED=0 go build ./cmd/olivares)"
  build_olivares_bin "$BIN"
fi

# ---------------------------------------------------------------------------
# Boot the engine wired for Operate: the native procRunner spawns the fake claude;
# the rotated-token-file CredentialSource is satisfied by the dummy token.
# ---------------------------------------------------------------------------
note "serve (insecure) wired for Operate Claude Code: fake claude + token file"
OLIVARES_SESSION_RUNTIME_CLAUDE_BIN="$BINDIR/fake-claude" \
OLIVARES_SESSION_RUNTIME_TOKEN_FILE="$WORK/session-token" \
OLIVARES_SESSION_RUNTIME_TOKEN_TTL="15m" \
TMPDIR="${EXEC_TMP:-${TMPDIR:-/tmp}}" \
"$BIN" serve --insecure \
  --listen "127.0.0.1:$PORT" --grpc-listen "127.0.0.1:$GRPC_PORT" \
  --data-dir "$WORK/data" >"$WORK/boot.log" 2>&1 &
PID=$!
wait_health || { cat "$WORK/boot.log" >&2; fail "engine never became healthy"; }

SETUP_TOKEN="$(grep -oE 'olst_[A-Z0-9]+' "$WORK/boot.log" | head -1)"
[ -n "$SETUP_TOKEN" ] || { cat "$WORK/boot.log" >&2; fail "no one-time setup token on stdout"; }
curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' \
  -d "{\"token\":\"$SETUP_TOKEN\",\"email\":\"admin@local\",\"password\":\"correct-horse-battery-staple\"}" >/dev/null \
  || fail "setup failed"
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' | jget token)"
[ -n "$TOKEN" ] || fail "login returned no token"
TENANT="$(curl -sf -X POST "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"Production","slug":"prod"}' | jget tenant_id)"
[ -n "$TENANT" ] || fail "could not create the production tenant"

export OLIVARES_SERVER_URL="$BASE" OLIVARES_TOKEN="$TOKEN" OLIVARES_TENANT="$TENANT"
olv() { "$BIN" agent "$@"; }   # the real operator CLI (thin HTTP client)

# ---------------------------------------------------------------------------
# 1) register the shared workspace and 2) launch a governed session.
# ---------------------------------------------------------------------------
note "register the shared workspace and launch a governed stream-json session"
WS_REF="$(olv workspace add "$WORK/ws" --name smoke --mode rw --json | jget workspace_ref)"
assert_nonempty "workspace_ref" "$WS_REF"

RUN_REF="$(olv session create --transport stream-json --permission-mode bypassPermissions \
  --workspace "$WS_REF" --isolation native --json | jget run_ref)"
assert_nonempty "run_ref" "$RUN_REF"

# Poll until the session is live and the bridged init message has been parsed into the
# resumable claude_session_id (proves the governed stream-json I/O was actually read).
SID=""; STATE=""
for _ in $(seq 1 40); do
  J="$(olv session get "$RUN_REF")"   # `get` is always JSON (no --json flag)
  STATE="$(printf '%s' "$J" | jget state)"
  SID="$(printf '%s' "$J" | jget claude_session_id)"
  { [ "$STATE" = "running" ] || [ "$STATE" = "idle" ]; } && [ -n "$SID" ] && break
  sleep 0.25
done
assert_eq  "session state"        "$STATE" "running"
assert_eq  "captured session id"  "$SID"   "smoke-session-0001"

# ---------------------------------------------------------------------------
# 3) the lifecycle is ledgered + ANCHORED: created/launched carry a payload_hash.
# ---------------------------------------------------------------------------
note "verify the lifecycle ledger is anchored (payload_hash per transition)"
EV="$(olv session events "$RUN_REF")"
echo "$EV" | python3 -c '
import sys, json
items = json.load(sys.stdin).get("items", [])
events = {it.get("event"): it for it in items}
for want in ("created", "launched"):
    it = events.get(want)
    assert it is not None, f"missing lifecycle event: {want}"
    assert it.get("payload_hash"), f"event {want} is not anchored (no payload_hash)"
print("    ok: ledger anchored — events=" + ",".join(e for e in events))
'

# ---------------------------------------------------------------------------
# 4) attach replays the bridged stream (init + the echo of our input line).
# ---------------------------------------------------------------------------
note "send one input line and attach — the bridged frames replay losslessly"
olv session input "$RUN_REF" --line '{"type":"user","message":{"role":"user","content":"hi"}}'
sleep 0.5
if command -v timeout >/dev/null 2>&1; then
  timeout 4 "$BIN" agent session attach "$RUN_REF" >"$WORK/attach.out" 2>/dev/null || true
  grep -q '"subtype":"init"\|"subtype": "init"' "$WORK/attach.out" || fail "attach did not replay the init frame"
  grep -q 'ack' "$WORK/attach.out" || fail "attach did not replay the echoed assistant frame"
  echo "    ok: attach replayed init + echoed frame"
else
  echo "    (skipped attach replay: 'timeout' not available)"
fi

# ---------------------------------------------------------------------------
# 5) stop tears the process group down → state stopped, ledgered.
# ---------------------------------------------------------------------------
note "stop the session and assert it is stopped + ledgered"
olv session stop "$RUN_REF" >/dev/null
STATE=""
for _ in $(seq 1 40); do
  STATE="$(olv session get "$RUN_REF" | jget state)"
  [ "$STATE" = "stopped" ] && break
  sleep 0.25
done
assert_eq "stopped state" "$STATE" "stopped"
olv session events "$RUN_REF" | python3 -c '
import sys, json
events = {it.get("event") for it in json.load(sys.stdin).get("items", [])}
assert "stopped" in events, "no stopped event in the ledger"
print("    ok: stop ledgered")
'

kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; PID=""
echo
echo "PASS — native Claude Code co-deployment is governable on the real binary:"
echo "  session launched · stream-json bridged (session id captured) · lifecycle anchored · attach replayed · stop ledgered."
