#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# smoke.sh — runs the EXACT commands this example's README documents against the
# real single binary and asserts the governed verdicts. It is the example's
# reproducibility contract (the same posture as scripts/quickstart-smoke.sh):
# if governing Claude Code with the PreToolUse hooks PEP stops behaving the way the
# README claims, this fails — so the example can never quietly lie.
#
# What it proves, end to end, against `olivares serve`:
#   1. deny-closed default — a tool with no matching allow rule is DENIED.
#   2. explicit allow      — an allowlisted read tool is ALLOWED.
#   3. explicit deny       — a blocked tool (Bash) is DENIED with the policy reason.
#   4. governed rewrite     — an allowed WebFetch is rewritten (updatedInput) to an
#                             internal mirror before it runs.
#
# The decision endpoint, wire shape and policy schema are (the governed
# PreToolUse/PostToolUse PEP). The ask→HITL human-approval loop is documented in the
# README ("Going further") and proved by cmd/olivares/claudehookpep_test.go.
#
# Usage:  examples/govern-claude-code/smoke.sh
# Requires: go (to build if ./bin/olivares is absent), curl, python3.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="${OLIVARES_BIN:-$ROOT/bin/olivares}"
# SMOKE_PORT pins the trio (PORT, PORT+1, PORT+2) when you need determinism. The old
# fixed default (8971) is a latent collision on shared CI hosts — three runner
# installs share one box, so two concurrent examples jobs would fight for the same
# port and the loser reads as "engine never became healthy". Default to asking the
# kernel for three free ports instead (held open together so they cannot repeat).
if [ -n "${SMOKE_PORT:-}" ]; then
  PORT="$SMOKE_PORT"
  GRPC_PORT="$((PORT + 1))"
  PEP_PORT="$((PORT + 2))"
else
  read -r PORT GRPC_PORT PEP_PORT < <(python3 - <<'PY'
import socket
socks = [socket.socket() for _ in range(3)]
for s in socks:
    s.bind(("127.0.0.1", 0))
print(*[s.getsockname()[1] for s in socks])
for s in socks:
    s.close()
PY
)
fi
WORK="$(mktemp -d)"
# mktemp -d crea 0700. El motor bajo prueba, si corre como root, lanza cada plugin de
# conector bajo un uid DEDICADO no-root, y ese hijo tiene que atravesar TODA la cadena
# hasta su binario. Un solo eslabon en 0700 lo para con EACCES, que se lee como noexec.
chmod 711 "$WORK"
BASE="http://127.0.0.1:$PORT"
PEP="http://127.0.0.1:$PEP_PORT/"
PID=""

cleanup() {
  [ -n "$PID" ] && kill "$PID" 2>/dev/null || true
  [ -n "$PID" ] && wait "$PID" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
note() { echo "==> $*"; }

assert_eq() {
  if [ "$2" != "$3" ]; then fail "$1: got '$2', want '$3'"; fi
  echo "    ok: $1 = $2"
}

wait_health() {
  # 120s, not 20s: this asserts "the engine becomes healthy", not "the engine boots
  # fast on an idle box". On the shared CI host a cold first boot (key generation +
  # migrations) under concurrent Go test jobs overran the old 20s budget and failed
  # the smoke with a healthy engine seconds away (measured: job 90995331178).
  for _ in $(seq 1 240); do
    curl -sf "$BASE/healthz" >/dev/null 2>&1 && return 0
    sleep 0.5
  done
  return 1
}

# decision TOOL INPUT_JSON: POST one PreToolUse hook to the PEP and print the
# permissionDecision. The bearer is the operator token; the X-Olivares-Hook-Tenant
# header binds the call to the governed tenant (exactly what the managed
# `olivares claude-hook` command stamps from its environment).
decision() {
  local tool="$1" input="$2"
  curl -sf -X POST "$PEP" \
    -H "Authorization: Bearer $TOKEN" \
    -H "X-Olivares-Hook-Tenant: $TENANT" \
    -H 'Content-Type: application/json' \
    -d "{\"session_id\":\"sess-example\",\"hook_event_name\":\"PreToolUse\",\"tool_name\":\"$tool\",\"tool_input\":$input}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin).get("hookSpecificOutput",{}).get("permissionDecision",""))'
}

# rewritten_field TOOL INPUT_JSON FIELD: POST a hook and print updatedInput[FIELD].
rewritten_field() {
  local tool="$1" input="$2" field="$3"
  curl -sf -X POST "$PEP" \
    -H "Authorization: Bearer $TOKEN" \
    -H "X-Olivares-Hook-Tenant: $TENANT" \
    -H 'Content-Type: application/json' \
    -d "{\"session_id\":\"sess-example\",\"hook_event_name\":\"PreToolUse\",\"tool_name\":\"$tool\",\"tool_input\":$input}" \
  | python3 -c "import sys,json;print(json.load(sys.stdin).get('hookSpecificOutput',{}).get('updatedInput',{}).get('$field',''))"
}

if [ ! -x "$BIN" ]; then
  note "building $BIN (the example's 'task build')"
  ( cd "$ROOT" && CGO_ENABLED=0 go build -trimpath -o "$BIN" ./cmd/olivares )
fi

# ---------------------------------------------------------------------------
# Step 1 — a fresh install: setup, login, create the governed tenant.
# (Identical to the operator on-ramp in scripts/quickstart-smoke.sh PATH B.)
# ---------------------------------------------------------------------------
note "fresh install: serve, one-time setup, login, create the 'prod' tenant"
"$BIN" serve --insecure \
  --listen "127.0.0.1:$PORT" --grpc-listen "127.0.0.1:$GRPC_PORT" \
  --data-dir "$WORK/data" >"$WORK/boot1.log" 2>&1 &
PID=$!
wait_health || { cat "$WORK/boot1.log" >&2; fail "engine never became healthy"; }

SETUP_TOKEN="$(grep -oE 'olst_[A-Z0-9]+' "$WORK/boot1.log" | head -1)"
[ -n "$SETUP_TOKEN" ] || fail "no one-time setup token on stdout"
curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' \
  -d "{\"token\":\"$SETUP_TOKEN\",\"email\":\"admin@local\",\"password\":\"correct-horse-battery-staple\"}" >/dev/null \
  || fail "setup failed"
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"
[ -n "$TOKEN" ] || fail "login returned no token"
TENANT="$(curl -sf -X POST "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"Production","slug":"prod"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["tenant_id"])')"
[ -n "$TENANT" ] || fail "could not create the production tenant"
note "governed tenant: $TENANT"

kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; PID=""

# ---------------------------------------------------------------------------
# Step 2 — restart with the governed hooks PEP wired (OLIVARES_HOOK_PEP_CONFIG).
# The policy is the deny-closed allowlist this example teaches. We render it here
# with the real tenant id; the README shows the same file verbatim.
# ---------------------------------------------------------------------------
note "write the governed hook policy and restart with the PEP mounted"
cat >"$WORK/hook-pep.json" <<JSON
{
  "listen": "127.0.0.1:$PEP_PORT",
  "tenants": [
    {
      "tenant": "$TENANT",
      "require_firm_identity": false,
      "policy": {
        "version": "examples.govern/v1",
        "default": "deny",
        "rules": [
          { "tool": "Read", "decision": "allow", "reason": "reads are permitted" },
          { "tool": "Grep", "decision": "allow", "reason": "reads are permitted" },
          { "tool": "Glob", "decision": "allow", "reason": "reads are permitted" },
          { "tool": "Bash", "decision": "deny", "reason": "shell execution is blocked by this policy" },
          { "tool": "WebFetch", "decision": "allow", "rewrite": { "url": "https://mirror.internal/allowed" }, "reason": "external fetches are redirected to the internal mirror" }
        ]
      }
    }
  ]
}
JSON

OLIVARES_HOOK_PEP_CONFIG="$WORK/hook-pep.json" "$BIN" serve --insecure \
  --listen "127.0.0.1:$PORT" --grpc-listen "127.0.0.1:$GRPC_PORT" \
  --data-dir "$WORK/data" >"$WORK/boot2.log" 2>&1 &
PID=$!
wait_health || { cat "$WORK/boot2.log" >&2; fail "engine never came back after restart"; }
grep -q "governed Claude Code hooks PEP mounted" "$WORK/boot2.log" \
  || { cat "$WORK/boot2.log" >&2; fail "the hooks PEP was not mounted (check OLIVARES_HOOK_PEP_CONFIG)"; }
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"
[ -n "$TOKEN" ] || fail "re-login returned no token"

# ---------------------------------------------------------------------------
# Step 3 — drive PreToolUse decisions through the governed PEP and assert them.
# ---------------------------------------------------------------------------
note "1) deny-closed default: a tool with no allow rule is DENIED"
assert_eq "Write (no rule) → deny-closed" \
  "$(decision Write '{"file_path":"/etc/passwd","content":"x"}')" "deny"

note "2) explicit allow: an allowlisted read tool is ALLOWED"
assert_eq "Read → allow" \
  "$(decision Read '{"file_path":"/repo/README.md"}')" "allow"

note "3) explicit deny: shell execution is BLOCKED with the policy reason"
assert_eq "Bash → deny" \
  "$(decision Bash '{"command":"rm -rf /"}')" "deny"

note "4) governed rewrite: an external fetch is allowed but rewritten to the mirror"
assert_eq "WebFetch → allow" \
  "$(decision WebFetch '{"url":"https://news.example.com/feed"}')" "allow"
assert_eq "WebFetch updatedInput.url → mirror" \
  "$(rewritten_field WebFetch '{"url":"https://news.example.com/feed"}' url)" "https://mirror.internal/allowed"

kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; PID=""

echo
echo "PASS — the control plane governed Claude Code tool-calls on the real binary:"
echo "  deny-closed default · allowlisted read allowed · shell denied · external fetch rewritten."
