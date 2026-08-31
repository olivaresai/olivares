#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# The observed-session/run join key lives in three languages. If it drifts, the estate still gets a
# Launched row, but it is an orphan beside the Discovered row whose telemetry the demo tells the
# story about. Fail before building or waiting for Playwright, and distinguish drift (1) from an
# unreadable contract (2).
set -u

ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || {
  echo "test-demo-agent: cannot inspect files outside a git worktree" >&2
  exit 2
}

SEED="$ROOT/cmd/olivares/seed/seed.go"
AGENT="$ROOT/scripts/demo-agent.sh"
SEEDER="$ROOT/scripts/seed-demo-work.py"
for file in "$SEED" "$AGENT" "$SEEDER"; do
  [ -r "$file" ] || {
    echo "test-demo-agent: cannot inspect missing ${file#"$ROOT"/}" >&2
    exit 2
  }
done

# Each extractor is syntax-specific so a matching value in explanatory prose cannot make it green.
ref_seed="$(sed -n 's/^[[:space:]]*SessionLive[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$SEED" | head -1)"
ref_agent="$(sed -n 's/^SID="\([^"]*\)".*/\1/p' "$AGENT" | head -1)"
ref_seeder="$(sed -n 's/^DEMO_LIVE_SESSION[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$SEEDER" | head -1)"

missing=""
[ -n "$ref_seed" ] || missing="$missing cmd/olivares/seed/seed.go(SessionLive)"
[ -n "$ref_agent" ] || missing="$missing scripts/demo-agent.sh(SID)"
[ -n "$ref_seeder" ] || missing="$missing scripts/seed-demo-work.py(DEMO_LIVE_SESSION)"
if [ -n "$missing" ]; then
  echo "test-demo-agent: could not extract:$missing" >&2
  exit 2
fi

if [ "$ref_seed" != "$ref_agent" ] || [ "$ref_seed" != "$ref_seeder" ]; then
  echo "test-demo-agent: demo live-session reference drifted" >&2
  echo "  engine seed: $ref_seed" >&2
  echo "  demo agent:  $ref_agent" >&2
  echo "  API seeder:  $ref_seeder" >&2
  exit 1
fi

first="$(printf 'ping\n' | sh "$AGENT" 2>/dev/null | head -1)"
expected="{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"$ref_seed\"}"
if [ "$first" != "$expected" ]; then
  echo "test-demo-agent: fixture did not emit the expected init frame" >&2
  echo "  expected: $expected" >&2
  echo "  actual:   $first" >&2
  exit 1
fi

resumed="$(printf 'ping\n' | sh "$AGENT" --resume sess-other 2>/dev/null | head -1)"
case "$resumed" in
  *'"session_id":"sess-other"'*) ;;
  *) echo "test-demo-agent: --resume did not override the session id: $resumed" >&2; exit 1 ;;
esac

echo "test-demo-agent: OK — '$ref_seed' agrees and the fixture speaks init/resume."
