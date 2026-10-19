#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Live Playwright spec for the composer's per-session drafts (web/e2e/session-drafts.spec.ts).
# Boots `serve --insecure --seed-demo` with the fixture driver (no provider account, no
# model turn), creates two named running sessions, builds the console and serves it with
# `vite preview` against the live engine, and runs the spec with E2E_DRAFT_RUN_A/B set.
# The console is served from this checkout's source, so the spec covers console work
# before RM regenerates the embedded bundle. Extra arguments go to Playwright.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
. "$ROOT/scripts/lib/exec-tmpdir.sh"
. "$ROOT/scripts/lib/session-conversation-fixture.sh"

PORT="${E2E_PORT:-8494}"
WEB_PORT="${E2E_WEB_PORT:-$((PORT + 1))}"
GRPC_PORT="${E2E_GRPC_PORT:-$((PORT + 2))}"

cleanup() {
  [ -n "${WEB_PID:-}" ] && kill "$WEB_PID" 2>/dev/null || true
  [ -n "${WEB_PID:-}" ] && wait "$WEB_PID" 2>/dev/null || true
  [ -n "${PID:-}" ] && kill "$PID" 2>/dev/null || true
  [ -n "${PID:-}" ] && wait "$PID" 2>/dev/null || true
  rm -rf "${DATA:-}" ${EXEC_TMP:+"$EXEC_TMP"}
}
trap cleanup EXIT

DATA="$(mktemp -d)"
PID=""
WEB_PID=""
DIST="${E2E_DRAFT_DIST:-$DATA/dist-e2e}"

EXEC_TMP="$(olivares_exec_tmpdir)" || {
  echo "$(basename "$0"): no temporary directory on this box can execute" >&2
  exit 2
}

BIN="${OLIVARES_BIN:-$ROOT/bin/olivares}"
if [ ! -x "$BIN" ]; then
  echo "$(basename "$0"): set OLIVARES_BIN to a built engine binary" >&2
  exit 2
fi

for PROBE_PORT in "$PORT" "$WEB_PORT" "$GRPC_PORT"; do
  if curl -sf --connect-timeout 1 "http://127.0.0.1:$PROBE_PORT" >/dev/null 2>&1; then
    echo "$(basename "$0"): something already answers on 127.0.0.1:$PROBE_PORT" >&2
    exit 2
  fi
done

prepare_conversation_fixture

echo "==> serve --insecure --seed-demo with the conversation fixture driver"
TMPDIR="$EXEC_TMP" \
"$BIN" serve --insecure --seed-demo \
  --listen "127.0.0.1:$PORT" \
  --grpc-listen "127.0.0.1:$GRPC_PORT" \
  --data-dir "$DATA/estate" >"$DATA/engine.log" 2>&1 &
PID=$!

for _ in $(seq 1 80); do
  curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && break
  sleep 0.25
done
curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null || {
  cat "$DATA/engine.log" >&2
  echo "engine never became healthy" >&2
  exit 1
}

LOGIN="$(curl -sf -X POST "http://127.0.0.1:$PORT/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"email":"demo@olivares.local","password":"olivares-demo-estate"}')"
TOKEN="$(python3 -c 'import sys,json;print(json.load(sys.stdin).get("token",""))' <<<"$LOGIN")"
[ -n "$TOKEN" ] || { echo "demo login failed"; cat "$DATA/engine.log" >&2; exit 1; }

WHOAMI="$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sf "http://127.0.0.1:$PORT/v1/auth/whoami" -H @-)"
TENANT="$(python3 -c 'import sys,json;print(json.load(sys.stdin)["grants"][0]["tenant"])' <<<"$WHOAMI")"
[ -n "$TENANT" ] || { echo "could not resolve demo tenant"; exit 1; }
echo "==> demo tenant $TENANT"

RUNS="$(FIXTURE_TOKEN="$TOKEN" FIXTURE_TENANT="$TENANT" \
  FIXTURE_DATA="$DATA" FIXTURE_BASE="http://127.0.0.1:$PORT" python3 - <<'PY'
import json
import os
import urllib.request

base = os.environ["FIXTURE_BASE"]
data = os.environ["FIXTURE_DATA"]
headers = {
    "Authorization": "Bearer " + os.environ["FIXTURE_TOKEN"],
    "X-Olivares-Tenant": os.environ["FIXTURE_TENANT"],
    "Content-Type": "application/json",
}

def post(path, body):
    request = urllib.request.Request(
        base + "/v1/m/sessions/" + path,
        data=json.dumps(body).encode(), headers=headers, method="POST",
    )
    with urllib.request.urlopen(request, timeout=20) as response:
        return json.load(response)

profile = post("provider-profiles", {
    "driver": "claude",
    "config_home": os.path.join(data, "claude-config"),
    "user_home": os.path.join(data, "claude-home"),
    "display_name": "Draft journey fixture",
})
workspace = post("workspaces", {
    "name": "Draft journey", "root_path": os.path.join(data, "ws"),
    "mount_mode": "rw", "dlp_mode": "off",
})
runs = []
for name in ("draft-first", "draft-second"):
    run = post("runs", {
        "name": name, "transport": "stream-json",
        "permission_mode": "default", "isolation": "native",
        "provider_profile_ref": profile["profile_ref"],
        "workspace_ref": workspace["workspace_ref"],
    })
    if run.get("state") != "running" or not run.get("run_ref"):
        raise SystemExit(f"fixture session {name} did not start")
    runs.append(run["run_ref"])
print(json.dumps(runs))
PY
)"
RUN_A="$(python3 -c 'import sys,json;print(json.load(sys.stdin)[0])' <<<"$RUNS")"
RUN_B="$(python3 -c 'import sys,json;print(json.load(sys.stdin)[1])' <<<"$RUNS")"

echo "==> building the console into $DIST"
cd "$ROOT/web"
pnpm exec vite build --outDir "$DIST" >"$DATA/vite-build.log" 2>&1

echo "==> vite preview on 127.0.0.1:$WEB_PORT against the engine"
VITE_API_TARGET="http://127.0.0.1:$PORT" \
TMPDIR="$EXEC_TMP" \
pnpm exec vite preview --outDir "$DIST" --host 127.0.0.1 --port "$WEB_PORT" --strictPort \
  >"$DATA/vite.log" 2>&1 &
WEB_PID=$!

for _ in $(seq 1 80); do
  curl -sf "http://127.0.0.1:$WEB_PORT" >/dev/null 2>&1 && break
  sleep 0.25
done
curl -sf "http://127.0.0.1:$WEB_PORT" >/dev/null || {
  cat "$DATA/vite.log" >&2
  echo "vite preview never became healthy" >&2
  exit 1
}

echo "==> Playwright session-drafts.spec.ts"
PLAYWRIGHT_BASE_URL="http://127.0.0.1:$WEB_PORT" \
E2E_DRAFT_RUN_A="$RUN_A" \
E2E_DRAFT_RUN_B="$RUN_B" \
  nice -n 10 pnpm exec playwright test e2e/session-drafts.spec.ts "$@"
