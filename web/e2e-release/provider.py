#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Labelled Ollama, OpenAI and Anthropic protocol stand-in; never a replacement engine API.

A turn whose last user message names COMMAND answers with one shell call, so the
tool asks for approval; the turn after the tool's result answers ANSWER only when
that result holds PROOF, which only running the command prints. A refused, denied
or failed call answers TOOL_NOT_RUN and the start of its result. A turn that names
RESUME_PROMPT answers RESUMED. No prompt contains PROOF or either answer.
"""
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

MODEL = "first-hour-stub"
ANSWER = "FIRST_HOUR_PROVIDER_STUB_OK"
RESUMED = "FIRST_HOUR_RESUMED_STUB_OK"
COMMAND = "echo first-hour-$((6*7))"
PROOF = "first-hour-42"
TOOL_NOT_RUN = "FIRST_HOUR_STUB_TOOL_NOT_RUN"
RESUME_PROMPT = "Answer once more after the resume."
# Codex names its shell tool per version; each name takes its own arguments.
CODEX_SHELLS = {
    "shell_command": {"command": COMMAND},
    "exec_command": {"cmd": COMMAND},
    "shell": {"command": ["bash", "-lc", COMMAND]},
}


def latest(items: object, roles: tuple[str, ...]) -> dict:
    """The last item with one of these roles or types: tools append system notes after it."""
    items = items if isinstance(items, list) else [items]
    return next((i for i in reversed(items) if isinstance(i, dict) and (i.get("role") in roles or i.get("type") in roles)), {})


def flat(value: object) -> str:
    return value if isinstance(value, str) else json.dumps(value)


def answer(last: object, result: str | None, tools: list) -> str | None:
    """None asks for the shell call; otherwise the text to answer. result is the
    tool's output when the turn returns one."""
    if result is not None:
        return ANSWER if PROOF in result else f"{TOOL_NOT_RUN} {result[:200]}"
    text = json.dumps(last)
    if COMMAND in text and tools:
        return None
    return RESUMED if RESUME_PROMPT in text else ANSWER


class Provider(BaseHTTPRequestHandler):
    def log_message(self, *_args: object) -> None:
        pass  # No request bodies, credentials or prompt text in artifacts.

    def reply(self, data: dict, status: int = 200) -> None:
        body = json.dumps(data).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def events(self, events: list[tuple[str | None, dict]], done: bool = False) -> None:
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for name, data in events:
            self.wfile.write((f"event: {name}\n" if name else "").encode() + f"data: {json.dumps(data)}\n\n".encode())
        if done:
            self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()

    def do_GET(self) -> None:
        path = urlsplit(self.path).path
        if path == "/api/tags":
            self.reply({"models": [{"name": MODEL, "model": MODEL, "size": 1}]})
        elif path == "/v1/models":
            # One body for the OpenAI and the Anthropic model list.
            self.reply({"object": "list", "has_more": False, "first_id": MODEL, "last_id": MODEL,
                        "data": [{"id": MODEL, "object": "model", "type": "model", "owned_by": "local-stub",
                                  "display_name": MODEL, "created_at": "2026-01-01T00:00:00Z"}]})
        else:
            self.reply({"error": "unexpected provider route"}, 404)

    def do_POST(self) -> None:
        length = int(self.headers.get("Content-Length", "0"))
        if length > 2_000_000:
            self.reply({"error": "request too large"}, 413)
            return
        request = json.loads(self.rfile.read(length))
        path = urlsplit(self.path).path
        if path == "/api/show":
            self.reply({"capabilities": ["completion", "tools"], "model_info": {"general.architecture": "qwen2", "qwen2.context_length": 32768}})
        elif path == "/v1/chat/completions":
            self.openai_chat(request)
        elif path == "/v1/responses":
            self.openai_responses(request)
        elif path == "/v1/messages/count_tokens":
            self.reply({"input_tokens": 1})
        elif path == "/v1/messages":
            self.anthropic_messages(request)
        else:
            self.reply({"error": "unexpected provider route"}, 404)

    def openai_chat(self, request: dict) -> None:
        """OpenAI chat completions (OpenCode)."""
        last = latest(request.get("messages"), ("user", "tool"))
        result = flat(last.get("content")) if last.get("role") == "tool" else None
        text = answer(last, result, request.get("tools") or [])
        call = {"id": "call_first_hour", "type": "function",
                "function": {"name": "bash", "arguments": json.dumps({"command": COMMAND, "description": "Print the proof line"})}}
        finish = "stop" if text else "tool_calls"
        base = {"id": "first-hour-stub", "created": 1, "model": MODEL}
        if request.get("stream"):
            delta = {"role": "assistant", "content": text} if text else {"role": "assistant", "tool_calls": [{"index": 0, **call}]}
            self.events([(None, {**base, "object": "chat.completion.chunk", "choices": [{"index": 0, "delta": d, "finish_reason": end}]})
                         for d, end in [(delta, None), ({}, finish)]], done=True)
        else:
            message = {"role": "assistant", "content": text} if text else {"role": "assistant", "content": None, "tool_calls": [call]}
            self.reply({**base, "object": "chat.completion", "choices": [{"index": 0, "message": message, "finish_reason": finish}],
                        "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})

    def openai_responses(self, request: dict) -> None:
        """OpenAI Responses (Codex); Codex always streams."""
        last = latest(request.get("input"), ("user", "function_call_output"))
        tools = [t.get("name") for t in request.get("tools") or [] if isinstance(t, dict)]
        result = flat(last.get("output")) if last.get("type") == "function_call_output" else None
        text = answer(last, result, tools)
        name = next((n for n in CODEX_SHELLS if n in tools), None)
        if text is None and name is None:
            # Name what was offered, so a new Codex tool name fails readably.
            text = "FIRST_HOUR_STUB_NO_SHELL_TOOL " + ",".join(map(str, tools))
        item = ({"type": "message", "id": "msg_first_hour", "status": "completed", "role": "assistant",
                 "content": [{"type": "output_text", "text": text, "annotations": []}]} if text is not None else
                {"type": "function_call", "id": "fc_first_hour", "call_id": "call_first_hour", "status": "completed",
                 "name": name, "arguments": json.dumps(CODEX_SHELLS[name])})
        response = {"id": "resp_first_hour", "object": "response", "model": MODEL}
        self.events([(e["type"], e) for e in [
            {"type": "response.created", "response": {**response, "status": "in_progress", "output": []}},
            {"type": "response.output_item.done", "output_index": 0, "item": item},
            {"type": "response.completed", "response": {**response, "status": "completed", "output": [item],
                                                        "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}},
        ]])

    def anthropic_messages(self, request: dict) -> None:
        """Anthropic Messages (Claude Code)."""
        last = latest(request.get("messages"), ("user",))
        content = last.get("content")
        results = [b.get("content") for b in content if isinstance(b, dict) and b.get("type") == "tool_result"] if isinstance(content, list) else []
        text = answer(last, flat(results) if results else None, request.get("tools") or [])
        command = {"command": COMMAND, "description": "Print the proof line"}
        block = {"type": "text", "text": text} if text else {"type": "tool_use", "id": "toolu_first_hour", "name": "Bash", "input": command}
        stop = "end_turn" if text else "tool_use"
        message = {"id": "msg_first_hour", "type": "message", "role": "assistant", "model": request.get("model", MODEL),
                   "content": [block], "stop_reason": stop, "stop_sequence": None, "usage": {"input_tokens": 1, "output_tokens": 1}}
        if not request.get("stream"):
            self.reply(message)
            return
        start = {"type": "text", "text": ""} if text else {**block, "input": {}}
        delta = {"type": "text_delta", "text": text} if text else {"type": "input_json_delta", "partial_json": json.dumps(command)}
        self.events([(e["type"], e) for e in [
            {"type": "message_start", "message": {**message, "content": [], "stop_reason": None}},
            {"type": "content_block_start", "index": 0, "content_block": start},
            {"type": "content_block_delta", "index": 0, "delta": delta},
            {"type": "content_block_stop", "index": 0},
            {"type": "message_delta", "delta": {"stop_reason": stop, "stop_sequence": None}, "usage": {"output_tokens": 1}},
            {"type": "message_stop"},
        ]])


if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", 11434), Provider).serve_forever()
