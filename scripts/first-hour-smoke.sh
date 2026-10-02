#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# first-hour-smoke.sh — replay the LOCAL first-hour shape headless against the
# real binary and assert the evidence rows. It is the first-hour reproducibility
# contract: install → register one coding agent in inventory → wire the Claude
# Code hook PEP → allow one tool and deny another → read GET /v1/audit.
#
# This is the local shape (SQLite, loopback, no docker, no Postgres). Team
# (Compose + Postgres) and hybrid (remote agent hook) are documented, not
# executed here: this container has no docker, and PostgreSQL is not running.
#
# Usage:  scripts/first-hour-smoke.sh
# Requires: go (to build if ./bin/olivares is absent), curl, python3.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/build-bin.sh"
BIN="${OLIVARES_BIN:-$ROOT/bin/olivares}"
PID=""
WORK=""
EXEC_TMP=""

cleanup() {
  [ -n "${PID:-}" ] && kill "$PID" 2>/dev/null || true
  [ -n "${PID:-}" ] && wait "$PID" 2>/dev/null || true
  rm -rf "${WORK:-}" ${EXEC_TMP:+"$EXEC_TMP"}
}
trap cleanup EXIT

WORK="$(mktemp -d)"
chmod 711 "$WORK"

# shellcheck source=/dev/null
. "$ROOT/scripts/lib/exec-tmpdir.sh"
EXEC_TMP="$(olivares_exec_tmpdir)" || {
  echo "$(basename "$0"): ⛔ NO ARRANCO: ningun directorio temporal EJECUTA." >&2
  echo "   Remedio: exporta OLIVARES_EXEC_TMPDIR a un directorio que ejecute." >&2
  exit 2
}

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
BASE="http://127.0.0.1:$PORT"
PEP="http://127.0.0.1:$PEP_PORT/"

fail() { echo "FAIL: $*" >&2; exit 1; }
note() { echo "==> $*"; }

assert_eq() {
  if [ "$2" != "$3" ]; then fail "$1: got '$2', want '$3'"; fi
  echo "    ok: $1 = $2"
}

wait_health() {
  for _ in $(seq 1 240); do
    curl -sf "$BASE/healthz" >/dev/null 2>&1 && return 0
    sleep 0.5
  done
  return 1
}

if [ ! -x "$BIN" ]; then
  note "building $BIN"
  build_olivares_bin "$BIN"
fi

START="$(date +%s)"

# ---------------------------------------------------------------------------
# 1. Fresh local install: serve (headless; quickstart is TLS and blocks).
# ---------------------------------------------------------------------------
note "fresh install: serve --insecure, one-time setup, login, create tenant"
TMPDIR="${EXEC_TMP:-${TMPDIR:-/tmp}}" \
"$BIN" serve --insecure \
  --listen "127.0.0.1:$PORT" --grpc-listen "127.0.0.1:$GRPC_PORT" \
  --data-dir "$WORK/data" >"$WORK/boot1.log" 2>&1 &
PID=$!
wait_health || { cat "$WORK/boot1.log" >&2; fail "engine never became healthy"; }

# No pipe: grep answers 1 for "the log has no token" and 2 for "the log could not be
# read", and those are two different failures. A `| head -1` would report head's status
# for both, and under pipefail it can turn a token that WAS found into a failure.
token_rc=0
SETUP_TOKENS="$(grep -oE 'olst_[A-Z0-9]+' "$WORK/boot1.log")" || token_rc=$?
[ "$token_rc" -le 1 ] || {
  echo "COULD NOT LOOK: could not read $WORK/boot1.log for the setup token (grep rc=$token_rc)" >&2
  exit 2
}
SETUP_TOKEN="${SETUP_TOKENS%%$'\n'*}"
[ -n "$SETUP_TOKEN" ] || fail "no one-time setup token on stdout"
printf '{"token":"%s","email":"admin@local","password":"correct-horse-battery-staple"}' "$SETUP_TOKEN" \
  | curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' --data-binary @- >/dev/null \
  || fail "setup failed"
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"
[ -n "$TOKEN" ] || fail "login returned no token"
TENANT="$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sf -X POST "$BASE/v1/system/orgs" -H @- \
  -H 'Content-Type: application/json' -d '{"name":"First hour","slug":"first-hour"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["tenant_id"])')"
[ -n "$TENANT" ] || fail "could not create the first-hour tenant"
note "tenant: $TENANT"

# The first-hour docs describe the deployment's actual default, not a mandatory
# AAL3 ceremony. Check both the policy and this fresh password session.
printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sf "$BASE/v1/auth/step-up-policy" -H @- \
  | python3 -c 'import sys,json; p=json.load(sys.stdin); assert p["admin_step_up"] == "none", p; print("    ok: administrative step-up default = none")'
printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sf "$BASE/v1/auth/whoami" -H @- \
  | python3 -c 'import sys,json; p=json.load(sys.stdin); assert p["aal"] == 1 and p["admin_step_up"] == "none" and p["step_up_satisfied"] is True, p; print("    ok: fresh AAL1 administrator satisfies the default policy")'
POLICY_STATUS="$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sS -X PUT "$BASE/v1/auth/step-up-policy" -H @- \
  -H 'Content-Type: application/json' -d '{"admin_step_up":"passkey"}' \
  -o "$WORK/step-up-refusal.json" -w '%{http_code}')"
assert_eq "unproven passkey policy → conflict" "$POLICY_STATUS" "409"
python3 - "$WORK/step-up-refusal.json" <<'PY'
import json, sys
with open(sys.argv[1]) as stream:
    body = json.load(stream)
assert body["error"]["code"] == "passkey_not_enrolled", body
print("    ok: policy raise requires a working enrolled factor")
PY
printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sf "$BASE/v1/auth/step-up-policy" -H @- \
  | python3 -c 'import sys,json; p=json.load(sys.stdin); assert p["admin_step_up"] == "none", p; print("    ok: refused policy raise keeps none")'

# ---------------------------------------------------------------------------
# 2. Register ONE coding agent in the control-plane inventory.
# ---------------------------------------------------------------------------
note "register coding agent in inventory (POST /v1/agents)"
AGENT_JSON="$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sf -X POST "$BASE/v1/agents" \
  -H @- -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{"name":"claude-code-local","kind":"claude-code"}')"
AGENT_ID="$(printf '%s' "$AGENT_JSON" | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])')"
[ -n "$AGENT_ID" ] || fail "POST /v1/agents returned no id: $AGENT_JSON"
LISTED="$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sf "$BASE/v1/agents" \
  -H @- -H "X-Olivares-Tenant: $TENANT")"
printf '%s' "$LISTED" | python3 -c "
import sys, json
body = json.load(sys.stdin)
items = body.get('items') or body.get('Items') or []
ids = [i.get('id') for i in items]
kind = [i.get('kind') for i in items]
if '$AGENT_ID' not in ids:
    raise SystemExit('agent %s not in inventory: %s' % ('$AGENT_ID', body))
if 'claude-code' not in kind:
    raise SystemExit('claude-code kind missing: %s' % body)
print('    ok: inventory lists', '$AGENT_ID', 'kind=claude-code')
" || fail "GET /v1/agents did not list the registered agent"

kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; PID=""

# ---------------------------------------------------------------------------
# 3. Restart with the governed hooks PEP (deny-closed: allow Read, deny Bash).
# ---------------------------------------------------------------------------
note "read the published deny-closed hook policy and restart with the PEP mounted"
python3 - "$ROOT/docs-site/src/content/docs" "$TENANT" "$PEP_PORT" "$WORK/hook-pep.json" <<'PY'
import json, re, sys
from pathlib import Path
root, tenant, port, output = sys.argv[1:]
policy = None
for locale in ("", "de", "es", "fr", "ja", "ru", "zh"):
    page = (Path(root) / locale / "how-to/first-hour.md").read_text()
    blocks = re.findall(r'^cat > \./hook-pep\.json <<JSON\n(.*?)\nJSON$', page, re.M | re.S)
    if len(blocks) != 1:
        raise SystemExit("first-hour docs must contain exactly one hook-pep.json block: " + (locale or "en"))
    current = json.loads(blocks[0])
    if current["tenants"][0]["tenant"] != "$TENANT" or current["listen"] != "127.0.0.1:8447":
        raise SystemExit("first-hour tenant/listen placeholders differ: " + (locale or "en"))
    if policy is not None and policy != current:
        raise SystemExit("first-hour policy differs between locales: " + locale)
    policy = current
policy["tenants"][0]["tenant"] = tenant
policy["listen"] = "127.0.0.1:" + port
Path(output).write_text(json.dumps(policy) + "\n")
print("    ok: all seven locales publish the same executable hook policy")
PY

OLIVARES_HOOK_PEP_CONFIG="$WORK/hook-pep.json" \
TMPDIR="${EXEC_TMP:-${TMPDIR:-/tmp}}" \
"$BIN" serve --insecure \
  --listen "127.0.0.1:$PORT" --grpc-listen "127.0.0.1:$GRPC_PORT" \
  --data-dir "$WORK/data" >"$WORK/boot2.log" 2>&1 &
PID=$!
wait_health || { cat "$WORK/boot2.log" >&2; fail "engine never came back after restart"; }
grep -q "governed Claude Code hooks PEP mounted" "$WORK/boot2.log" \
  || { cat "$WORK/boot2.log" >&2; fail "the hooks PEP was not mounted"; }
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"
[ -n "$TOKEN" ] || fail "re-login returned no token"

# ---------------------------------------------------------------------------
# 4. Governed session via the managed hook client (allow Read, deny Bash).
# ---------------------------------------------------------------------------
note "drive PreToolUse through olivares claude-hook (allow Read, deny Bash)"
hook_decision() {
  local tool="$1" input="$2"
  printf '%s\n' "{\"session_id\":\"sess-first-hour\",\"hook_event_name\":\"PreToolUse\",\"tool_name\":\"$tool\",\"tool_input\":$input}" \
    | OLIVARES_HOOK_PEP_URL="$PEP" \
      OLIVARES_HOOK_PEP_TOKEN="$TOKEN" \
      OLIVARES_HOOK_PEP_TENANT="$TENANT" \
      "$BIN" claude-hook \
    | python3 -c 'import sys,json;print(json.load(sys.stdin).get("hookSpecificOutput",{}).get("permissionDecision",""))'
}

assert_eq "Read → allow" "$(hook_decision Read '{"file_path":"/repo/README.md"}')" "allow"
assert_eq "Bash → deny" "$(hook_decision Bash '{"command":"rm -rf /"}')" "deny"
assert_eq "Write → deny (default)" "$(hook_decision Write '{"file_path":"/repo/new.txt","content":"blocked"}')" "deny"

# ---------------------------------------------------------------------------
# 5. Evidence rows on the tenant ledger.
# ---------------------------------------------------------------------------
note "read evidence rows GET /v1/audit?action=hook.tool"
audit_actions() {
  local prefix="$1"
  printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sf "$BASE/v1/audit?action=$prefix&limit=100" \
    -H @- -H "X-Olivares-Tenant: $TENANT" \
    | python3 -c '
import sys, json
body = json.load(sys.stdin)
items = body.get("items") or []
print(" ".join(sorted({i.get("action","") for i in items})))
print("count", len(items))
'
}

ALLOW_EV="$(audit_actions hook.tool.allow)"
DENY_EV="$(audit_actions hook.tool.deny)"
echo "    allow ledger: $ALLOW_EV"
echo "    deny ledger:  $DENY_EV"
grep -q 'hook.tool.allow' <<<"$ALLOW_EV" || fail "no hook.tool.allow evidence row"
grep -q 'hook.tool.deny' <<<"$DENY_EV" || fail "no hook.tool.deny evidence row"
grep -q 'count 0' <<<"$ALLOW_EV" && fail "allow evidence count is 0"
grep -q 'count 0' <<<"$DENY_EV" && fail "deny evidence count is 0"

python3 - "$WORK/data/olivares.db" "$TENANT" <<'PY'
import json, sqlite3, sys
from pathlib import Path
with sqlite3.connect(Path(sys.argv[1]).as_uri() + "?mode=ro", uri=True) as db:
    counts = dict(db.execute(
        "SELECT action, COUNT(*) FROM audit_events WHERE tenant_id = ? "
        "AND action IN ('hook.tool.allow', 'hook.tool.deny') GROUP BY action",
        (sys.argv[2],),
    ))
if counts.get("hook.tool.allow", 0) < 1 or counts.get("hook.tool.deny", 0) < 2:
    raise SystemExit("hook decisions missing from the persisted ledger: " + json.dumps(counts))
print("    ok: persisted hook ledger " + json.dumps(counts, sort_keys=True))
PY

# ---------------------------------------------------------------------------
# 6. doctor first-hour next-step is present (optional; does not have to be healthy).
# ---------------------------------------------------------------------------
note "olivares doctor names the first-hour next step"
export OLIVARES_HOOK_PEP_CONFIG="$WORK/hook-pep.json"
# Doctor refuses a plaintext --server (https origin only). First-hour checks
# read PATH and the operator env, not the live probe, so the default https
# origin is enough; livez/readyz stay unknown on this insecure smoke.
DOCTOR_JSON="$("$BIN" doctor --mode user --data-dir "$WORK/data" \
  --config "$WORK/missing.env" --init unknown -o json || true)"
printf '%s' "$DOCTOR_JSON" | python3 -c '
import sys, json
report = json.load(sys.stdin)
names = {c["name"]: c for c in report.get("checks", [])}
for required in ("first-hour-coding-agent", "first-hour-hook-pep", "first-hour-next-step"):
    if required not in names:
        raise SystemExit("doctor missing check " + required)
pep = names["first-hour-hook-pep"]
if pep.get("status") != "pass":
    raise SystemExit("hook-pep status=%s detail=%s" % (pep.get("status"), pep.get("detail")))
if "never-print" in json.dumps(report):
    raise SystemExit("doctor disclosed a fixture secret")
print("    ok: doctor first-hour checks present; hook-pep=pass; next-step=%s" % names["first-hour-next-step"].get("detail",""))
' || { echo "$DOCTOR_JSON" >&2; fail "doctor first-hour checks missing or dishonest"; }

ELAPSED=$(( $(date +%s) - START ))
kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; PID=""

echo
echo "PASS — local first hour on the real binary in ${ELAPSED}s:"
echo "  install · inventory lists claude-code · Read allow · Bash deny · evidence rows."
