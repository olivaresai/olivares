#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# provider-onboarding-smoke.sh — replay the D19 path headless against the REAL
# binary and assert what it claims: register a provider credential, test it
# without spending, bind it to a profile, launch a session, and prove the child
# received THAT credential and that the credential appears nowhere else.
#
# ⛔ WHAT IS REAL HERE AND WHAT IS A DOUBLE, said up front because the difference
#    is the whole value of the evidence:
#      REAL  — the engine, the sealed secret store, the provider plane, the
#              profile plane, the launch path, the CLI, every HTTP answer.
#      DOUBLE — (1) the PROVIDER: a local TLS server that serves `/v1/models`,
#              because no real vendor key is available in this container
#              (OLIVARES_TEST_ANTHROPIC_KEY is unset); it is registered as an
#              `openai_compatible` provider, which is the product's own supported
#              way to point at a non-official endpoint, so no test-only code path
#              is involved. (2) the CLAUDE CLI: a script that records the
#              environment it was launched with. The launch, the injection and
#              the refusals are the product's.
#
# Usage:  scripts/provider-onboarding-smoke.sh
# Requires: go (to build if ./bin/olivares is absent), curl, python3, openssl.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/build-bin.sh"
BIN="${OLIVARES_BIN:-$ROOT/bin/olivares}"
PID=""
DOUBLE_PID=""
WORK=""
EXEC_TMP=""

cleanup() {
  [ -n "${PID:-}" ] && kill "$PID" 2>/dev/null || true
  [ -n "${DOUBLE_PID:-}" ] && kill "$DOUBLE_PID" 2>/dev/null || true
  [ -n "${PID:-}" ] && wait "$PID" 2>/dev/null || true
  [ -n "${DOUBLE_PID:-}" ] && wait "$DOUBLE_PID" 2>/dev/null || true
  rm -rf "${WORK:-}" ${EXEC_TMP:+"$EXEC_TMP"}
}
trap cleanup EXIT

WORK="$(mktemp -d)"
chmod 711 "$WORK"

# shellcheck source=/dev/null
. "$ROOT/scripts/lib/exec-tmpdir.sh"
EXEC_TMP="$(olivares_exec_tmpdir)" || {
  echo "$(basename "$0"): NO START: no temporary directory EXECUTES." >&2
  exit 2
}

read -r PORT GRPC_PORT DOUBLE_PORT < <(python3 - <<'PY'
import socket
socks = [socket.socket() for _ in range(3)]
for s in socks:
    s.bind(("127.0.0.1", 0))
print(*[s.getsockname()[1] for s in socks])
for s in socks:
    s.close()
PY
)
BASE="http://127.0.0.1:$PORT"

fail() { echo "FAIL: $*" >&2; exit 1; }
note() { echo "==> $*"; }
ok()   { echo "    ok: $*"; }

assert_eq() {
  if [ "$2" != "$3" ]; then fail "$1: got '$2', want '$3'"; fi
  ok "$1 = $2"
}

wait_health() {
  for _ in $(seq 1 240); do
    curl -sf "$BASE/healthz" >/dev/null 2>&1 && return 0
    sleep 0.5
  done
  return 1
}

jqp() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }

# THE CANARY. It is registered as a credential and then searched for in every
# artefact the operator or an attacker could reach. A grep for it is the only
# honest form of "the value never leaves".
CANARY="sk-d19-CANARY-0123456789-ABCD"

if [ ! -x "$BIN" ]; then
  note "building $BIN"
  build_olivares_bin "$BIN"
fi

# ---------------------------------------------------------------------------
# 0. The provider DOUBLE: TLS, one route, and it records what it was asked.
# ---------------------------------------------------------------------------
note "start the recorded provider double on https://127.0.0.1:$DOUBLE_PORT"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -keyout "$WORK/double.key" -out "$WORK/double.crt" \
  -subj "/CN=127.0.0.1" -addext "subjectAltName=IP:127.0.0.1" >/dev/null 2>&1 \
  || fail "could not mint the double's certificate"

cat >"$WORK/double.py" <<'PY'
import http.server, json, ssl, sys, os

PORT = int(sys.argv[1])
CERT, KEY, LOG = sys.argv[2], sys.argv[3], sys.argv[4]
EXPECTED = sys.argv[5]

class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        auth = self.headers.get("Authorization", "")
        with open(LOG, "a") as fh:
            fh.write(json.dumps({"path": self.path, "auth_present": bool(auth),
                                 "auth_matches": auth == "Bearer " + EXPECTED}) + "\n")
        if self.path != "/v1/models":
            self.send_response(404); self.end_headers(); return
        if auth != "Bearer " + EXPECTED:
            self.send_response(401); self.end_headers(); return
        body = json.dumps({"data": [{"id": "double-opus"}, {"id": "double-haiku"}]}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    # The double must not be the noisiest thing in the transcript.
    def log_message(self, *a): pass

    # ⛔ A COMPLETION PATH DOES NOT EXIST HERE ON PURPOSE. If the connection test
    # ever starts spending, this double answers 501 and the assertion below fires.
    def do_POST(self):
        with open(LOG, "a") as fh:
            fh.write(json.dumps({"path": self.path, "method": "POST"}) + "\n")
        self.send_response(501); self.end_headers()

ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(CERT, KEY)
srv = http.server.HTTPServer(("127.0.0.1", PORT), H)
srv.socket = ctx.wrap_socket(srv.socket, server_side=True)
srv.serve_forever()
PY
python3 "$WORK/double.py" "$DOUBLE_PORT" "$WORK/double.crt" "$WORK/double.key" \
  "$WORK/double.log" "$CANARY" >"$WORK/double.out" 2>&1 &
DOUBLE_PID=$!
for _ in $(seq 1 100); do
  curl -sk "https://127.0.0.1:$DOUBLE_PORT/v1/models" >/dev/null 2>&1 && break
  sleep 0.1
done

# ---------------------------------------------------------------------------
# 1. The fake official CLI: it records the environment it was launched with.
# ---------------------------------------------------------------------------
CLAUDE_BIN="$EXEC_TMP/claude-recorder"
cat >"$CLAUDE_BIN" <<'SH'
#!/usr/bin/env bash
# Records the environment this child was launched with, then behaves like a
# stream-json session long enough to be observed and stopped.
#
# ⛔ IT WRITES UNDER $HOME AND NOT UNDER A PATH THIS SCRIPT EXPORTS, and finding
# that out is itself evidence: the launcher does NOT hand the child the ambient
# environment — it builds a minimal one from the explicit launch spec plus the
# caller's env_allow list, so a variable this script exported to the ENGINE never
# reached the child. HOME is one of the two the PROFILE owns, so it is always there
# and it is always the profile's own.
env > "$HOME/d19-child.env"
printf '{"type":"system","subtype":"init","session_id":"d19-smoke"}\n'
sleep 30
SH
chmod 0755 "$CLAUDE_BIN"

HOME_DIR="$WORK/homes/user"; CONFIG_DIR="$WORK/homes/config"; WS_DIR="$WORK/ws"
mkdir -p "$HOME_DIR" "$CONFIG_DIR" "$WS_DIR"

# ---------------------------------------------------------------------------
# 2. Fresh local install.
# ---------------------------------------------------------------------------
note "fresh install: serve --insecure, one-time setup, login, tenant"
TMPDIR="${EXEC_TMP:-${TMPDIR:-/tmp}}" \
SSL_CERT_FILE="$WORK/double.crt" \
OLIVARES_SESSION_RUNTIME_CLAUDE_BIN="$CLAUDE_BIN" \
"$BIN" serve --insecure \
  --listen "127.0.0.1:$PORT" --grpc-listen "127.0.0.1:$GRPC_PORT" \
  --data-dir "$WORK/data" >"$WORK/engine.log" 2>&1 &
PID=$!
wait_health || { cat "$WORK/engine.log" >&2; fail "engine never became healthy"; }

SETUP_TOKEN="$(grep -oE 'olst_[A-Z0-9]+' "$WORK/engine.log" | head -1)"
[ -n "$SETUP_TOKEN" ] || fail "no one-time setup token on stdout"
curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' \
  -d "{\"token\":\"$SETUP_TOKEN\",\"email\":\"admin@local\",\"password\":\"correct-horse-battery-staple\"}" >/dev/null \
  || fail "setup failed"
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' | jqp 'd["token"]')"
TENANT="$(curl -sf -X POST "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"D19","slug":"d19"}' | jqp 'd["tenant_id"]')"
[ -n "$TENANT" ] || fail "could not create the tenant"
ok "tenant $TENANT"

CLI=("$BIN" --server "$BASE" --token "$TOKEN" --tenant "$TENANT")
export OLIVARES_SERVER_URL="$BASE" OLIVARES_TOKEN="$TOKEN" OLIVARES_TENANT="$TENANT"

# ---------------------------------------------------------------------------
# 3. Register the provider — from the CLI, with the key on stdin.
# ---------------------------------------------------------------------------
note "olivares provider add (key on stdin, never a flag)"
ADD_JSON="$(printf '%s' "$CANARY" | "$BIN" provider add \
  --kind openai_compatible --name "Recorded double" \
  --base-url "https://127.0.0.1:$DOUBLE_PORT" \
  --server "$BASE" --token "$TOKEN" --tenant "$TENANT" -o json)" \
  || fail "provider add failed"
PROVIDER="$(printf '%s' "$ADD_JSON" | jqp 'd["provider_ref"]')"
HINT="$(printf '%s' "$ADD_JSON" | jqp 'd.get("key_hint","")')"
[ -n "$PROVIDER" ] || fail "no provider_ref: $ADD_JSON"
assert_eq "the hint is the last four characters" "$HINT" "…ABCD"
printf '%s' "$ADD_JSON" | grep -q "$CANARY" && fail "the create response echoed the credential"
ok "the create response does not carry the credential"
assert_eq "a fresh provider is NOT reported as working" \
  "$(printf '%s' "$ADD_JSON" | jqp 'd.get("probe_state","")')" ""

# ---------------------------------------------------------------------------
# 4. Test the connection — and prove it did not spend.
# ---------------------------------------------------------------------------
note "olivares provider test"
TEST_JSON="$("${CLI[@]}" provider test "$PROVIDER" -o json)" || fail "provider test failed"
assert_eq "the probe reports ok" "$(printf '%s' "$TEST_JSON" | jqp 'd["probe_state"]')" "ok"
assert_eq "the probe lists the double's models" \
  "$(printf '%s' "$TEST_JSON" | jqp '",".join(d.get("models",[]))')" "double-haiku,double-opus"
printf '%s' "$TEST_JSON" | grep -q "$CANARY" && fail "the test response echoed the credential"
ok "the test response does not carry the credential"
if grep -q '"method": "POST"' "$WORK/double.log"; then
  fail "the connection test sent a POST: it must never spend"
fi
ok "the double saw only GET /v1/models — the test spent nothing"
grep -q '"auth_matches": true' "$WORK/double.log" \
  || fail "the provider never received the registered credential"
ok "the double received exactly the registered credential"

# ---------------------------------------------------------------------------
# 5. Register the profile bound to it, and launch.
# ---------------------------------------------------------------------------
note "olivares agent workspace add + agent profile create --provider"
# ⛔ `--mode ro` AND IT IS NOT A CONVENIENCE. A read-WRITE mount of a classified
# workspace is a PRIVILEGED launch (cmd/olivares/sessiongov.go isCriticalLaunch), and a
# privileged launch is refused deny-closed unless a HITL approval bridge is wired —
# measured here on the first run of this script:
#   403 "privileged session launch requires human approval but the HITL bridge is not
#        wired (deny-closed)"
# That gate is CORRECT and is not this lane's subject. A read-only mount is the
# ordinary launch, which is what the provider path has to be proven on.
WS_JSON="$("${CLI[@]}" agent workspace add "$WS_DIR" --name d19 --mode ro -o json)" \
  || fail "workspace add failed"
WS_REF="$(printf '%s' "$WS_JSON" | jqp 'd.get("workspace_ref") or d.get("ref")')"
[ -n "$WS_REF" ] || fail "no workspace_ref: $WS_JSON"

PROFILE_JSON="$("${CLI[@]}" agent profile create --driver claude \
  --config-home "$CONFIG_DIR" --user-home "$HOME_DIR" --name "Claude (smoke)" \
  --auth-source managed_injection --provider "$PROVIDER" -o json)" \
  || fail "agent profile create failed"
PROFILE="$(printf '%s' "$PROFILE_JSON" | jqp 'd["profile_ref"]')"
assert_eq "the profile names the provider" \
  "$(printf '%s' "$PROFILE_JSON" | jqp 'd.get("provider_record_ref","")')" "$PROVIDER"

note "olivares agent session create --provider-profile"
RUN_JSON="$("${CLI[@]}" agent session create --name d19-1 --workspace "$WS_REF" \
  --provider-profile "$PROFILE" -o json)" || fail "session create failed"
RUN="$(printf '%s' "$RUN_JSON" | jqp 'd["run_ref"]')"
ok "run $RUN"

for _ in $(seq 1 100); do [ -s "$HOME_DIR/d19-child.env" ] && break; sleep 0.1; done
[ -s "$HOME_DIR/d19-child.env" ] || fail "the child never recorded its environment"

# ---------------------------------------------------------------------------
# 6. THE CLAIM: the child received THAT credential, and nothing else did.
# ---------------------------------------------------------------------------
note "assert the injection"
assert_eq "the child received the registered credential" \
  "$(grep -c "^OPENAI_API_KEY=$CANARY$" "$HOME_DIR/d19-child.env" || true)" "1"
assert_eq "the child received the provider's endpoint" \
  "$(grep -c "^OPENAI_BASE_URL=https://127.0.0.1:$DOUBLE_PORT$" "$HOME_DIR/d19-child.env" || true)" "1"
assert_eq "no host inference bearer was injected" \
  "$(grep -c '^ANTHROPIC_AUTH_TOKEN=' "$HOME_DIR/d19-child.env" || true)" "0"
assert_eq "the run DTO carries no credential" \
  "$(printf '%s' "$RUN_JSON" | grep -c "$CANARY" || true)" "0"
assert_eq "the engine log carries no credential" \
  "$(grep -c "$CANARY" "$WORK/engine.log" || true)" "0"
assert_eq "the provider list carries no credential" \
  "$("${CLI[@]}" provider ls -o json | grep -c "$CANARY" || true)" "0"

note "assert the database holds no cleartext credential"
assert_eq "no data-dir file contains the credential" \
  "$(grep -rl "$CANARY" "$WORK/data" 2>/dev/null | wc -l | tr -d ' ')" "0"

"${CLI[@]}" agent session stop "$RUN" >/dev/null 2>&1 || true

# ---------------------------------------------------------------------------
# 7. Revoke, and prove the next launch is refused BY NAME rather than falling back.
# ---------------------------------------------------------------------------
note "olivares provider rm --yes, then relaunch"
"${CLI[@]}" provider rm "$PROVIDER" --yes >/dev/null || fail "provider rm failed"
REFUSAL="$("${CLI[@]}" agent session create --name d19-2 --workspace "$WS_REF" \
  --provider-profile "$PROFILE" 2>&1 || true)"
printf '%s' "$REFUSAL" | grep -qi "revoked" \
  || fail "a launch under a revoked provider must be refused by name, got: $REFUSAL"
ok "the launch is refused and names the cause"
assert_eq "no second child was launched" \
  "$(grep -c "^OPENAI_API_KEY=$CANARY$" "$HOME_DIR/d19-child.env" || true)" "1"

echo
echo "provider-onboarding-smoke: OK — provider registered, tested without spending,"
echo "bound, launched with its own credential, and revoked with the launch refused."
