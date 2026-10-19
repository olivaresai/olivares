#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Labelled stand-in for Claude Code (no vendor account on this host): answers `auth` and a session's
# stream-json loop with canned frames. Nothing here runs a model. It is a dash script on purpose:
# its own RSS (~1 MB) is small enough to show the engine's cost per session, not the tool's.
case "$1 $2" in
"auth status") echo '{"loggedIn":true,"authMethod":"claude.ai","email":"standin@example.test","subscriptionType":"max"}'; exit 0;;
"auth login") read code; mkdir -p "$CLAUDE_CONFIG_DIR"; echo '{}' > "$CLAUDE_CONFIG_DIR/.credentials.json"; echo "Login successful."; exit 0;;
esac
[ "$1" = "--version" ] && { echo "2.1.289 (Claude Code)"; exit 0; }
printf '{"type":"system","subtype":"init","session_id":"standin-%s","model":"stand-in","tools":[]}\n' "$$"
while IFS= read -r line; do
  case "$line" in
  *'"request_id"'*)
    id=${line#*\"request_id\":\"}; id=${id%%\"*}
    printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{}}}\n' "$id";;
  *'"user"'*'"message"'*)
    c=${line#*ping-}; c=ping-${c%%[\\\"]*}
    printf '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"stand-in reply to %s"}]}}\n' "$c"
    printf '{"type":"result","subtype":"success","is_error":false,"result":"stand-in reply to %s","total_cost_usd":0,"duration_ms":1}\n' "$c";;
  esac
done
