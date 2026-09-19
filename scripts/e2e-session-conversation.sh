#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Live Playwright spec for the session conversation surface. Boots
# `serve --insecure --seed-demo` with the fixture driver (no provider account,
# no model turn), registers one Claude profile, and runs
# web/e2e/session-conversation.spec.ts.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/build-bin.sh"
. "$ROOT/scripts/lib/exec-tmpdir.sh"

PORT="${E2E_PORT:-8490}"
GRPC_PORT="${E2E_GRPC_PORT:-$((PORT + 1))}"
BIN="${OLIVARES_BIN:-$ROOT/bin/olivares}"
DRIVER="$ROOT/web/e2e/fixtures/conversation-driver.py"

cleanup() {
  [ -n "${PID:-}" ] && kill "$PID" 2>/dev/null || true
  [ -n "${PID:-}" ] && wait "$PID" 2>/dev/null || true
  rm -rf "${DATA:-}" ${EXEC_TMP:+"$EXEC_TMP"}
}
trap cleanup EXIT

DATA="$(mktemp -d)"
PID=""

EXEC_TMP="$(olivares_exec_tmpdir)" || {
  echo "$(basename "$0"): no temporary directory on this box can execute" >&2
  exit 2
}

if [ ! -x "$BIN" ]; then
  echo "==> building $BIN"
  build_olivares_bin "$BIN"
fi
chmod +x "$DRIVER"

printf 'fixture-dummy-bearer-not-a-secret' >"$DATA/session-token"
mkdir -p "$DATA/claude-config" "$DATA/claude-home" "$DATA/ws"

if curl -sf --connect-timeout 1 "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then
  echo "$(basename "$0"): something already answers on 127.0.0.1:$PORT" >&2
  exit 2
fi

echo "==> serve --insecure --seed-demo with the conversation fixture driver"
OLIVARES_SESSION_RUNTIME_CLAUDE_BIN="$DRIVER" \
OLIVARES_SESSION_RUNTIME_TOKEN_FILE="$DATA/session-token" \
OLIVARES_SESSION_RUNTIME_TOKEN_TTL="15m" \
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

ORGS="$(curl -sf "http://127.0.0.1:$PORT/v1/system/orgs" -H "Authorization: Bearer $TOKEN")"
TENANT="$(python3 -c 'import sys,json;[print(o["tenant_id"]) for o in json.load(sys.stdin).get("items",[]) if o.get("slug")=="demo"]' <<<"$ORGS")"
[ -n "$TENANT" ] || { echo "could not resolve demo tenant"; exit 1; }
echo "==> demo tenant $TENANT"

PROFILE="$(curl -sf -X POST "http://127.0.0.1:$PORT/v1/m/sessions/provider-profiles" \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d "{\"driver\":\"claude\",\"config_home\":\"$DATA/claude-config\",\"user_home\":\"$DATA/claude-home\",\"display_name\":\"Fixture Claude\"}")"
echo "$PROFILE" | python3 -c 'import sys,json; d=json.load(sys.stdin); assert d.get("profile_ref"), d' \
  || { echo "could not register the fixture profile: $PROFILE"; cat "$DATA/engine.log" >&2; exit 1; }

echo "==> Playwright session-conversation.spec.ts"
cd "$ROOT/web"
PLAYWRIGHT_BASE_URL="http://127.0.0.1:$PORT" \
DEMO_TENANT="$TENANT" \
CONVERSATION_FIXTURE=1 \
  nice -n 10 pnpm exec playwright test e2e/session-conversation.spec.ts
