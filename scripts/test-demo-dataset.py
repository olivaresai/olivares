#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Battery for the demo dataset: scripts/seed-demo-dataset.py and scripts/demo-tools.

Two halves, neither needs an engine, a network or a vendor account:

  data contract   the recorded conversations and the dataset tables agree with each other, read as
                  plain prose, and carry nothing private
  stand-ins       each AI tool stand-in is run as the real process the engine launches, over its own
                  wire protocol, and answers the recorded transcript (no mock of the process)

The same stand-ins are driven by a real engine in scripts/docs-captures.sh; this battery pins what
that run depends on, so a protocol drift fails here first.

Exit 0 when every case passes, 1 on a failure.

    python3 scripts/test-demo-dataset.py
"""
from __future__ import annotations

import importlib.util
import json
import os
import re
import shutil
import subprocess
import socket
import sys
import tempfile
import time
import unittest
from http.client import HTTPConnection
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
TOOLS = ROOT / "scripts" / "demo-tools"
CONVERSATIONS = json.loads((TOOLS / "conversations.json").read_text(encoding="utf8"))["sessions"]

# Anything that makes a published image private or tied to this host.
PRIVATE = [
    re.compile(r"/workspace|/home/|/Users/|/root\b|externaldisk"),
    re.compile(r"\b(sk-[A-Za-z0-9]{16,}|ghp_[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{12,}|xox[bap]-)"),
    re.compile(r"-----BEGIN [A-Z ]*KEY-----"),
    re.compile(r"\b(?:\d{1,3}\.){3}\d{1,3}\b"),
]
EMAIL = re.compile(r"[\w.+-]+@([\w-]+\.[\w.-]+)")
RESERVED_DOMAINS = {"olivares.local", "acme.example", "example.com", "example.org"}
MARKDOWN = [
    re.compile(r"\*\*[^*\n]+\*\*"),
    re.compile(r"```"),
    re.compile(r"(^|\n)#{1,6} \S"),
    re.compile(r"\[[^\]\n]+\]\([^)\n]+\)"),
    re.compile(r"(^|\s)`[^`\n]+`"),
    re.compile(r"(^|\n)\s*[-*] \S[^\n]*\n\s*[-*] \S"),
]


def load(name: str, filename: str):
    spec = importlib.util.spec_from_file_location(name, ROOT / "scripts" / filename)
    if spec is None or spec.loader is None:
        print("COULD NOT CHECK: cannot load %s" % filename, file=sys.stderr)
        sys.exit(2)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


SEEDER = load("seed_demo_dataset_under_test", "seed-demo-dataset.py")
STAND_IN = load("demo_tool_under_test", "demo-tools/demo-tool.py")

# The release-number and placeholder patterns of web/e2e/capture-target.ts, which would fail a capture of
# these texts. Kept in step by hand: web/e2e/capture-target.spec.ts pins the TypeScript side.
RELEASE = re.compile(
    r"(?:\bv(?=\d)|\b(?:version|release|build|olivares(?: ai)?|engine)\s+v?)%s(?!\d|\.\d)",
    re.I)
PLACEHOLDER = [
    re.compile(r"\b(undefined|NaN)\b|\[object Object\]"),
    re.compile(r"\b(TODO|TBD|FIXME)\b"),
    re.compile(r"lorem ipsum", re.I),
    re.compile(r"\{\{[^}\n]+\}\}"),
]
# The dates and harness names exigirInstanteLimpio refuses (web/e2e/capture-target.ts): a recorded conversation
# that carried one would date every capture of it.
_MONTH = ("Jan(?:uary)?|Feb(?:ruary)?|Mar(?:ch)?|Apr(?:il)?|May|June?|July?|Aug(?:ust)?|Sep(?:t(?:ember)?)?|"
          "Oct(?:ober)?|Nov(?:ember)?|Dec(?:ember)?")
DATES = [
    re.compile(r"\b(?:%s)\.?\s\d{1,2}\b|\b\d{1,2}(?:st|nd|rd|th)?\s(?:%s)\b|\b(?:%s)\.?\s\d{4}\b"
               r"|(?<!\d)\d{4}[./-]\d{1,2}[./-]\d{1,2}(?!\d)|(?<!\d)\d{1,2}[./-]\d{1,2}[./-]\d{4}(?!\d)"
               % (_MONTH, _MONTH, _MONTH))
]
HARNESS_NAMES = [
    re.compile(r"\b(?:docs|demo)-capture[\w-]*|\bdemo-vol-[\w-]*|\btmp\.[a-z0-9]{6,}"),
    re.compile(r"\bdemo-(?=[A-Za-z0-9]*[0-9A-Z])[A-Za-z0-9]{6}\b"),
]
RELEASES_TO_TRY = ("0.1", "0.2", "1.0", "1.1", "2.0", "26.10")


def spoken_texts():
    for s in CONVERSATIONS:
        yield s["name"], s["name"]
        for turn in s["turns"]:
            yield s["name"], turn["prompt"]
            for step in turn["steps"]:
                if "say" in step:
                    yield s["name"], step["say"]
                else:
                    yield s["name"], step["tool"]["output"]
                    yield s["name"], step["tool"]["command"]


class DataContract(unittest.TestCase):
    def test_every_reference_of_a_session_resolves(self):
        profiles = {key: driver for key, driver, _, _ in SEEDER.PROFILES}
        for s in CONVERSATIONS:
            with self.subTest(session=s["name"]):
                self.assertIn(s["profile"], profiles)
                self.assertEqual(profiles[s["profile"]], s["tool"], "the profile runs another tool")
                self.assertIn(s["folder"], SEEDER.WORKSPACES)
                self.assertIn(s["folder"], SEEDER.REPOS)
                if s.get("template"):
                    self.assertIn(s["template"], {t[0] for t in SEEDER.TEMPLATES})
                self.assertTrue(s["turns"])

    def test_a_prompt_names_one_answer_per_tool(self):
        seen = {}
        for s in CONVERSATIONS:
            for turn in s["turns"]:
                key = (s["tool"], turn["prompt"].strip())
                self.assertNotIn(key, seen, "two turns share a prompt: the stand-in could not tell them apart")
                seen[key] = s["name"]
        self.assertEqual(len({s["name"] for s in CONVERSATIONS}), len(CONVERSATIONS), "session names repeat")

    def test_only_the_last_turn_ends_the_session(self):
        for s in CONVERSATIONS:
            with self.subTest(session=s["name"]):
                self.assertEqual([bool(t.get("final")) for t in s["turns"]],
                                 [False] * (len(s["turns"]) - 1) + [True])

    def test_the_four_tools_and_a_local_model_are_all_present(self):
        self.assertEqual({s["tool"] for s in CONVERSATIONS}, {"claude", "codex", "gemini-cli", "opencode"})
        self.assertTrue(any(s.get("local") for s in CONVERSATIONS), "no session runs on a local model")
        self.assertGreaterEqual(len({s["profile"] for s in CONVERSATIONS}), 5)

    def test_conversations_are_plain_prose(self):
        for name, text in spoken_texts():
            for pattern in MARKDOWN:
                with self.subTest(session=name, pattern=pattern.pattern):
                    self.assertIsNone(pattern.search(text), "markdown in: %r" % text[:80])

    def test_no_text_the_capture_checks_would_refuse(self):
        # A conversation is on a screen. The captured screen is refused when it names a release or carries a
        # placeholder, so the recorded text must not, whichever release it is photographed from.
        for name, text in spoken_texts():
            for version in RELEASES_TO_TRY:
                with self.subTest(session=name, version=version):
                    pattern = re.compile(RELEASE.pattern % re.escape(version), re.I)
                    self.assertIsNone(pattern.search(text), "reads as release %s: %r" % (version, text[:80]))
            for pattern in PLACEHOLDER + DATES + HARNESS_NAMES:
                with self.subTest(session=name, pattern=pattern.pattern):
                    self.assertIsNone(pattern.search(text.lower() if pattern in HARNESS_NAMES[:1] else text),
                                      "refused by the capture check: %r" % text[:80])

    def test_the_steps_run_in_the_order_the_engine_needs(self):
        order = [name for name, _ in SEEDER.STEPS]
        self.assertEqual(len(order), len(set(order)))
        before = lambda a, b: self.assertLess(order.index(a), order.index(b), "%s must come before %s" % (a, b))
        before("modules", "providers")
        before("providers", "profiles")  # a profile binds the local provider
        before("profiles", "sessions")
        before("workspaces", "sessions")
        before("templates", "sessions")
        before("approvals", "sessions")  # the launch approval is decided by a person that exists
        before("people", "approvals")
        before("people", "communications")

    def test_the_session_id_contract_holds_across_the_three_files(self):
        # The stand-in reads the variable, the seeder lets it through, the harness sets it for the engine.
        self.assertIn('"DEMO_SESSION_UNIQUE"', (ROOT / "scripts" / "seed-demo-dataset.py").read_text(encoding="utf8"))
        self.assertIn("DEMO_SESSION_UNIQUE", (TOOLS / "demo-tool.py").read_text(encoding="utf8"))
        self.assertIn("DEMO_SESSION_UNIQUE=1", (ROOT / "scripts" / "docs-captures.sh").read_text(encoding="utf8"))

    def test_nothing_private_or_of_this_host(self):
        texts = list(spoken_texts())
        for files in SEEDER.REPOS.values():
            texts += [("repository", body) for body in files.values()]
        for name, text in texts:
            for pattern in PRIVATE:
                with self.subTest(where=name, pattern=pattern.pattern):
                    self.assertIsNone(pattern.search(text), "private or host-specific text: %r" % text[:80])
            for domain in EMAIL.findall(text):
                self.assertIn(domain, RESERVED_DOMAINS, "an address on a real domain: %r" % text[:80])
        for _, email, _ in SEEDER.PEOPLE:
            self.assertIn(email.split("@")[1], RESERVED_DOMAINS)

    def test_the_dataset_tables_are_consistent(self):
        keys = [p[0] for p in SEEDER.PROFILES]
        self.assertEqual(len(keys), len(set(keys)))
        self.assertEqual(len({p[3] for p in SEEDER.PROFILES}), len(SEEDER.PROFILES), "account names repeat")
        self.assertEqual(len({t[0] for t in SEEDER.TEMPLATES}), len(SEEDER.TEMPLATES))
        # A template that lists tools only runs under dontAsk: the engine refuses the rest.
        for name, _, body in SEEDER.TEMPLATES:
            if body["policies"].get("allowed_tools"):
                self.assertEqual(body["settings"]["permission_mode"], "dontAsk", name)
            self.assertNotIn("dlp_mode", body["policies"], "a native launch cannot enforce a DLP mode: " + name)
        # The skills pack is read from a folder of the seeded repository.
        self.assertTrue(any(p.startswith(".agents/skills/") for p in SEEDER.REPOS["payments-api"]))

    def test_no_deny_policy_is_written_to_the_running_engine(self):
        # On the released 0.1 engine a runtime ABAC policy makes authority evidence unavailable: the skills import
        # and the communications plane stop working until a restart. Run every step against a recording API: the
        # only policies written are approval policies.
        written = []

        class Recorder(StubApi):
            def call(self, method, path, body=None, headers=None, token=None, anonymous=False):
                if method != "GET" and "polic" in path:
                    written.append((path, (body or {}).get("kind")))
                return 200, {"items": [], "modules": []}

        for name, step in SEEDER.STEPS:
            try:
                step(Recorder({}), {"root": tempfile.gettempdir(), "ollama_url": "http://127.0.0.1:1"})
            except Exception:  # the recording answers are empty on purpose; only what is written matters here
                pass
        self.assertTrue(written, "no step wrote a policy: the recording did not reach step_policies")
        self.assertEqual({kind for _, kind in written}, {"approval"}, "a step wrote a policy that is not an approval")

    def test_approval_requests_do_not_collide_with_the_launch_gate(self):
        # The engine's own gate asks `sessions.run.launch`; a seeded request of that action would hold the
        # dataset's own conversations. And a request is told apart by (action, subject): they must differ.
        pairs = [(action, subject) for action, _, subject, *_ in SEEDER.APPROVALS]
        self.assertEqual(len(pairs), len(set(pairs)))
        for action, *_ in SEEDER.APPROVALS:
            self.assertNotEqual(action, "sessions.run.launch")


class SeederGuards(unittest.TestCase):
    """The seeder creates administrator accounts with a published password: it must refuse anything but a
    loopback engine, and never take the token from argv. Both refusals come before any request."""

    def seed(self, *argv: str, token: str | None = "demo-token-for-the-test"):
        env = {"PATH": os.environ.get("PATH", "")}
        if token:
            env["OLIVARES_TOKEN"] = token
        return subprocess.run([sys.executable, "-I", str(ROOT / "scripts" / "seed-demo-dataset.py"), *argv],
                              capture_output=True, text=True, timeout=30, env=env)

    def test_refuses_a_host_that_is_not_loopback(self):
        for url in ("https://olivares.example.com", "http://10.0.0.5:8466", "http://203.0.113.9"):
            with self.subTest(url=url):
                proc = self.seed(url, "-", "tenant", "/nonexistent")
                self.assertEqual(proc.returncode, 2)
                self.assertIn("loopback", proc.stderr)

    def test_refuses_a_token_on_argv(self):
        proc = self.seed("http://127.0.0.1:1", "a-literal-token-value", "tenant", "/nonexistent")
        self.assertEqual(proc.returncode, 2)
        self.assertIn("OLIVARES_TOKEN", proc.stderr)
        self.assertNotIn("a-literal-token-value", proc.stderr, "the refusal must not echo the token")

    def test_needs_the_token_in_the_environment(self):
        proc = self.seed("http://127.0.0.1:1", "-", "tenant", "/nonexistent", token=None)
        self.assertEqual(proc.returncode, 2)

    def test_a_loopback_engine_that_is_down_fails_instead_of_passing(self):
        with tempfile.TemporaryDirectory() as root:
            proc = self.seed("http://127.0.0.1:9", "-", "tenant", root)
        self.assertNotEqual(proc.returncode, 0)


def run_tool(kind: str, args: list[str], stdin_lines: list[dict] | None = None, env: dict | None = None):
    """Run a stand-in exactly as the engine does: argv and NDJSON on stdin."""
    payload = "".join(json.dumps(f) + "\n" for f in (stdin_lines or []))
    proc = subprocess.run(
        [sys.executable, "-I", str(TOOLS / "demo-tool.py"), kind, *args],
        input=payload, capture_output=True, text=True, timeout=30,
        env={"PATH": os.environ.get("PATH", ""), **(env or {})},
    )
    # A status command prints plain text; a session prints one JSON frame per line.
    frames = [json.loads(line) for line in proc.stdout.splitlines() if line.lstrip().startswith("{")]
    return proc, frames


def session(tool: str, name: str) -> dict:
    return next(s for s in CONVERSATIONS if s["tool"] == tool and s["name"] == name)


def replies(turn: dict) -> list[str]:
    return [step["say"] for step in turn["steps"] if "say" in step]


class FinalTurn(unittest.TestCase):
    """The engine keeps a session's stdin open. Only the `final` turn ends the process, so a tool that
    waited for end-of-input would pass a test that closes stdin and hang a real session."""

    def exits_by_itself(self, argv, frames):
        proc = subprocess.Popen([sys.executable, "-I", str(TOOLS / "demo-tool.py"), *argv], stdin=subprocess.PIPE,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            for frame in frames:
                proc.stdin.write(json.dumps(frame) + "\n")
                proc.stdin.flush()
            return proc.wait(timeout=15)
        except subprocess.TimeoutExpired:
            return None
        finally:
            proc.kill()
            proc.communicate()

    def test_every_tool_ends_after_its_final_turn_with_stdin_open(self):
        sessions = {s["tool"]: s for s in CONVERSATIONS}
        claude = [{"type": "user", "message": {"role": "user", "content": t["prompt"]}}
                  for t in sessions["claude"]["turns"]]
        self.assertEqual(self.exits_by_itself(["claude"], claude), 0)
        codex = [{"id": 1, "method": "initialize"}, {"id": 2, "method": "thread/start", "params": {}}] + [
            {"id": 3 + i, "method": "turn/start", "params": {"input": [{"type": "text", "text": t["prompt"]}]}}
            for i, t in enumerate(sessions["codex"]["turns"])]
        self.assertEqual(self.exits_by_itself(["codex", "app-server"], codex), 0)
        for kind, tool, argv in (("opencode", "opencode", ["opencode", "acp"]), ("gemini", "gemini-cli", ["gemini", "--acp"])):
            acp = [{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}},
                   {"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": {}}] + [
                {"jsonrpc": "2.0", "id": 3 + i, "method": "session/prompt",
                 "params": {"prompt": [{"type": "text", "text": t["prompt"]}]}}
                for i, t in enumerate(sessions[tool]["turns"])]
            self.assertEqual(self.exits_by_itself(argv, acp), 0, kind)

    def test_a_turn_that_is_not_final_keeps_the_session_open(self):
        first = CONVERSATIONS[0]["turns"][0]
        self.assertFalse(first.get("final"))
        proc = subprocess.Popen([sys.executable, "-I", str(TOOLS / "demo-tool.py"), "claude"], stdin=subprocess.PIPE,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            proc.stdin.write(json.dumps({"type": "user", "message": {"role": "user", "content": first["prompt"]}}) + "\n")
            proc.stdin.flush()
            time.sleep(1.0)
            self.assertIsNone(proc.poll(), "the session ended before its final turn")
        finally:
            proc.kill()
            proc.communicate()


class ClaudeStandIn(unittest.TestCase):
    def test_status_says_signed_in(self):
        proc, _ = run_tool("claude", ["auth", "status", "--json"])
        status = json.loads(proc.stdout)
        self.assertTrue(status["loggedIn"])
        self.assertNotIn("email", status, "a stand-in must not invent an account address")

    def test_a_recorded_conversation_is_answered_and_ends(self):
        s = next(x for x in CONVERSATIONS if x["tool"] == "claude")
        inputs = [{"type": "user", "message": {"role": "user", "content": [{"type": "text", "text": t["prompt"]}]}}
                  for t in s["turns"]]
        proc, frames = run_tool("claude", ["--model", s["model"]], inputs, {"DEMO_SESSION_UNIQUE": "1"})
        self.assertEqual(proc.returncode, 0)
        self.assertEqual(frames[0]["type"], "system")
        self.assertEqual(frames[0]["subtype"], "init")
        said = [c["text"] for f in frames if f["type"] == "assistant"
                for c in f["message"]["content"] if c["type"] == "text"]
        for turn in s["turns"]:
            for reply in replies(turn):
                self.assertIn(reply, said)
        self.assertEqual(sum(1 for f in frames if f["type"] == "result"), len(s["turns"]))
        # Cost is cumulative across turns, as the engine's usage account expects.
        results = [f for f in frames if f["type"] == "result"]
        self.assertLess(results[0]["total_cost_usd"], results[-1]["total_cost_usd"])

    def test_an_unrecorded_prompt_fails_the_tool_instead_of_inventing_an_answer(self):
        proc, frames = run_tool("claude", [], [
            {"type": "user", "message": {"role": "user", "content": "something nobody recorded"}}])
        self.assertEqual(proc.returncode, 3)
        self.assertIn("no recorded answer", proc.stderr)
        self.assertEqual([f for f in frames if f.get("type") in ("assistant", "result")], [],
                         "a reply was published for a prompt nobody recorded")

    def test_input_that_is_not_json_fails_the_tool(self):
        proc = subprocess.run([sys.executable, "-I", str(TOOLS / "demo-tool.py"), "claude"], input="not json\n",
                              capture_output=True, text=True, timeout=30)
        self.assertEqual(proc.returncode, 4)

    def test_the_session_id_joins_the_observed_session_unless_unique(self):
        _, fixed = run_tool("claude", [], [], {})
        self.assertEqual(fixed[0]["session_id"], STAND_IN.LIVE_SESSION)
        _, unique = run_tool("claude", [], [], {"DEMO_SESSION_UNIQUE": "1"})
        self.assertNotEqual(unique[0]["session_id"], fixed[0]["session_id"])
        _, resumed = run_tool("claude", ["--resume", "sess-other"], [], {"DEMO_SESSION_UNIQUE": "1"})
        self.assertEqual(resumed[0]["session_id"], "sess-other")


class CodexStandIn(unittest.TestCase):
    def test_status_says_signed_in(self):
        proc, _ = run_tool("codex", ["login", "status"])
        self.assertTrue((proc.stdout + proc.stderr).lower().startswith("logged in"))

    def test_the_app_server_handshake_and_a_turn(self):
        s = next(x for x in CONVERSATIONS if x["tool"] == "codex")
        frames_in = [
            {"id": 1, "method": "initialize", "params": {"clientInfo": {"name": "olivares", "version": "0"}}},
            {"method": "initialized"},
            {"id": 2, "method": "account/read", "params": {"refreshToken": False}},
            {"id": 3, "method": "thread/start", "params": {"cwd": "/tmp", "model": s["model"]}},
        ]
        for i, turn in enumerate(s["turns"]):
            frames_in.append({"id": 4 + i, "method": "turn/start", "params": {
                "threadId": "t", "input": [{"type": "text", "text": turn["prompt"]}]}})
        proc, frames = run_tool("codex", ["app-server", "--listen", "stdio://"], frames_in)
        self.assertEqual(proc.returncode, 0)
        answers = {f["id"]: f for f in frames if "id" in f}
        self.assertIn("userAgent", answers[1]["result"])
        self.assertEqual(answers[2]["result"]["account"]["type"], "chatgpt")
        thread = answers[3]["result"]["thread"]["id"]
        self.assertTrue(thread)
        notes = [f for f in frames if "method" in f]
        messages = [n["params"]["item"]["text"] for n in notes
                    if n["method"] == "item/completed" and n["params"]["item"]["type"] == "agentMessage"]
        for turn in s["turns"]:
            for reply in replies(turn):
                self.assertIn(reply, messages)
        self.assertEqual(sum(1 for n in notes if n["method"] == "turn/completed"), len(s["turns"]))
        usage = [n for n in notes if n["method"] == "thread/tokenUsage/updated"]
        self.assertEqual(len(usage), len(s["turns"]))
        self.assertTrue(all(n["params"]["threadId"] == thread for n in usage))
        totals = [n["params"]["tokenUsage"]["total"]["outputTokens"] for n in usage]
        self.assertEqual(totals, sorted(totals), "token totals must only grow")

    def test_a_thread_without_a_model_gets_the_first_recorded_one(self):
        first = next(x["model"] for x in CONVERSATIONS if x["tool"] == "codex")
        _, frames = run_tool("codex", ["app-server"], [{"id": 1, "method": "thread/start", "params": {}}])
        result = frames[0]["result"]
        self.assertEqual(result["model"], first)
        self.assertEqual(result["thread"]["model"], first)

    def test_a_method_it_does_not_implement_is_refused_not_ignored(self):
        _, frames = run_tool("codex", ["app-server"], [{"id": 9, "method": "something/unknown"}])
        self.assertEqual(frames[0]["error"]["code"], -32601)


class AcpStandIns(unittest.TestCase):
    def drive(self, kind: str, tool: str, extra: list[dict]):
        s = next(x for x in CONVERSATIONS if x["tool"] == tool and not x.get("local"))
        flow = [{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": 1}},
                {"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": {"cwd": "/tmp", "mcpServers": []}}]
        flow += extra
        for i, turn in enumerate(s["turns"]):
            flow.append({"jsonrpc": "2.0", "id": 20 + i, "method": "session/prompt", "params": {
                "sessionId": "x", "prompt": [{"type": "text", "text": turn["prompt"]}]}})
        args = ["acp"] if kind == "opencode" else ["--acp"]
        proc, frames = run_tool(kind, args, flow)
        self.assertEqual(proc.returncode, 0)
        return s, frames

    def check_turns(self, s, frames):
        chunks = [f["params"]["update"]["content"]["text"] for f in frames
                  if f.get("method") == "session/update"
                  and f["params"]["update"]["sessionUpdate"] == "agent_message_chunk"]
        for turn in s["turns"]:
            for reply in replies(turn):
                self.assertIn(reply, chunks)
        stops = [f["result"]["stopReason"] for f in frames if "result" in f and "stopReason" in f["result"]]
        self.assertEqual(stops, ["end_turn"] * len(s["turns"]))

    def test_opencode_selects_a_model_it_advertised_and_echoes_it(self):
        s = next(x for x in CONVERSATIONS if x["tool"] == "opencode" and not x.get("local"))
        s, frames = self.drive("opencode", "opencode", [
            {"jsonrpc": "2.0", "id": 5, "method": "session/set_config_option",
             "params": {"sessionId": "x", "configId": "model", "value": s["model"]}}])
        new = next(f for f in frames if f.get("id") == 2)["result"]
        model = next(o for o in new["configOptions"] if o["id"] == "model")
        offered = [o["value"] for o in model["options"]]
        self.assertIn(s["model"], offered)
        set_reply = next(f for f in frames if f.get("id") == 5)["result"]["configOptions"]
        self.assertEqual(next(o for o in set_reply if o["id"] == "model")["currentValue"], s["model"])
        self.check_turns(s, frames)

    def test_a_local_model_is_advertised_under_the_engines_provider_id(self):
        local = next(x for x in CONVERSATIONS if x.get("local"))
        _, frames = run_tool("opencode", ["acp"], [
            {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": 1}},
            {"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": {"cwd": "/tmp", "mcpServers": []}}])
        options = next(f for f in frames if f.get("id") == 2)["result"]["configOptions"]
        offered = [o["value"] for o in next(o for o in options if o["id"] == "model")["options"]]
        self.assertIn("olivares_ollama/" + local["model"], offered)

    def test_gemini_offers_modes_and_models_and_takes_both(self):
        s, frames = self.drive("gemini", "gemini-cli", [
            {"jsonrpc": "2.0", "id": 6, "method": "session/set_mode", "params": {"sessionId": "x", "modeId": "default"}},
            {"jsonrpc": "2.0", "id": 7, "method": "session/set_model", "params": {"sessionId": "x", "modelId": "gemini-2.5-pro"}}])
        new = next(f for f in frames if f.get("id") == 2)["result"]
        self.assertIn("default", [m["id"] for m in new["modes"]["availableModes"]])
        self.assertIn(s["model"], [m["modelId"] for m in new["models"]["availableModels"]])
        self.assertEqual(next(f for f in frames if f.get("id") == 6)["result"], {})
        self.check_turns(s, frames)

    def test_status_commands(self):
        proc, _ = run_tool("opencode", ["auth", "list"])
        self.assertIn("OpenAI oauth", proc.stdout)
        # Gemini's sign-in is read from files, so its stand-in has no status command to answer.
        proc, _ = run_tool("gemini", ["--version"])
        self.assertEqual(proc.stdout.strip(), STAND_IN.VERSIONS["gemini"])


class BuiltExecutables(unittest.TestCase):
    """A confined session child reads only its own program: the built file must stand alone."""

    def test_each_tool_runs_without_its_neighbors(self):
        with tempfile.TemporaryDirectory() as out, tempfile.TemporaryDirectory() as cwd:
            subprocess.run([sys.executable, "-I", str(TOOLS / "build-demo-tools.py"), out], check=True, timeout=30)
            self.assertEqual(sorted(os.listdir(out)), ["claude", "codex", "gemini", "opencode"])
            first = {t: next(s for s in CONVERSATIONS if s["tool"] == t) for t in ("claude", "codex", "gemini-cli", "opencode")}

            def run(name, argv, frames):
                built = os.path.join(out, name)
                # The engine executes the file itself; this host may mount a temporary directory noexec, so the
                # run goes through the interpreter and the file is checked for what exec needs.
                self.assertEqual(os.stat(built).st_mode & 0o111, 0o111, "%s is not executable" % name)
                with open(built, encoding="utf8") as fh:
                    self.assertEqual(fh.readline().strip(), "#!/usr/bin/env python3")
                proc = subprocess.run([sys.executable, "-I", built, *argv],
                                      input="".join(json.dumps(f) + "\n" for f in frames), cwd=cwd,
                                      capture_output=True, text=True, timeout=30, env={"PATH": os.environ["PATH"]})
                self.assertEqual(proc.returncode, 0, "%s: %s" % (name, proc.stderr[-200:]))
                return proc.stdout

            turn = lambda tool: first[tool]["turns"][0]
            last_reply = lambda tool: replies(turn(tool))[-1]
            self.assertIn(last_reply("claude"), run("claude", [], [
                {"type": "user", "message": {"role": "user", "content": turn("claude")["prompt"]}}]))
            self.assertIn(last_reply("codex"), run("codex", ["app-server"], [
                {"id": 1, "method": "initialize", "params": {}}, {"id": 2, "method": "thread/start", "params": {}},
                {"id": 3, "method": "turn/start", "params": {"threadId": "t", "input": [
                    {"type": "text", "text": turn("codex")["prompt"]}]}}]))
            for name, tool, argv in (("opencode", "opencode", ["acp"]), ("gemini", "gemini-cli", ["--acp"])):
                self.assertIn(last_reply(tool), run(name, argv, [
                    {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}},
                    {"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": {}},
                    {"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": {
                        "prompt": [{"type": "text", "text": turn(tool)["prompt"]}]}}]))

    def test_the_local_model_endpoint_serves_what_the_sessions_use(self):
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 0))
            port = probe.getsockname()[1]
        proc = subprocess.Popen([sys.executable, "-I", str(TOOLS / "demo-ollama.py"), str(port)])
        try:
            body = None
            for _ in range(50):
                self.assertIsNone(proc.poll(), "the endpoint exited")
                try:
                    conn = HTTPConnection("127.0.0.1", port, timeout=2)
                    conn.request("GET", "/api/tags")
                    body = json.loads(conn.getresponse().read())
                    break
                except OSError:
                    time.sleep(0.1)
            self.assertIsNotNone(body, "the endpoint never answered")
            self.assertEqual(sorted(m["name"] for m in body["models"]),
                             sorted({s["model"] for s in CONVERSATIONS if s.get("local")}))
        finally:
            proc.terminate()
            proc.wait(timeout=10)


class PublishBlock(unittest.TestCase):
    """The PUBLICAR leg of scripts/docs-captures.sh, run alone on a scratch repository: it needs no engine."""

    def run_leg(self, with_manifest: bool, referenced: bool = True):
        text = (ROOT / "scripts" / "docs-captures.sh").read_text(encoding="utf8")
        start = text.index('if [ "${PUBLICAR:-0}" = "1" ]; then')
        leg = text[start:text.index("\nexit $RC", start)]
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        repo = Path(tmp.name)
        shots, published = repo / "web" / "playwright-report" / "docs", repo / "docs-site" / "public" / "console"
        shots.mkdir(parents=True)
        published.mkdir(parents=True)
        (repo / "docs-site" / "src").mkdir(parents=True)
        (repo / "scripts" / "lib").mkdir(parents=True)
        shutil.copyfile(ROOT / "scripts" / "lib" / "capture-refs.sh",
                        repo / "scripts" / "lib" / "capture-refs.sh")
        (repo / "README.md").write_text("console/view-light.png" if referenced else "No captures")
        (shots / "view-light.png").write_bytes(b"new")
        (published / "view-light.png").write_bytes(b"tracked")
        if with_manifest:
            (shots / "manifest.json").write_text("{}")
        git = ["git", "-c", "user.name=t", "-c", "user.email=t@localhost", "-C", str(repo)]
        for step in (["init", "-q"], ["add", "-f", "-A"], ["commit", "-q", "-m", "tree"]):
            subprocess.run(git + step, check=True, capture_output=True)
        env = {"PATH": os.environ["PATH"], "HOME": str(repo), "ROOT": str(repo), "RC": "0", "PUBLICAR": "1"}
        proc = subprocess.run(["bash", "-c", "set -euo pipefail\n" + leg], env=env, capture_output=True, text=True,
                              timeout=60)
        return proc, (published / "view-light.png").read_bytes()

    def test_without_a_manifest_no_published_image_is_overwritten(self):
        proc, image = self.run_leg(with_manifest=False)
        self.assertEqual(proc.returncode, 2, proc.stderr)
        self.assertIn("no manifest.json", proc.stderr)
        self.assertEqual(image, b"tracked")

    def test_with_a_manifest_the_captures_are_published(self):
        proc, image = self.run_leg(with_manifest=True)
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(image, b"new")

    def test_an_unnamed_capture_is_not_published(self):
        proc, image = self.run_leg(with_manifest=True, referenced=False)
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(image, b"tracked")
        self.assertIn("NOT PUBLISHED 1 capture(s)", proc.stderr)


class StubApi(SEEDER.Api):
    """The engine's answers for the seeder's own decisions, from a table: what the seeder does with a
    list, a decision or a refusal is its logic, and the product contract is exercised against a real
    engine by scripts/test-demo-dataset-live.sh."""

    def __init__(self, routes):
        super().__init__("http://127.0.0.1:1", "t", "tenant")
        self.routes, self.calls = routes, []

    def call(self, method, path, body=None, headers=None, token=None, anonymous=False):
        self.calls.append((method, path.split("?")[0], token))
        for (m, prefix), answer in self.routes.items():
            if m == method and path.startswith(prefix):
                return answer(body) if callable(answer) else answer
        raise AssertionError("unexpected call: %s %s" % (method, path))


class SeederLogic(unittest.TestCase):
    def test_a_list_that_says_more_follows_without_a_cursor_is_an_error(self):
        api = StubApi({("GET", "/v1/x"): (200, {"items": [{"id": 1}], "has_more": True})})
        with self.assertRaisesRegex(SEEDER.SeedError, "no cursor"):
            api.items("x", "/v1/x")

    def test_a_list_without_items_is_an_error_not_an_empty_list(self):
        api = StubApi({("GET", "/v1/x"): (200, {"entries": []})})
        with self.assertRaises(SEEDER.SeedError):
            api.items("x", "/v1/x")

    def approvals_api(self, statuses):
        existing = []
        for action, kind, subject, reason, required, decision in SEEDER.APPROVALS:
            status = statuses(decision) if decision else "pending"
            existing.append({"id": "id-" + subject, "action": action, "subject_ref": subject, "status": status})
        decided = []
        api = StubApi({
            ("GET", "/v1/m/governance/approvals"): lambda _: (200, {"items": existing, "has_more": False}),
            ("POST", "/v1/m/governance/approvals/"): lambda body: (decided.append(body["decision"]) or (200, {})),
            ("POST", "/v1/auth/login"): (200, {"token": "reviewer-token"}),
        })
        return api, decided

    def test_a_second_pass_creates_no_approval_and_decides_a_left_pending_one(self):
        reviewer_email = next(p[1] for p in SEEDER.PEOPLE if p[2] == "admin")
        api, decided = self.approvals_api(lambda decision: "pending")
        SEEDER.step_approvals(api, {})
        creations = [c for c in api.calls if c[0] == "POST" and c[1] == "/v1/m/governance/approvals"]
        self.assertEqual(creations, [], "an approval that exists was created again")
        wanted = sorted(d for *_, d in SEEDER.APPROVALS if d)
        self.assertEqual(sorted(decided), wanted, "a request left pending by an interrupted run was not decided")
        self.assertTrue(all(c[2] == "reviewer-token" for c in api.calls if c[1].endswith("/decisions")),
                        "a decision was made with the token that created the request")
        self.assertIn(reviewer_email, [p[1] for p in SEEDER.PEOPLE])

    def test_a_request_that_was_decided_is_not_decided_again(self):
        api, decided = self.approvals_api(lambda decision: "approved" if decision == "approve" else "rejected")
        SEEDER.step_approvals(api, {})
        self.assertEqual(decided, [], "a decided request received a second decision")

    def business_actions_written(self, edition):
        """The approval actions the policies and approval requests of one pass hold, for an estate of `edition`."""
        created = []

        def made(body):
            created.append(body)
            return 201, {"id": "id-%d" % len(created)}

        api = StubApi({
            ("GET", "/v1/m/governance/approvals"): (200, {"items": [], "has_more": False}),
            ("GET", "/v1/m/governance/policies"): (200, {"items": [], "has_more": False}),
            ("POST", "/v1/m/governance/approvals"): made,
            ("POST", "/v1/m/governance/policies"): made,
            ("POST", "/v1/auth/login"): (200, {"token": "reviewer-token"}),
        })
        ctx = {"edition": edition}
        SEEDER.step_policies(api, ctx)
        SEEDER.step_approvals(api, ctx)
        return {(b.get("action") or b["spec"]["match"]["action"]) for b in created if "decision" not in b}

    def test_a_community_estate_holds_no_policy_or_approval_for_a_business_action(self):
        business = {"deploy.promote", "evidence.export"}
        community = self.business_actions_written("community")
        self.assertTrue(community, "a Community estate holds no approval at all")
        self.assertEqual(community & business, set(), "a Community estate holds a Business approval kind")
        self.assertTrue(business <= self.business_actions_written("enterprise"),
                        "a Business estate lost the approval kinds only it can act on")

    def test_the_edition_is_the_first_step_so_nothing_is_seeded_before_it_is_known(self):
        self.assertEqual(SEEDER.STEPS[0][0], "edition")

    def test_the_business_lists_are_one_place_and_a_non_community_edition_keeps_everything(self):
        import demo_edition as editions

        self.assertEqual(editions.BUSINESS_APPROVAL_ACTIONS, {"deploy.promote", "evidence.export"})
        self.assertEqual(editions.BUSINESS_SURFACES, {"deploy", "redteam"})
        for action in editions.BUSINESS_APPROVAL_ACTIONS:
            self.assertFalse(editions.keeps_action("community", action))
            self.assertTrue(editions.keeps_action("enterprise", action))
            self.assertTrue(editions.keeps_action("business", action))
        for surface in editions.BUSINESS_SURFACES:
            self.assertFalse(editions.keeps_surface("community", surface))
            self.assertTrue(editions.keeps_surface("enterprise", surface))
        self.assertTrue(editions.keeps_action("community", "policy.publish"))
        self.assertTrue(editions.keeps_surface("community", "agents"))

    def test_the_edition_is_read_from_the_engine_and_never_guessed(self):
        ctx = {}
        SEEDER.step_edition(StubApi({("GET", "/v1/server-info"): (200, {"edition": "community"})}), ctx)
        self.assertEqual(ctx["edition"], "community")
        for answer in ((200, {}), (200, {"edition": "mystery"}), (503, {"error": "down"})):
            with self.assertRaisesRegex(SEEDER.SeedError, "edition"):
                SEEDER.step_edition(StubApi({("GET", "/v1/server-info"): answer}), {})

    def communications_api(self, channels, inbox, creation=(201, None), deliver=True):
        sent = []
        created = []

        def create(body):
            if creation[0] not in (200, 201):
                return creation
            created.append(body["slug"])
            return 201, {"channel": {"id": "ch-" + body["slug"]}}

        api = StubApi({
            ("GET", "/v1/workspaces"): (200, {"items": [{"id": "ws", "is_default": True}]}),
            ("GET", "/v1/auth/whoami"): (200, {"actor": "user:me-id"}),
            ("GET", "/v1/members"): (200, {"items": [{"email": p[1], "user_id": "u-" + p[1]} for p in SEEDER.PEOPLE],
                                         "has_more": False}),
            ("GET", "/v1/m/sessions/channels"): (200, {"items": [{"slug": s, "id": "ch-" + s} for s in channels],
                                                       "has_more": False}),
            ("POST", "/v1/m/sessions/channels"): create,
            ("GET", "/v1/m/sessions/inbox"): lambda _: (200, {"items": inbox + (
                [{"message": {"channel_id": c, "content": {"subject": s}}} for c, s in sent] if deliver else []),
                "has_more": False}),
            ("POST", "/v1/m/sessions/messages/send"): lambda body: (sent.append(
                (body["channel_id"], body["content"]["subject"])) or (200, {})),
            ("POST", "/v1/auth/login"): (200, {"token": "sender-token"}),
        })
        return api, created, sent

    def test_communications_send_only_what_is_missing(self):
        slugs = [c[0] for c in SEEDER.CHANNELS]
        everything = [{"message": {"channel_id": "ch-" + slug, "content": {"subject": subject}}}
                      for slug, subject, _ in SEEDER.MESSAGES]
        api, created, sent = self.communications_api(slugs, everything)
        SEEDER.step_communications(api, {})
        self.assertEqual((created, sent), ([], []), "a second pass created or sent something")
        # A run that stopped after the first message: the channels exist, one message is missing.
        api, created, sent = self.communications_api(slugs, everything[:-1])
        SEEDER.step_communications(api, {})
        slug, subject, _ = SEEDER.MESSAGES[-1]
        self.assertEqual((created, sent), ([], [("ch-" + slug, subject)]))
        # A run that stopped after the channels: every message is sent.
        api, created, sent = self.communications_api(slugs, [])
        SEEDER.step_communications(api, {})
        self.assertEqual(len(sent), len(SEEDER.MESSAGES))

    def test_a_message_that_never_reaches_the_inbox_is_an_error(self):
        slugs = [c[0] for c in SEEDER.CHANNELS]
        api, _, sent = self.communications_api(slugs, [], deliver=False)
        with self.assertRaisesRegex(SEEDER.SeedError, "inbox does not hold"):
            SEEDER.step_communications(api, {})
        self.assertEqual(len(sent), len(SEEDER.MESSAGES), "the sends were attempted before the check")

    def test_a_missing_channel_is_created_and_its_messages_sent(self):
        api, created, sent = self.communications_api([], [])
        SEEDER.step_communications(api, {})
        self.assertEqual(created, [c[0] for c in SEEDER.CHANNELS])
        self.assertEqual(len(sent), len(SEEDER.MESSAGES))

    def test_an_unavailable_communications_plane_is_an_error_unless_left_out(self):
        unavailable = (503, {"code": "evidence_unavailable"})
        api, _, sent = self.communications_api([], [], creation=unavailable)
        with self.assertRaisesRegex(SEEDER.SeedError, "unavailable"):
            SEEDER.step_communications(api, {})
        self.assertEqual(sent, [])
        api, created, sent = self.communications_api([], [], creation=unavailable)
        SEEDER.step_communications(api, {"no_communications": True})
        self.assertEqual((created, sent), ([], []))
        self.assertEqual(api.calls, [], "a skipped step still called the engine")

    def test_an_approvals_list_that_cannot_be_matched_is_refused(self):
        api = StubApi({("GET", "/v1/m/governance/approvals"): (200, {"items": [{"id": "a", "action": "x"}]})})
        with self.assertRaises(SEEDER.SeedError):
            SEEDER.step_approvals(api, {})

    def test_the_launch_approval_is_the_one_that_appeared_after_the_launch(self):
        stale = {"id": "old", "action": "sessions.run.launch", "status": "pending"}
        fresh = {"id": "new", "action": "sessions.run.launch", "status": "pending"}
        listing = [[stale], [stale, fresh]]
        decided = []
        api = StubApi({
            ("GET", "/v1/m/governance/approvals"): lambda _: (200, {"items": listing.pop(0) if len(listing) > 1 else listing[0]}),
            ("POST", "/v1/m/governance/approvals/"): lambda body: (decided.append(body["decision"]) or (200, {})),
            ("POST", "/v1/auth/login"): (200, {"token": "reviewer-token"}),
        })
        before = SEEDER.pending_launches(api)
        self.assertEqual(before, {"old"})
        SEEDER.approve_launch(api, "a session", before, "ok")
        self.assertTrue(any(c[1] == "/v1/m/governance/approvals/new/decisions" for c in api.calls))
        self.assertFalse(any(c[1] == "/v1/m/governance/approvals/old/decisions" for c in api.calls))

    def test_no_new_launch_request_is_an_error(self):
        stale = {"id": "old", "action": "sessions.run.launch", "status": "pending"}
        api = StubApi({("GET", "/v1/m/governance/approvals"): (200, {"items": [stale]})})
        original = SEEDER.time.sleep
        SEEDER.time.sleep = lambda _: None
        try:
            with self.assertRaises(SEEDER.SeedError):
                SEEDER.approve_launch(api, "a session", {"old"}, "ok")
        finally:
            SEEDER.time.sleep = original

    def test_budgets_skip_only_a_build_that_does_not_serve_them(self):
        refusal = {"error": {"code": "module_error", "message": "FinOps is a Business feature: https://olivares.ai/pricing"}}
        served = StubApi({("GET", "/v1/m/finops/budgets"): (200, {"items": []}),
                          ("POST", "/v1/m/finops/budgets"): (501, refusal)})
        SEEDER.step_budgets(served, {})  # reported, not raised
        off = StubApi({("GET", "/v1/m/finops/budgets"): (404, {"error": {"code": "module_not_enabled"}})})
        SEEDER.step_budgets(off, {})
        for status in (401, 403, 500, 503):
            broken = StubApi({("GET", "/v1/m/finops/budgets"): (status, {"error": {"code": "x"}})})
            with self.subTest(status=status), self.assertRaises(SEEDER.SeedError):
                SEEDER.step_budgets(broken, {})

    def test_a_session_that_exists_and_did_not_finish_is_an_error(self):
        name = CONVERSATIONS[0]["name"]
        api = StubApi({("GET", "/v1/m/sessions/runs"): (200, {"items": [{"name": name, "state": "running"}]})})
        with self.assertRaises(SEEDER.SeedError):
            SEEDER.step_sessions(api, {"workspaces": {}, "profiles": {}, "templates": {}})

    def test_an_unreachable_engine_names_itself(self):
        proc = subprocess.run([sys.executable, "-I", str(ROOT / "scripts" / "seed-demo-dataset.py"),
                               "http://127.0.0.1:9", "-", "tenant", tempfile.gettempdir()],
                              capture_output=True, text=True, timeout=30,
                              env={"PATH": os.environ.get("PATH", ""), "OLIVARES_TOKEN": "demo-token-for-the-test"})
        self.assertEqual(proc.returncode, 1)
        self.assertIn("did not answer", proc.stderr)
        self.assertNotIn("Traceback", proc.stderr)


class LiveSnapshotCommand(unittest.TestCase):
    def test_snapshot_passes_bearer_through_environment_only(self) -> None:
        script = (ROOT / "scripts" / "test-demo-dataset-live.sh").read_text(encoding="utf8")
        snapshot = script.split("snapshot() {", 1)[1].split("\nPY\n}\n", 1)[0] + "\nPY\n"
        # Observe the interpreter boundary without starting an engine or sending a credential.
        # The synthetic marker is deliberately safe to expose on the RED version's argv.
        probe = """python3() {
          printf '%s\\n' "${OLIVARES_TOKEN:-}"
          printf '%s\\n' "$@"
          cat >/dev/null
        }
        TOKEN=synthetic-snapshot-bearer
        PORT=12345
        TENANT=snapshot-tenant
        snapshot() { """ + snapshot + """
        }
        snapshot
        """
        result = subprocess.run(["bash", "-eu", "-c", probe], capture_output=True,
                                text=True, timeout=10, env={"PATH": os.environ.get("PATH", "")})
        self.assertEqual(result.returncode, 0, result.stderr)
        received = result.stdout.splitlines()
        self.assertEqual(received[1:], ["-I", "-", "12345", "snapshot-tenant"],
                         "only the port and tenant may reach interpreter argv")
        self.assertEqual(received[0], "synthetic-snapshot-bearer", "bearer missing from environment")


if __name__ == "__main__":
    unittest.main(verbosity=2)
