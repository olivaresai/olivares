#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""A zero-network Claude stand-in that speaks stream-json.

Selected only by OLIVARES_SESSION_RUNTIME_CLAUDE_BIN for the conversation live
spec. On each stdin line it emits an assistant message, a tool call, a tool
result and a result footer — the four shapes the conversation view must render —
and the same bookkeeping the released Claude Code streams around a turn: two
thinking_tokens counters before the first reply, a markdown reply after the tool
result and an allowed rate_limit_event before the footer (#1784).

It also answers Claude Code's `initialize` control request with a command catalog, as
the real tool does for the console's "/" menu, and pushes system/commands_changed when
the catalog file (.fixture-commands.json in the session folder, a JSON object
{"commands": [...], "agents": [...]}) changes. The shapes are the ones measured on Claude Code 2.1.296.
"""
from __future__ import annotations

import json
import os
import sys
import threading
import time

SID = f"sess-conversation-fixture-{os.getpid()}"
CWD = os.getcwd()


_OUT = threading.Lock()


def emit(obj: dict[str, object]) -> None:
    with _OUT:
        sys.stdout.write(json.dumps(obj, separators=(",", ":")) + "\n")
        sys.stdout.flush()


# Like the real tool, the catalog comes from the session's own folder.
CATALOG_FILE = os.environ.get("FIXTURE_COMMANDS_FILE") or os.path.join(CWD, ".fixture-commands.json")
DEFAULT_CATALOG = {
    "commands": [
        {"name": "compact", "description": "Free up context by summarizing the conversation so far",
         "argumentHint": "<optional custom summarization instructions>", "builtin": True},
        {"name": "model", "description": "Set the AI model for Claude Code", "argumentHint": "<model>", "builtin": True},
    ],
    "agents": [],
}


def catalog() -> dict[str, object]:
    if CATALOG_FILE:
        try:
            with open(CATALOG_FILE, encoding="utf-8") as handle:
                return json.load(handle)
        except (OSError, ValueError):
            pass
    return DEFAULT_CATALOG


def watch_catalog() -> None:
    """Tell the engine when the catalog changed, as Claude Code does for a watched folder."""
    if not CATALOG_FILE:
        return
    seen = None
    while True:
        try:
            stamp = os.stat(CATALOG_FILE).st_mtime_ns
        except OSError:
            stamp = None
        if seen is not None and stamp != seen:
            emit({"type": "system", "subtype": "commands_changed", "commands": catalog()["commands"]})
        seen = stamp
        time.sleep(0.2)


threading.Thread(target=watch_catalog, daemon=True).start()


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
    try:
        request = json.loads(line)
    except ValueError:
        request = {}
    if request.get("type") == "control_request":
        if request.get("request", {}).get("subtype") == "initialize":
            emit(
                {
                    "type": "control_response",
                    "response": {
                        "subtype": "success",
                        "request_id": request.get("request_id"),
                        "response": {**catalog(), "models": [], "account": {"tokenSource": "none"}},
                    },
                }
            )
        continue
    for estimated in (120, 412):
        emit(
            {
                "type": "system",
                "subtype": "thinking_tokens",
                "session_id": SID,
                "estimated_tokens": estimated,
                "estimated_tokens_delta": estimated,
            }
        )
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
            "type": "assistant",
            "session_id": SID,
            "message": {
                "role": "assistant",
                "content": [
                    {
                        "type": "text",
                        "text": "Two things matter:\n\n"
                        "- **Identify clients:** list who calls the API\n"
                        "- Check `README.md` for the title\n\n"
                        "```sh\nmake test\n```",
                    }
                ],
            },
        }
    )
    emit(
        {
            "type": "rate_limit_event",
            "session_id": SID,
            "rate_limit_info": {
                "status": "allowed",
                "resetsAt": 1760270400,
                "rateLimitType": "five_hour",
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
