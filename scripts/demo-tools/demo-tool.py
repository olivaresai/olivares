#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Offline stand-in for the AI tools of the demo estate: Claude Code, Codex, Gemini CLI and OpenCode.

build-demo-tools.py writes one self-contained executable per tool (this file with the tool
name and conversations.json embedded: a confined session child can read its own program and
nothing else). The harness points OLIVARES_SESSION_RUNTIME_<TOOL>_BIN at it, so the engine
launches it exactly as it launches the real tool and speaks the tool's own wire protocol:

  claude    stream-json over stdin and stdout
  codex     `app-server` JSON-RPC (the frames driver_codex.go sends)
  opencode  `acp` Agent Client Protocol v1
  gemini    `--acp` Agent Client Protocol v1 (plus session/set_mode and session/set_model)

Answers come from conversations.json, matched on the exact prompt text, so every finished
conversation in a capture is a recorded transcript and no vendor, account or network is used.
The status commands the engine runs (`auth status`, `login status`, `auth list`) answer "signed
in" the way the real tool prints it. A tool never reads, writes or prints a credential.
"""
import json
import os
import sys
import time
import uuid

HERE = os.path.dirname(os.path.realpath(__file__))
# The observed session cmd/olivares/seed/seed.go creates. A launched run that announces this id is joined
# to that row; a run launched with DEMO_SESSION_UNIQUE=1 gets an id of its own. scripts/test-demo-agent.sh
# fails when this, the engine seed and DEMO_LIVE_SESSION in seed-demo-work.py disagree.
LIVE_SESSION = "sess-coder-7a3f"
VERSIONS = {"claude": "2.1.0 (Claude Code)", "codex": "codex-cli 0.153.4",
            "opencode": "1.18.30", "gemini": "0.62.0"}


def load_turns(tool):
    embedded = globals().get("EMBEDDED_CONVERSATIONS")
    if embedded is not None:
        sessions = embedded["sessions"]
    else:
        with open(os.path.join(HERE, "conversations.json"), encoding="utf8") as fh:
            sessions = json.load(fh)["sessions"]
    return {t["prompt"].strip(): t for s in sessions if s["tool"] == tool for t in s["turns"]}


def offered_models(tool):
    """The models the recorded sessions of this tool use: what the tool advertises as selectable."""
    embedded = globals().get("EMBEDDED_CONVERSATIONS")
    if embedded is None:
        with open(os.path.join(HERE, "conversations.json"), encoding="utf8") as fh:
            embedded = json.load(fh)
    seen = []
    for s in embedded["sessions"]:
        # OpenCode names a model of the local provider under the engine's own provider id.
        model = "olivares_ollama/" + s["model"] if s.get("local") else s["model"]
        if s["tool"] == tool and model not in seen:
            seen.append(model)
    return seen


def lookup(turns, prompt):
    turn = turns.get(prompt.strip())
    if turn:
        return turn
    # A prompt nobody recorded is a mistake in the dataset. Answering it would publish an invented reply and
    # look finished, so the tool fails: the run ends failed and the seeder names the session.
    sys.stderr.write("demo tool: no recorded answer for this prompt: %r\n" % prompt.strip()[:80])
    sys.exit(3)


def emit(obj):
    sys.stdout.write(json.dumps(obj, separators=(",", ":")) + "\n")
    sys.stdout.flush()


def lines():
    for line in sys.stdin:
        line = line.strip()
        if line:
            try:
                yield json.loads(line)
            except ValueError:
                sys.stderr.write("demo tool: input is not JSON: %r\n" % line[:80])
                sys.exit(4)


def text_blocks(blocks):
    return " ".join(b.get("text", "") for b in blocks or [] if isinstance(b, dict)).strip()


def pause(seconds=0.05):
    time.sleep(seconds)


# ---------------------------------------------------------------- Claude Code: stream-json
def claude(args):
    if args[:2] == ["auth", "status"]:
        print(json.dumps({"loggedIn": True, "authMethod": "claude.ai", "subscriptionType": "team"}))
        return
    model = args[args.index("--model") + 1] if "--model" in args and args.index("--model") + 1 < len(args) else "claude-sonnet-5-5"
    sid = "sess-%s" % uuid.uuid4().hex[:12] if os.environ.get("DEMO_SESSION_UNIQUE") == "1" else LIVE_SESSION
    if "--resume" in args and args.index("--resume") + 1 < len(args):
        sid = args[args.index("--resume") + 1]
    turns = load_turns("claude")
    emit({"type": "system", "subtype": "init", "session_id": sid, "cwd": os.getcwd(), "model": model,
          "tools": ["Read", "Grep", "Bash", "Edit"], "permissionMode": "default"})
    totals = {"in": 0, "out": 0, "cache": 0, "cost": 0.0}
    for msg in lines():
        if msg.get("type") != "user":
            continue
        content = (msg.get("message") or {}).get("content")
        prompt = content if isinstance(content, str) else text_blocks(content)
        turn = lookup(turns, prompt)
        n = 0
        for step in turn["steps"]:
            n += 1
            if "say" in step:
                emit({"type": "assistant", "session_id": sid, "message": {
                    "role": "assistant", "model": model, "content": [{"type": "text", "text": step["say"]}]}})
            else:
                call = step["tool"]
                tid = "toolu_%s_%d" % (sid[-6:], n)
                emit({"type": "assistant", "session_id": sid, "message": {
                    "role": "assistant", "model": model, "content": [
                        {"type": "tool_use", "id": tid, "name": call["name"], "input": call["input"]}]}})
                emit({"type": "user", "session_id": sid, "message": {
                    "role": "user", "content": [
                        {"type": "tool_result", "tool_use_id": tid, "content": call["output"]}]}})
            pause()
        usage = turn.get("usage", {})
        totals["in"] += usage.get("input", 0)
        totals["out"] += usage.get("output", 0)
        totals["cache"] += usage.get("cache_read", 0)
        totals["cost"] += usage.get("cost_usd", 0.0)
        last = [s["say"] for s in turn["steps"] if "say" in s][-1:]
        emit({"type": "result", "subtype": "success", "session_id": sid, "is_error": False,
              "result": last[0] if last else "", "duration_ms": 1800 * n, "model": model,
              "total_cost_usd": round(totals["cost"], 6),
              "usage": {"input_tokens": totals["in"], "output_tokens": totals["out"],
                        "cache_read_input_tokens": totals["cache"]},
              "modelUsage": {model: {"inputTokens": totals["in"], "outputTokens": totals["out"],
                                     "cacheReadInputTokens": totals["cache"], "costUSD": round(totals["cost"], 6)}}})
        if turn.get("final"):
            return


# ---------------------------------------------------------------- Codex: app-server JSON-RPC
def codex(args):
    if args[:2] == ["login", "status"]:
        print("Logged in using ChatGPT", file=sys.stderr)
        return
    if "app-server" not in args:
        return
    turns = load_turns("codex")
    default_model = offered_models("codex")[0]
    thread, turn_no, usage_total = "thr_%s" % uuid.uuid4().hex[:12], 0, {"in": 0, "cached": 0, "out": 0}
    for frame in lines():
        method, rid, params = frame.get("method"), frame.get("id"), frame.get("params") or {}
        if method is None or rid is None:
            continue  # `initialized` notification or a reply to a request of ours
        if method == "initialize":
            emit({"id": rid, "result": {"userAgent": "demo_codex/0.153.4", "codexHome": os.environ.get("CODEX_HOME", ""),
                                        "platformFamily": "unix", "platformOs": "linux"}})
        elif method == "account/read":
            emit({"id": rid, "result": {"account": {"type": "chatgpt", "email": None, "planType": "pro"},
                                        "requiresOpenaiAuth": False}})
        elif method in ("thread/start", "thread/resume"):
            emit({"id": rid, "result": {
                "thread": {"id": thread, "parentThreadId": None, "agentRole": None, "model": default_model,
                           "cliVersion": "0.153.4", "modelProvider": "openai"},
                "model": params.get("model") or default_model, "modelProvider": "openai",
                "cwd": os.getcwd(), "approvalPolicy": "untrusted",
                "sandbox": {"type": "readOnly", "networkAccess": False}, "approvalsReviewer": "user"}})
        elif method == "turn/start":
            turn_no += 1
            tid = "turn-%d" % turn_no
            prompt = text_blocks(params.get("input"))
            turn = lookup(turns, prompt)
            emit({"id": rid, "result": {"turn": {"id": tid, "status": "inProgress", "items": []}}})
            for i, step in enumerate(turn["steps"], 1):
                if "say" in step:
                    item = {"type": "agentMessage", "id": "msg_%d_%d" % (turn_no, i), "text": step["say"]}
                else:
                    call = step["tool"]
                    item = {"type": "commandExecution", "id": "cmd_%d_%d" % (turn_no, i), "command": call["command"],
                            "aggregatedOutput": call["output"], "status": "completed", "exitCode": 0}
                emit({"method": "item/completed", "params": {"threadId": thread, "turnId": tid, "item": item}})
                pause()
            usage = turn.get("usage", {})
            usage_total["in"] += usage.get("input", 0)
            usage_total["cached"] += usage.get("cache_read", 0)
            usage_total["out"] += usage.get("output", 0)

            def breakdown(t):
                return {"inputTokens": t["in"], "cachedInputTokens": t["cached"], "cacheWriteInputTokens": 0,
                        "outputTokens": t["out"], "reasoningOutputTokens": 0, "totalTokens": t["in"] + t["out"]}
            emit({"method": "thread/tokenUsage/updated", "params": {
                "threadId": thread, "turnId": tid, "tokenUsage": {
                    "total": breakdown(usage_total), "last": breakdown({"in": 1, "cached": 0, "out": 1}),
                    "modelContextWindow": 400000}}})
            emit({"method": "turn/completed", "params": {
                "threadId": thread, "turn": {"id": tid, "status": "completed", "items": []}}})
            if turn.get("final"):
                return
        elif method == "turn/interrupt":
            emit({"id": rid, "result": {}})
        elif method == "thread/unsubscribe":
            emit({"id": rid, "result": {"status": "unsubscribed"}})
        else:
            emit({"id": rid, "error": {"code": -32601, "message": "not implemented: %s" % method}})


# ---------------------------------------------------------------- OpenCode and Gemini CLI: ACP v1
def acp(kind, args):
    tool = "gemini-cli" if kind == "gemini" else "opencode"
    turns = load_turns(tool)
    models = offered_models(tool)
    current = {"model": models[0], "effort": "medium"}
    sid = "ses_%s" % uuid.uuid4().hex[:14]

    def update(body):
        emit({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": body}})

    def config_options():
        return [{"id": "model", "name": "Model", "type": "select", "category": "model",
                 "currentValue": current["model"],
                 "options": [{"value": m, "name": m} for m in models]},
                {"id": "effort", "name": "Effort", "type": "select", "category": "thought_level",
                 "currentValue": current["effort"],
                 "options": [{"value": v, "name": v.capitalize()} for v in ("low", "medium", "high")]}]

    for frame in lines():
        method, rid, params = frame.get("method"), frame.get("id"), frame.get("params") or {}
        if method is None or rid is None:
            continue

        def reply(result):
            emit({"jsonrpc": "2.0", "id": rid, "result": result})
        if method == "initialize":
            reply({"protocolVersion": 1, "authMethods": [],
                   "agentInfo": {"name": "Gemini CLI" if kind == "gemini" else "OpenCode", "version": VERSIONS[kind]},
                   "agentCapabilities": {"loadSession": True, "mcpCapabilities": {"http": True},
                                         "sessionCapabilities": {"list": {}, "close": {}, "resume": {}}}})
        elif method == "session/new":
            result = {"sessionId": sid}
            if kind == "gemini":
                result["modes"] = {"availableModes": [{"id": "default"}, {"id": "plan"}], "currentModeId": "default"}
                result["models"] = {"availableModels": [{"modelId": m} for m in models],
                                    "currentModelId": current["model"]}
            else:
                result["configOptions"] = config_options()
            reply(result)
        elif method in ("session/set_mode", "session/set_model"):
            reply({})
        elif method == "session/set_config_option":
            if params.get("configId") == "model" and params.get("value") in models:
                current["model"] = params["value"]
            if params.get("configId") == "effort" and params.get("value") in ("low", "medium", "high"):
                current["effort"] = params["value"]
            reply({"configOptions": config_options()})
        elif method == "session/prompt":
            prompt = text_blocks(params.get("prompt"))
            turn = lookup(turns, prompt)
            msg = "msg_%s" % uuid.uuid4().hex[:8]
            for i, step in enumerate(turn["steps"], 1):
                if "say" in step:
                    update({"sessionUpdate": "agent_message_chunk", "messageId": msg + str(i),
                            "content": {"type": "text", "text": step["say"]}})
                else:
                    call = step["tool"]
                    cid = "call_%d" % i
                    update({"sessionUpdate": "tool_call", "toolCallId": cid, "title": call["name"],
                            "kind": "execute", "status": "in_progress", "rawInput": {"command": call["command"]}})
                    update({"sessionUpdate": "tool_call_update", "toolCallId": cid, "status": "completed",
                            "content": [{"type": "content", "content": {"type": "text", "text": call["output"]}}]})
                pause()
            reply({"stopReason": "end_turn"})
            if turn.get("final"):
                return
        elif method == "session/close":
            reply({})
            return
        else:
            emit({"jsonrpc": "2.0", "id": rid, "error": {"code": -32601, "message": "not implemented: %s" % method}})


def main():
    kind, args = sys.argv[1], sys.argv[2:]
    if args and args[0] in ("--version", "-v", "version"):
        print(VERSIONS.get(kind, "1.0.0"))
        return 0
    if kind == "claude":
        claude(args)
    elif kind == "codex":
        codex(args)
    elif kind == "opencode":
        if args[:2] == ["auth", "list"]:
            print("┌  Credentials\n│\n●  OpenAI oauth\n│\n└  1 credentials")
            return 0
        if args and args[0] == "acp":
            acp(kind, args)
    elif kind == "gemini":
        if "--acp" in args:
            acp(kind, args)
    return 0


if __name__ == "__main__":
    sys.exit(main())
