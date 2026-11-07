#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# One fresh engine, no seeded sessions; only the external tool is a stand-in.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
. "$ROOT/scripts/lib/engine-boot.sh"
. "$ROOT/scripts/lib/exec-workdir.sh"
. "$ROOT/scripts/lib/session-conversation-fixture.sh"
BIN="${OLIVARES_BIN:-$ROOT/bin/olivares}"
PORT="${E2E_PORT:-8596}"
# The engine runs the tool stand-in from here, and /tmp is noexec on some hosts.
DATA="$(olivares_pick_exec_workdir console-first-use)" || {
  echo "console-first-use: no executable scratch directory" >&2
  exit 2
}
PIDFILE="$DATA/engine.pids"
touch "$PIDFILE"
cleanup() {
  local pid started i
  while read -r pid started; do
    if [ -n "$started" ] && [ "$(engine_proc_starttime "$pid")" = "$started" ]; then
      kill "$pid" 2>/dev/null || true
      for i in {1..20}; do
        engine_proc_gone "$pid" && break
        sleep 0.1
      done
      if ! engine_proc_gone "$pid" && [ "$(engine_proc_starttime "$pid")" = "$started" ]; then
        kill -9 "$pid" 2>/dev/null || true
        for i in {1..10}; do engine_proc_gone "$pid" && break; sleep 0.1; done
        engine_proc_gone "$pid" || echo "cleanup: engine $pid survived SIGKILL" >&2
      fi
    fi
  done < "$PIDFILE"
  rm -rf "$DATA"
}
trap cleanup EXIT
prepare_conversation_fixture
# Detection and execution see the same zero-network tool. No ambient vendor login.
mkdir -p "$DATA/tools" "$DATA/home"
cp "$ROOT/web/e2e/fixtures/conversation-driver.py" "$DATA/tools/claude"
export OLIVARES_SESSION_RUNTIME_CLAUDE_BIN="$DATA/tools/claude"
export PLAYWRIGHT_FIRST_TURN_GATE="$DATA/tools/.fixture-first-turn"
touch "$PLAYWRIGHT_FIRST_TURN_GATE" "$DATA/tools/.fixture-signed-in"
# A virgin store's first boot took 23 to 62 s on the loaded CI host (2026-11-07), past
# the helper's default 20 s. Wait up to 120 s for /healthz, which answers only after the
# setup token is printed; an engine that exits still fails at once. The workflow steps
# allow 6 minutes: this bound plus the test's own 180 s.
boot_started=$SECONDS
export PLAYWRIGHT_SETUP_TOKEN
PLAYWRIGHT_SETUP_TOKEN="$(HOME="$DATA/home" PATH="$DATA/tools:$PATH" boot_engine_at "$BIN" "$PORT" "$DATA/engine" "$PIDFILE" 240)"
echo "console-first-use: the fresh engine answered after $((SECONDS - boot_started)) s" >&2
export PLAYWRIGHT_BASE_URL="http://127.0.0.1:$PORT"
cd "$ROOT/web"
pnpm exec playwright test e2e/console-first-use.spec.ts --workers=1 --retries=0
