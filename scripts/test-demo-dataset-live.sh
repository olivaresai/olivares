#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Run the demo dataset against a real engine, twice, and prove the second pass changes nothing.
# The offline battery (scripts/test-demo-dataset.py) pins the stand-ins and the seeder's decisions; this is
# the product contract: the routes accept what the seeder sends, the stand-ins launch as real session
# children, and every conversation ends finished. Needs a built binary and loopback ports; it is heavy
# enough to run through the heavy-job wrapper:
#
#   OLIVARES_BIN=/path/to/olivares bash scripts/test-demo-dataset-live.sh
#
# The communications plane needs the directory writer's one-time activation, which scripts/docs-captures.sh
# performs; this test runs without it and passes --no-communications. The channels and messages are covered by
# the offline battery (SeederLogic) and by the capture run itself. Budgets are Business: where the engine
# answers 501 the snapshot shows them as n/a.
#
# Exit 0 when both passes succeed and the second creates nothing, 1 on a finding, 2 when it could not look.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="${OLIVARES_BIN:-$ROOT/bin/olivares}"
[ -x "$BIN" ] || { echo "test-demo-dataset-live: OLIVARES_BIN must name an executable engine" >&2; exit 2; }
. "$ROOT/scripts/lib/exec-tmpdir.sh"
EXEC_TMP="$(olivares_exec_tmpdir)" || { echo "test-demo-dataset-live: no temporary directory executes; set OLIVARES_EXEC_TMPDIR" >&2; exit 2; }
WORK="$(mktemp -d "$EXEC_TMP/dataset-live.XXXXXX")"
PIDS=()
cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  rm -rf "$WORK"
}
trap cleanup EXIT

free_port() { python3 -I -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'; }
PORT="$(free_port)"; GRPC="$(free_port)"; OLLAMA="$(free_port)"
mkdir -p "$WORK/data" "$WORK/home"
printf 'demo-harness-inference-token-not-a-real-credential\n' >"$WORK/token"; chmod 600 "$WORK/token"
python3 -I "$ROOT/scripts/demo-tools/build-demo-tools.py" "$WORK/tools"
python3 -I "$ROOT/scripts/demo-tools/demo-ollama.py" "$OLLAMA" >"$WORK/ollama.log" 2>&1 &
PIDS+=($!)

engine() { # engine [serve flags...]
  env -i PATH=/usr/local/bin:/usr/bin:/bin HOME="$WORK/home" TMPDIR="$WORK" \
    OLIVARES_SESSION_RUNTIME_TOKEN_FILE="$WORK/token" OLIVARES_SESSION_RUNTIME_WIF=0 \
    DEMO_SESSION_UNIQUE=1 \
    OLIVARES_SESSION_RUNTIME_CLAUDE_BIN="$WORK/tools/claude" OLIVARES_SESSION_RUNTIME_CODEX_BIN="$WORK/tools/codex" \
    OLIVARES_SESSION_RUNTIME_GEMINI_BIN="$WORK/tools/gemini" OLIVARES_SESSION_RUNTIME_OPENCODE_BIN="$WORK/tools/opencode" \
    "$BIN" serve --insecure "$@" --listen "127.0.0.1:$PORT" --grpc-listen "127.0.0.1:$GRPC" \
    --data-dir "$WORK/data" >>"$WORK/engine.log" 2>&1 &
  ENGINE_PID=$!
  PIDS+=("$ENGINE_PID")
  for _ in $(seq 1 240); do
    kill -0 "$ENGINE_PID" 2>/dev/null || { tail -20 "$WORK/engine.log" >&2; echo "test-demo-dataset-live: the engine exited" >&2; exit 2; }
    curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && return 0
    sleep 0.5
  done
  echo "test-demo-dataset-live: the engine did not answer" >&2; exit 2
}

api() { # api METHOD PATH [JSON] -> body on stdout; the token never reaches argv
  printf 'Authorization: Bearer %s\nX-Olivares-Tenant: %s\nContent-Type: application/json\n' "$TOKEN" "$TENANT" |
    curl -sSf -X "$1" "http://127.0.0.1:$PORT$2" -H @- ${3:+--data-binary "$3"}
}

login() {
  TOKEN="$(printf '%s' '{"email":"demo@olivares.local","password":"olivares-demo-estate"}' |
    curl -sf -X POST "http://127.0.0.1:$PORT/v1/auth/login" -H 'Content-Type: application/json' --data-binary @- |
    python3 -I -c 'import sys,json; print(json.load(sys.stdin)["token"])')"
  TENANT="$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sf "http://127.0.0.1:$PORT/v1/auth/whoami" -H @- |
    python3 -I -c 'import sys,json; print(json.load(sys.stdin)["grants"][0]["tenant"])')"
}

# A fresh install with the modules selected, then restarted: how the harness hands the engine over.
engine --seed-demo
login || { echo "test-demo-dataset-live: could not sign in to the demo engine" >&2; exit 2; }
SELECT="$(api GET /v1/console/modules | python3 -I -c '
import sys, json
m = json.load(sys.stdin)["modules"]
want = ["sessions", "skills", "governance", "inferenceproxy", "models", "accessmap"]
sel = [x["name"] for x in m if x["selected"]]
print(json.dumps({"selected": sel + [w for w in want if w not in sel and any(x["name"] == w for x in m)]}))')"
api PUT /v1/console/modules "$SELECT" >/dev/null || { echo "test-demo-dataset-live: the engine refused the module selection" >&2; exit 2; }
sleep 8
kill "$ENGINE_PID" 2>/dev/null || true; wait "$ENGINE_PID" 2>/dev/null || true
engine
login || { echo "test-demo-dataset-live: could not sign in after the restart" >&2; exit 2; }

seed() {
  OLIVARES_TOKEN="$TOKEN" python3 -I "$ROOT/scripts/seed-demo-dataset.py" "http://127.0.0.1:$PORT" - "$TENANT" \
    "$WORK/dataset" --ollama-url "http://127.0.0.1:$OLLAMA" --no-communications
}

snapshot() { # one line per surface: what exists, by count and by state
  OLIVARES_TOKEN="$TOKEN" python3 -I - "$PORT" "$TENANT" <<'PY'
import json, os, sys, urllib.error, urllib.request
port, tenant = sys.argv[1:3]
token = os.environ["OLIVARES_TOKEN"]
def get(path):
    req = urllib.request.Request("http://127.0.0.1:%s%s" % (port, path))
    req.add_header("Authorization", "Bearer " + token)
    req.add_header("X-Olivares-Tenant", tenant)
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return json.load(r)
    except urllib.error.HTTPError as exc:
        if exc.code == 501:  # a Business feature on this edition
            return None
        raise
surfaces = {
    "providers": "/v1/m/sessions/providers?limit=100",
    "profiles": "/v1/m/sessions/provider-profiles?limit=100",
    "accounts": "/v1/m/sessions/provider-accounts?limit=100",
    "workspaces": "/v1/m/sessions/workspaces?limit=100",
    "templates": "/v1/m/sessions/templates?limit=100",
    "policies": "/v1/m/governance/policies?limit=100",
    "approvals": "/v1/m/governance/approvals?limit=100",
    "runs": "/v1/m/sessions/runs?limit=100",
    "packs": "/v1/m/skills/packs?limit=100",
    "members": "/v1/members?limit=100",
    "models": "/v1/m/models/owned-models?limit=100",
    "dlp": "/v1/m/inferenceproxy/dlp/rules",
    "mcp": "/v1/console/mcp-gateway",
    "budgets": "/v1/m/finops/budgets?limit=100",
}
for name, path in surfaces.items():
    body = get(path)
    if body is None:
        print("%-10s n/a" % name)
        continue
    items = body.get("items") or body.get("rules") or body.get("servers") or []
    states = {}
    for it in items:
        st = it.get("state") or it.get("status") or it.get("effective_status") or "-"
        states[st] = states.get(st, 0) + 1
    print("%-10s %3d %s" % (name, len(items), json.dumps(states, sort_keys=True)))
body = get("/v1/m/sessions/runs?limit=100")
bad = [r["name"] for r in body["items"] if r.get("state") != "stopped" or r.get("exit_code") != 0]
if bad:
    print("UNFINISHED %s" % bad, file=sys.stderr)
    sys.exit(1)
PY
}

echo "==> first pass"
seed
snapshot >"$WORK/first.txt" || { echo "test-demo-dataset-live: the first pass left sessions unfinished or the estate unreadable" >&2; exit 1; }
cat "$WORK/first.txt"
echo "==> second pass"
seed
snapshot >"$WORK/second.txt" || { echo "test-demo-dataset-live: the second pass left sessions unfinished or the estate unreadable" >&2; exit 1; }
if ! diff -u "$WORK/first.txt" "$WORK/second.txt"; then
  echo "test-demo-dataset-live: the second pass changed the estate" >&2
  exit 1
fi
runs="$(awk '$1 == "runs" { print $2 }' "$WORK/first.txt")"
[ "$runs" -ge 7 ] || { echo "test-demo-dataset-live: expected the seven recorded conversations, found $runs runs" >&2; exit 1; }
echo "test-demo-dataset-live: OK — two passes, the same estate, every conversation finished"
