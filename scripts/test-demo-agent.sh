#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# The observed-session/run join key lives in three files. If it drifts, the estate still gets a
# Launched row, but it is an orphan beside the Discovered row whose telemetry the demo tells the
# story about. Fail before building or waiting for Playwright, and distinguish drift (1) from an
# unreadable contract (2).
set -u

ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || {
  echo "test-demo-agent: cannot inspect files outside a git worktree" >&2
  exit 2
}

SEED="$ROOT/cmd/olivares/seed/seed.go"
AGENT="$ROOT/scripts/demo-tools/demo-tool.py"
SEEDER="$ROOT/scripts/seed-demo-work.py"
for file in "$SEED" "$AGENT" "$SEEDER"; do
  [ -r "$file" ] || {
    echo "test-demo-agent: cannot inspect missing ${file#"$ROOT"/}" >&2
    exit 2
  }
done

# Each extractor is syntax-specific so a matching value in explanatory prose cannot make it green.
ref_seed="$(sed -n 's/^[[:space:]]*SessionLive[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$SEED" | head -1)"
ref_agent="$(sed -n 's/^LIVE_SESSION = "\([^"]*\)".*/\1/p' "$AGENT" | head -1)"
ref_seeder="$(sed -n 's/^DEMO_LIVE_SESSION[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$SEEDER" | head -1)"

missing=""
[ -n "$ref_seed" ] || missing="$missing cmd/olivares/seed/seed.go(SessionLive)"
[ -n "$ref_agent" ] || missing="$missing scripts/demo-tools/demo-tool.py(LIVE_SESSION)"
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

# The stand-in announces its session id in the first frame, before it reads any input.
announced() {
  env "$@" python3 -I "$AGENT" claude 2>/dev/null </dev/null | head -1 |
    python3 -I -c 'import json,sys; print(json.loads(sys.stdin.readline()).get("session_id", ""))' 2>/dev/null
}
fixed="$(announced DEMO_SESSION_UNIQUE=0)"
if [ "$fixed" != "$ref_seed" ]; then
  echo "test-demo-agent: the stand-in did not announce the observed session by default" >&2
  echo "  expected: $ref_seed" >&2
  echo "  actual:   $fixed" >&2
  exit 1
fi

unique="$(announced DEMO_SESSION_UNIQUE=1)"
if [ -z "$unique" ] || [ "$unique" = "$ref_seed" ]; then
  echo "test-demo-agent: DEMO_SESSION_UNIQUE=1 did not give the session an id of its own: '$unique'" >&2
  exit 1
fi

resumed="$(env DEMO_SESSION_UNIQUE=1 python3 -I "$AGENT" claude --resume sess-other 2>/dev/null </dev/null | head -1 |
  python3 -I -c 'import json,sys; print(json.loads(sys.stdin.readline()).get("session_id", ""))' 2>/dev/null)"
if [ "$resumed" != "sess-other" ]; then
  echo "test-demo-agent: --resume did not override the session id: '$resumed'" >&2
  exit 1
fi

echo "test-demo-agent: OK — '$ref_seed' agrees; the stand-in announces it, takes a unique id and resumes."
