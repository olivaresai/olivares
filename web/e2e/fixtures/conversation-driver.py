#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""A zero-network Claude stand-in that speaks stream-json.

Selected only by OLIVARES_SESSION_RUNTIME_CLAUDE_BIN for the conversation live
spec. On each stdin line it emits an assistant message, a tool call, a tool
result and a result footer — the four shapes the conversation view must render.
"""
from __future__ import annotations

import json
import sys

SID = "sess-conversation-fixture"
CWD = "/tmp/olivares-conversation-fixture"


def emit(obj: dict[str, object]) -> None:
    sys.stdout.write(json.dumps(obj, separators=(",", ":")) + "\n")
    sys.stdout.flush()


emit(
    {
        "type": "system",
        "subtype": "init",
        "session_id": SID,
        "cwd": CWD,
        "tools": ["Read", "Bash"],
        "model": "fixture-claude",
        "permissionMode": "default",
    }
)

while True:
    line = sys.stdin.readline()
    if not line:
        break
    emit(
        {
            "type": "assistant",
            "session_id": SID,
            "message": {
                "role": "assistant",
                "content": [{"type": "text", "text": "I'll look that up."}],
            },
        }
    )
    emit(
        {
            "type": "assistant",
            "session_id": SID,
            "message": {
                "role": "assistant",
                "content": [
                    {
                        "type": "tool_use",
                        "id": "toolu_read_1",
                        "name": "Read",
                        "input": {"file_path": "README.md"},
                    }
                ],
            },
        }
    )
    emit(
        {
            "type": "user",
            "session_id": SID,
            "message": {
                "role": "user",
                "content": [
                    {
                        "type": "tool_result",
                        "tool_use_id": "toolu_read_1",
                        "content": "# Olivares AI",
                    }
                ],
            },
        }
    )
    emit(
        {
            "type": "result",
            "subtype": "success",
            "session_id": SID,
            "result": "I'll look that up.",
            "duration_ms": 42,
            "total_cost_usd": 0.0,
            "usage": {"input_tokens": 2, "output_tokens": 8},
            "model": "fixture-claude",
        }
    )
