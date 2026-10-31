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

import datetime
import http.server
import importlib.util
import io
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request
from pathlib import Path
from unittest import mock

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
RESERVED_DOMAINS = {"olivares.local", "acme.example", "northwind.example", "example.com", "example.org"}
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
REDACCION = load("redaccion_under_test", "lib/redaccion.py")

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

# The canonical Northwind estate, as the dataset must seed it. These tables are the
# specification: the seeder's constants and conversations.json must agree with them, not the reverse.
CANONICAL_PEOPLE = (
    ("Ana Ruiz", "ana.ruiz@olivares.local", "admin"),
    ("Lena Fischer", "lena.fischer@olivares.local", "editor"),
    ("Marc Vidal", "marc.vidal@olivares.local", "editor"),
    ("Sam Okafor", "sam.okafor@olivares.local", "editor"),
    ("Priya Nair", "priya.nair@olivares.local", "editor"),
    ("Tomas Gil", "tomas.gil@olivares.local", "editor"),
)
# key: (driver, account name, account label, launch-ready). The name is the engine's slug; the label
# is the sign-in the Northwind estate names, shared by the profile and its account because the engine stores
# one label for both. Gemini is installed and not signed in: no account, no launch.
CANONICAL_PROFILES = {
    "claude-lena": ("claude", "lena-claude", "Lena's Claude account", True),
    "claude-sam": ("claude", "sam-claude", "Sam's Claude account", True),
    "claude-tomas": ("claude", "tomas-claude", "Tomas's Claude account", True),
    "claude-ana": ("claude", "ana-claude", "Ana's Claude account", True),
    "codex-marc": ("codex", "marc-chatgpt", "Marc's ChatGPT account", True),
    "codex-team": ("codex", "openai-team", "OpenAI team key", True),
    "opencode-team": ("opencode", "anthropic-team", "Anthropic team key", True),
    "gemini-docs": ("gemini-cli", None, None, False),
}
# name: (tool, profile, folder, owner, ending, run cost in micro-USD). ACP reports no cost, so the
# OpenCode session is the one unpriced session and the explicit $6.84 total is carried by the rest.
CANONICAL_SESSIONS = {
    "Refactor cart totals in checkout":
        ("claude", "claude-lena", "storefront-web", "Lena Fischer", "hold", 1_420_000),
    "Reconcile invoice export with billing ledger":
        ("codex", "codex-marc", "payments-api", "Marc Vidal", "linger", 880_000),
    "Upgrade Terraform AWS provider to 6.x":
        ("claude", "claude-sam", "infra", "Sam Okafor", "hold", 610_000),
    "Write docs for the export API":
        ("opencode", "opencode-team", "docs-site", "Priya Nair", "hold", None),
    "Fix flaky checkout e2e test":
        ("claude", "claude-tomas", "storefront-web", "Tomas Gil", "linger", 730_000),
    "Nightly dependency audit":
        ("codex", "codex-team", "infra", "Sam Okafor", "hold", 120_000),
    "Profile slow analytics query":
        ("claude", "claude-ana", "data-pipelines", "Ana Ruiz", "hold", 90_000),
    "Review the refund handler":
        ("claude", "claude-lena", "payments-api", "Lena Fischer", "exit", 420_000),
    "Plan per-client rate limiting":
        ("claude", "claude-ana", "payments-api", "Ana Ruiz", "exit", 310_000),
    "Draft the changelog entry":
        ("claude", "claude-ana", "docs-site", "Ana Ruiz", "exit", 280_000),
    "Check the export against the billing ledger":
        ("claude", "claude-ana", "data-pipelines", "Ana Ruiz", "exit", 190_000),
    "Review the analytics query plan":
        ("claude", "claude-tomas", "data-pipelines", "Tomas Gil", "exit", 350_000),
    "Document the retention settings":
        ("claude", "claude-lena", "docs-site", "Lena Fischer", "exit", 350_000),
    "Add idempotency keys to refunds":
        ("codex", "codex-marc", "payments-api", "Marc Vidal", "exit", 400_000),
    "Speed up the nightly orders export":
        ("codex", "codex-team", "data-pipelines", "Sam Okafor", "exit", 350_000),
    "Explain a slow query plan":
        ("codex", "codex-team", "data-pipelines", "Sam Okafor", "exit", 340_000),
}
# The three requests a person still has to answer, and the nine already answered (8 approved, 1 denied).
CANONICAL_PENDING = (
    ("db.migrate", "payments-staging", "Reconcile invoice export with billing ledger",
     "marc.vidal@olivares.local"),
    ("network.connect", "registry.terraform.io", "Upgrade Terraform AWS provider to 6.x",
     "sam.okafor@olivares.local"),
    ("github.write", "storefront-web#482", "Refactor cart totals in checkout",
     "lena.fischer@olivares.local"),
)
CANONICAL_DECIDED = (
    ("db.migrate", "staging-dev", "marc.vidal@olivares.local", "approve"),
    ("db.migrate", "staging-db", "sam.okafor@olivares.local", "approve"),
    ("network.connect", "pypi.org", "lena.fischer@olivares.local", "approve"),
    ("network.connect", "api.stripe.com", "marc.vidal@olivares.local", "approve"),
    ("github.write", "storefront-web#123", "lena.fischer@olivares.local", "approve"),
    ("github.write", "storefront-web#456", "tomas.gil@olivares.local", "reject"),
    ("network.connect", "registry.npmjs.org", "priya.nair@olivares.local", "approve"),
    ("deploy.promote", "payments-api/production", "marc.vidal@olivares.local", "approve"),
    ("evidence.export", "q3-access-review", "lena.fischer@olivares.local", "approve"),
)
CANONICAL_MCP = ("GitHub", "Postgres analytics (read-only)", "Sentry")
CANONICAL_COMMUNITY_ACTIONS = {"db.migrate", "network.connect", "github.write"}


def seeded():
    return [s for s in CONVERSATIONS if not s.get("fixture")]


def live_sessions():
    return [s for s in seeded() if s.get("ending") in ("hold", "linger")]


def finished_sessions():
    return [s for s in seeded() if s.get("ending", "exit") == "exit"]


def first_finished(tool: str) -> dict:
    return next(s for s in seeded() if s["tool"] == tool and s.get("ending", "exit") == "exit")


def profile_table() -> dict:
    return {p["key"]: p for p in SEEDER.PROFILES}


def people_table() -> dict:
    return {display: (email, role) for display, email, role in SEEDER.PEOPLE}


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
        profiles = {key: spec["driver"] for key, spec in profile_table().items()}
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

    def test_each_session_declares_how_it_ends(self):
        for s in CONVERSATIONS:
            with self.subTest(session=s["name"]):
                turns, ending = s["turns"], s.get("ending", "exit")
                self.assertIn(ending, ("exit", "hold", "linger"))
                finals = [bool(t.get("final")) for t in turns]
                holds = [bool(t.get("hold")) for t in turns]
                if ending == "exit":
                    self.assertEqual(finals, [False] * (len(turns) - 1) + [True])
                    self.assertFalse(any(holds))
                elif ending == "hold":
                    self.assertFalse(any(finals), "a held session must not end")
                    self.assertEqual(holds, [False] * (len(turns) - 1) + [True])
                    self.assertEqual(turns[-1].get("usage") or {}, {}, "a held turn cannot credit usage")
                else:
                    self.assertFalse(any(finals), "a lingering session must stay open")
                    self.assertFalse(any(holds))
                    self.assertEqual(turns[-1].get("usage") or {}, {}, "a linger turn must not grow usage")

    def test_every_tool_is_present_and_no_local_model_remains(self):
        self.assertEqual({s["tool"] for s in CONVERSATIONS}, {"claude", "codex", "gemini-cli", "opencode"})
        self.assertFalse(any(s.get("local") for s in CONVERSATIONS), "the canonical estate has no local model")
        self.assertTrue(any(s.get("fixture") and s["tool"] == "gemini-cli" for s in CONVERSATIONS),
                        "no transcript covers Gemini, which is installed and not signed in")
        self.assertTrue(any(s.get("fixture") and s["tool"] == "opencode" for s in CONVERSATIONS),
                        "no finished transcript covers OpenCode")

    def test_the_six_people_are_the_canonical_ones(self):
        self.assertEqual(SEEDER.PEOPLE, CANONICAL_PEOPLE)

    def test_the_profiles_are_the_canonical_ones(self):
        actual = {key: (p["driver"], p["account"], p["account_display"], p["signed_in"])
                  for key, p in profile_table().items()}
        self.assertEqual(actual, CANONICAL_PROFILES)

    def test_every_account_name_is_a_name_the_engine_accepts(self):
        # The engine's accountname.Validate admits lowercase ASCII only, a letter first, at most 32
        # characters. Adoption refuses anything else with HTTP 422 before display_name can be set.
        for p in SEEDER.PROFILES:
            if p["account"]:
                with self.subTest(profile=p["key"]):
                    self.assertRegex(p["account"], r"^[a-z][a-z0-9-]{0,31}$")

    def test_the_seeded_sessions_are_the_canonical_seven_live_and_nine_finished(self):
        actual = {s["name"]: s for s in seeded()}
        self.assertEqual(set(actual), set(CANONICAL_SESSIONS))
        profiles = profile_table()
        for name, (tool, profile, folder, owner, ending, cost) in CANONICAL_SESSIONS.items():
            s = actual[name]
            with self.subTest(session=name):
                self.assertEqual((s["tool"], s["profile"], s["folder"], s.get("ending", "exit")),
                                 (tool, profile, folder, ending))
                self.assertEqual(profiles[profile]["owner"], owner)
                self.assertEqual(s.get("cost_micro_usd"), cost)
        self.assertEqual(len(live_sessions()), 7)
        self.assertEqual(len(finished_sessions()), 9)

    def test_the_canonical_figures_add_up(self):
        costs = [s.get("cost_micro_usd") for s in seeded()]
        self.assertEqual(sum(c for c in costs if c is not None), 6_840_000)
        self.assertEqual(sum(c is None for c in costs), 1)
        by_tool = lambda tool: sum(s.get("cost_micro_usd") or 0 for s in seeded() if s["tool"] == tool)
        self.assertEqual(by_tool("claude"), 4_750_000)
        # Design: Codex $1.62 + OpenCode $0.47. ACP reports no usage or cost, so the $0.47 cannot be
        # credited to OpenCode; the explicit $6.84 total stays exact and one session is reported unpriced.
        self.assertEqual(by_tool("codex"), 2_090_000)
        self.assertEqual(by_tool("opencode"), 0)
        self.assertEqual(by_tool("gemini-cli"), 0)

    def test_the_usage_the_stand_ins_emit_produces_each_canonical_cost(self):
        for s in seeded():
            with self.subTest(session=s["name"]):
                cost = s.get("cost_micro_usd")
                if s["tool"] == "claude":
                    turn_cost = sum((t.get("usage") or {}).get("cost_usd", 0.0) for t in s["turns"])
                    self.assertEqual(round(turn_cost * 1_000_000), cost)
                elif s["tool"] == "codex":
                    usage = [t.get("usage") or {} for t in s["turns"]]
                    tokens_in = sum(u.get("input", 0) for u in usage)
                    cached = sum(u.get("cache_read", 0) for u in usage)
                    out = sum(u.get("output", 0) for u in usage)
                    # genai-prices: gpt-5-codex $1.25/MTok input, $0.125/MTok cached, $10/MTok output.
                    self.assertEqual(round((tokens_in - cached) * 1.25 + cached * 0.125 + out * 10), cost)
                else:
                    self.assertIsNone(cost, "ACP carries no usage, so it cannot carry a cost")
                    self.assertTrue(all(not (t.get("usage") or {}) for t in s["turns"]))

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
        before("providers", "profiles")  # a managed profile binds a provider record
        before("people", "profiles")  # a person adopts the account and launches its sessions
        before("profiles", "sessions")
        before("workspaces", "sessions")
        before("templates", "sessions")
        before("sessions", "approvals")  # the canonical queue is seeded after the runs it describes
        before("people", "approvals")
        before("people", "communications")

    def test_the_session_id_contract_holds_across_the_three_files(self):
        # The stand-in reads the variable, the seeder lets it through, the harness sets it for the engine.
        self.assertIn('"DEMO_SESSION_UNIQUE"', (ROOT / "scripts" / "seed-demo-dataset.py").read_text(encoding="utf8"))
        self.assertIn("DEMO_SESSION_UNIQUE", (TOOLS / "demo-tool.py").read_text(encoding="utf8"))
        self.assertIn("DEMO_SESSION_UNIQUE=1", (ROOT / "scripts" / "docs-captures.sh").read_text(encoding="utf8"))

    def test_the_capture_harness_lets_the_dataset_own_the_canonical_estate(self):
        # The capture harness seeds the canonical dataset before the surfaces that would otherwise add
        # people, approvals or runs, and the work seeder is told not to seed those again. The local model
        # is gone from the estate, so the harness must not start it or pass its URL.
        text = (ROOT / "scripts" / "docs-captures.sh").read_text(encoding="utf8")
        dataset = text.index("seed-demo-dataset.py")
        self.assertLess(dataset, text.index("seed-demo-work.py"))
        self.assertLess(dataset, text.index("seed-estate-volume.py"))
        work = text.index("seed-demo-work.py")
        self.assertIn("--canonical-estate", text[work:work + 300])
        self.assertNotIn("demo-ollama", text)
        self.assertNotIn("--ollama-url", text)

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
        table = profile_table()
        self.assertEqual(len(table), len(SEEDER.PROFILES))
        accounts = [p["account"] for p in table.values() if p["account"]]
        self.assertEqual(len(accounts), len(set(accounts)), "account names repeat")
        self.assertEqual(len({t[0] for t in SEEDER.TEMPLATES}), len(SEEDER.TEMPLATES))
        # A template that lists tools only runs under dontAsk: the engine refuses the rest.
        for name, _, body in SEEDER.TEMPLATES:
            if body["policies"].get("allowed_tools"):
                self.assertEqual(body["settings"]["permission_mode"], "dontAsk", name)
            self.assertNotIn("dlp_mode", body["policies"], "a native launch cannot enforce a DLP mode: " + name)
        # The skills pack is read from a folder of the seeded repository.
        self.assertTrue(any(p.startswith(".agents/skills/") for p in SEEDER.REPOS["payments-api"]))
        # Every canonical folder is writable and carries no DLP label: the estate asks people about
        # migrations, network destinations and GitHub writes, never about a launch.
        for name, (_, mode, dlp) in SEEDER.WORKSPACES.items():
            self.assertEqual(mode, "rw", name)
            self.assertEqual(dlp, "off", name)
        self.assertEqual({kind for kind, _ in SEEDER.PROVIDERS}, {"anthropic", "openai"})
        self.assertEqual(tuple(name for name, _ in SEEDER.MCP_SERVERS), CANONICAL_MCP)

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
                step(Recorder({}), {"root": tempfile.gettempdir()})
            except Exception:  # the recording answers are empty on purpose; only what is written matters here
                pass
        self.assertTrue(written, "no step wrote a policy: the recording did not reach step_policies")
        self.assertEqual({kind for _, kind in written}, {"approval"}, "a step wrote a policy that is not an approval")

    def test_the_approval_tables_are_the_canonical_ones(self):
        pending = {(a, s): (session, who) for a, _, s, _, session, who, _ in SEEDER.PENDING_APPROVALS}
        decided = {(a, s): (who, d) for a, _, s, _, who, d in SEEDER.DECIDED_APPROVALS}
        self.assertEqual(set(pending), {(a, s) for a, s, _, _ in CANONICAL_PENDING})
        self.assertEqual(set(decided), {(a, s) for a, s, _, _ in CANONICAL_DECIDED})
        self.assertEqual(len(set(pending) | set(decided)), len(pending) + len(decided),
                         "a request is both pending and decided, or repeats its (action, subject)")
        names = set(CANONICAL_SESSIONS)
        for action, _, _, _, session, who, required in SEEDER.PENDING_APPROVALS:
            self.assertIn(session, names)
            self.assertEqual(required, 1)
            self.assertNotEqual(action, "sessions.run.launch")
            self.assertIn(who, {p[1] for p in SEEDER.PEOPLE})
        approved = sum(1 for *_, decision in SEEDER.DECIDED_APPROVALS if decision == "approve")
        denied = sum(1 for *_, decision in SEEDER.DECIDED_APPROVALS if decision == "reject")
        self.assertEqual((approved, denied), (8, 1))
        self.assertEqual(sum(1 for a, *_ in SEEDER.PENDING_APPROVALS
                             if a in CANONICAL_COMMUNITY_ACTIONS), 3)
        self.assertEqual(sum(1 for a, *_ in SEEDER.DECIDED_APPROVALS
                             if a in CANONICAL_COMMUNITY_ACTIONS), 7)


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


class TransportBoundary(unittest.TestCase):
    def reset_openers(self):
        # Both openers read the environment when they are built, and one test pollutes it.
        REDACCION._ABRIDOR = None
        REDACCION._ABRIDOR_CON_PROXY = None

    def test_an_inherited_proxy_never_receives_the_operator_token(self):
        # Loopback traffic needs no proxy, and an inherited http_proxy turns every seed into a request
        # that hands the operator bearer token to whatever host the variable names.
        seen = {"proxy": [], "origin": []}

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                seen[self.server.role].append(self.headers.get("Authorization"))
                body = b'{"ok":true}'
                self.send_response(200)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        servers = {}
        for role in ("proxy", "origin"):
            server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
            server.role = role
            servers[role] = server
            threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            with mock.patch.dict(os.environ):
                for name in ("no_proxy", "NO_PROXY"):
                    os.environ.pop(name, None)
                proxy = "http://127.0.0.1:%d" % servers["proxy"].server_port
                os.environ["http_proxy"] = os.environ["HTTP_PROXY"] = proxy
                self.reset_openers()
                self.addCleanup(self.reset_openers)
                request = urllib.request.Request(
                    "http://127.0.0.1:%d/v1/x" % servers["origin"].server_port)
                request.add_header("Authorization", "Bearer synthetic-marker")
                body = REDACCION.abre(request, timeout=10).read()
        finally:
            for server in servers.values():
                server.shutdown()
                server.server_close()
        self.assertEqual(body, b'{"ok":true}')
        self.assertEqual(seen["origin"], ["Bearer synthetic-marker"])
        self.assertEqual(seen["proxy"], [], "the operator token crossed the proxy boundary")

    def test_a_remote_engine_keeps_the_mandatory_egress_proxy(self):
        # Dropping the proxy for every destination would make a remote engine behind a mandatory egress
        # proxy unreachable. Only loopback skips it.
        seen = []

        class Proxy(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                seen.append((self.path, self.headers.get("Authorization")))
                body = b'{"ok":true}'
                self.send_response(200)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        server = http.server.HTTPServer(("127.0.0.1", 0), Proxy)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            with mock.patch.dict(os.environ):
                for name in ("no_proxy", "NO_PROXY"):
                    os.environ.pop(name, None)
                os.environ["http_proxy"] = os.environ["HTTP_PROXY"] = (
                    "http://127.0.0.1:%d" % server.server_port)
                self.reset_openers()
                self.addCleanup(self.reset_openers)
                request = urllib.request.Request("http://engine.example/v1/x")
                request.add_header("Authorization", "Bearer synthetic-marker")
                body = REDACCION.abre(request, timeout=10).read()
        finally:
            server.shutdown()
            server.server_close()
        self.assertEqual(body, b'{"ok":true}')
        self.assertEqual(seen, [("http://engine.example/v1/x", "Bearer synthetic-marker")])

    def test_only_loopback_hosts_skip_the_proxy(self):
        for url in ("http://127.0.0.1:8080/x", "http://127.9.9.9/x", "http://localhost:8080/x",
                    "http://[::1]:8080/x", "https://LOCALHOST/x"):
            self.assertTrue(REDACCION._es_loopback(url), url)
        for url in ("http://engine.example/x", "http://10.0.0.5/x", "http://127.0.0.1.evil.example/x",
                    "http://localhost.evil.example/x"):
            self.assertFalse(REDACCION._es_loopback(url), url)


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
        s = first_finished("claude")
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

    def test_a_thread_confirms_the_provider_it_was_bound_to(self):
        # The engine refuses to send input unless the app-server confirms the provider the session
        # is bound to: `olivares_record` for a managed-injection profile. A hardcoded "openai"
        # answer fails every such session with HTTP 409.
        _, frames = run_tool("codex", ["app-server"], [
            {"id": 1, "method": "thread/start", "params": {"modelProvider": "olivares_record"}}])
        result = frames[0]["result"]
        self.assertEqual(result["modelProvider"], "olivares_record")
        self.assertEqual(result["thread"]["modelProvider"], "olivares_record")

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
        s = next(x for x in CONVERSATIONS if x["tool"] == tool and x.get("ending", "exit") == "exit")
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
        s = next(x for x in CONVERSATIONS if x["tool"] == "opencode" and x.get("ending", "exit") == "exit")
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
        self.assertIn("Anthropic api", proc.stdout)
        self.assertNotIn("OpenAI oauth", proc.stdout, "the canonical OpenCode profile holds no OpenAI login")
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


class EventApi(StubApi):
    """The engine's attach stream, from a list: `events` is the one seam the answer proof reads."""

    def __init__(self, events, routes=None):
        super().__init__(routes or {})
        self.queued, self.event_paths = events, []

    def events(self, path, timeout):
        self.event_paths.append(path)
        yield from self.queued


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
        for action, _, subject, _, _, _, _ in SEEDER.PENDING_APPROVALS:
            existing.append({"id": "id-" + subject, "action": action, "subject_ref": subject, "status": "pending"})
        for action, _, subject, _, _, decision in SEEDER.DECIDED_APPROVALS:
            existing.append({"id": "id-" + subject, "action": action, "subject_ref": subject,
                             "status": statuses(decision)})
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
        wanted = sorted(d for *_, d in SEEDER.DECIDED_APPROVALS)
        self.assertEqual(sorted(decided), wanted, "a request left pending by an interrupted run was not decided")
        self.assertTrue(all(c[2] == "reviewer-token" for c in api.calls if c[1].endswith("/decisions")),
                        "a decision was made with the token that created the request")
        self.assertIn(reviewer_email, [p[1] for p in SEEDER.PEOPLE])

    def test_a_request_that_was_decided_is_not_decided_again(self):
        api, decided = self.approvals_api(lambda decision: "approved" if decision == "approve" else "rejected")
        SEEDER.step_approvals(api, {})
        self.assertEqual(decided, [], "a decided request received a second decision")

    def test_every_canonical_request_names_its_action_subject_session_and_asker(self):
        created = []

        def made(body):
            created.append(body)
            return 201, {"id": "id-%d" % len(created)}

        api = StubApi({
            ("GET", "/v1/m/governance/approvals"): (200, {"items": [], "has_more": False}),
            ("POST", "/v1/m/governance/approvals"): made,
            ("POST", "/v1/auth/login"): lambda body: (200, {"token": "token-" + body["email"]}),
        })
        refs = {session: "sid-" + session
                for _, _, _, _, session, _, _ in SEEDER.PENDING_APPROVALS}
        SEEDER.step_approvals(api, {"edition": "community", "session_refs": refs})
        bodies = [b for b in created if "decision" not in b]
        want = {}
        for action, _, subject, _, session, who, _ in SEEDER.PENDING_APPROVALS:
            if SEEDER.keeps_action("community", action):
                want[(action, subject)] = ("pending", session, who)
        for action, _, subject, _, who, decision in SEEDER.DECIDED_APPROVALS:
            if SEEDER.keeps_action("community", action):
                want[(action, subject)] = ("approved" if decision == "approve" else "rejected", None, who)
        self.assertEqual({(b["action"], b["subject_ref"]) for b in bodies}, set(want),
                         "the queue does not hold exactly the canonical requests of this edition")
        for body in bodies:
            key = (body["action"], body["subject_ref"])
            state, session, _ = want[key]
            if state == "pending":
                self.assertEqual(body.get("session_ref"), refs[session],
                                 "%s %s does not name the session that asked" % key)
            for field in ("expires_in_seconds", "escalate_in_seconds"):
                self.assertNotIn(field, body,
                                 "%s %s carries an expiry, which would end a request the estate keeps "
                                 "waiting" % key)
        creations = [c for c in api.calls
                     if c[0] == "POST" and c[1] == "/v1/m/governance/approvals"]
        self.assertEqual(len(creations), len(bodies))
        for (_, _, token), body in zip(creations, bodies):
            self.assertEqual(token, "token-" + want[(body["action"], body["subject_ref"])][2],
                             "%s %s was created with somebody else's token"
                             % (body["action"], body["subject_ref"]))

    MIDNIGHT = datetime.datetime(2026, 10, 19, tzinfo=datetime.timezone.utc)

    def live_plane_api(self, runs, rows):
        return StubApi({
            ("GET", "/v1/m/sessions/runs"): (200, {"items": runs, "has_more": False}),
            ("GET", "/v1/m/sessions/live"): (200, {"items": rows, "has_more": False}),
        })

    def test_an_adopted_row_that_still_reads_as_live_is_refused(self):
        api = self.live_plane_api([{"run_ref": "run-1"}], [
            {"live_ref": "obs-1", "attribution": "observed", "cc_state": "active",
             "last_event_at": "2026-10-01T00:00:00Z"}])
        with self.assertRaisesRegex(SEEDER.SeedError, "obs-1 reads as active"):
            SEEDER.verify_live_plane(api, self.MIDNIGHT)

    def test_an_adopted_row_active_since_local_midnight_is_refused(self):
        api = self.live_plane_api([{"run_ref": "run-1"}], [
            {"live_ref": "obs-1", "attribution": "observed", "cc_state": "ended",
             "last_event_at": "2026-10-19T00:00:00Z"}])
        with self.assertRaisesRegex(SEEDER.SeedError, "at or after the local midnight"):
            SEEDER.verify_live_plane(api, self.MIDNIGHT)

    def test_an_adopted_row_older_than_local_midnight_is_accepted(self):
        api = self.live_plane_api([{"run_ref": "run-1"}], [
            {"live_ref": "obs-1", "attribution": "observed", "cc_state": "ended",
             "last_event_at": "2026-10-18T23:59:59Z"}])
        SEEDER.verify_live_plane(api, self.MIDNIGHT)

    def test_a_managed_row_outside_the_run_list_is_refused(self):
        api = self.live_plane_api([{"run_ref": "run-1"}], [
            {"live_ref": "stray", "attribution": "managed", "run_ref": "run-2",
             "cc_state": "idle", "last_event_at": "2026-10-01T00:00:00Z"}])
        with self.assertRaisesRegex(SEEDER.SeedError, "stray reads as idle"):
            SEEDER.verify_live_plane(api, self.MIDNIGHT)

    def test_an_adopted_row_with_an_unreadable_timestamp_is_refused(self):
        api = self.live_plane_api([{"run_ref": "run-1"}], [
            {"live_ref": "obs-1", "attribution": "observed", "cc_state": "ended",
             "last_event_at": ""}])
        with self.assertRaisesRegex(SEEDER.SeedError, "unreadable last_event_at"):
            SEEDER.verify_live_plane(api, self.MIDNIGHT)

    def test_no_policy_carries_an_expiry_or_an_escalation(self):
        for name, spec in SEEDER.APPROVAL_POLICIES:
            for field in ("expires_in_seconds", "escalate_in_seconds"):
                self.assertNotIn(field, spec,
                                 "policy %r carries %s; a matching policy overwrites both windows from "
                                 "the request and the pending queue empties" % (name, field))

    def test_the_disposable_tenant_guard_runs_before_anything_is_written(self):
        names = [name for name, _ in SEEDER.STEPS]
        self.assertEqual(names[0], "edition")
        self.assertEqual(names[1], "estate",
                         "a step could write to a real tenant before the demo login proves it disposable")
        self.assertEqual(names[-1], "verify-approvals")
        self.assertEqual(names.count("verify-approvals"), 1)
        self.assertEqual(len(names), len(set(names)), "a step runs twice")

    def test_the_profiles_table_names_the_sign_in_the_engine_must_store(self):
        self.assertEqual(sum(1 for p in SEEDER.PROFILES if p["signed_in"]), 7)
        providers = {kind for kind, _ in SEEDER.PROVIDERS}
        for p in SEEDER.PROFILES:
            self.assertIn(p["auth_source"], ("provider_account_home", "managed_injection"), p["key"])
            if p["auth_source"] == "managed_injection":
                self.assertIn(p["provider"], providers, p["key"])
            else:
                self.assertNotIn("provider", p, p["key"])
            if p["account"]:
                # A display-name PATCH writes the profile's own column, so a profile and its adopted
                # account share one label; two labels could never both read back.
                self.assertEqual(p["display"], p["account_display"],
                                 "%s: the profile and its account must carry the same label" % p["key"])
        installed = next(p for p in SEEDER.PROFILES if not p["signed_in"])
        self.assertFalse(installed["account"], "an installed profile must not claim an account")

    def test_a_work_root_component_that_links_outside_is_refused(self):
        with tempfile.TemporaryDirectory() as base:
            root, outside = os.path.join(base, "root"), os.path.join(base, "outside")
            os.makedirs(root)
            os.makedirs(outside)
            os.symlink(outside, os.path.join(root, "repos"))
            with self.assertRaisesRegex(SEEDER.SeedError, "outside the work root"):
                SEEDER.owned_path(root, "repos", "storefront-web")
            os.makedirs(os.path.join(root, "homes"))
            os.symlink(outside, os.path.join(root, "homes", "lena"))
            with self.assertRaisesRegex(SEEDER.SeedError, "symbolic link"):
                SEEDER.owned_path(root, "homes", "lena")

    def test_the_attach_proof_reads_the_whole_run_for_the_answer_the_child_writes(self):
        answer = "The component now renders the total returned with the order."
        frame = {"type": "assistant", "message": {"content": [{"type": "text", "text": answer}]}}
        api = EventApi([{"seq": 3, "stream": "stdout", "line": json.dumps(frame)}])
        SEEDER.attach_marker(api, "run-1", answer, limit=1)
        self.assertEqual(api.event_paths, ["/v1/m/sessions/runs/run-1/attach?from=0"],
                         "the proof read from a cursor instead of replaying the run the input joined")

    def test_a_turn_whose_answer_never_arrives_is_an_error(self):
        api = EventApi([{"seq": 1, "stream": "stdout", "line": json.dumps({"type": "system"})}],
                       {("GET", "/v1/m/sessions/runs/"): (200, {"state": "idle"})})
        with self.assertRaisesRegex(SEEDER.SeedError, "never consumed.*state idle"):
            SEEDER.attach_marker(api, "run-1", "the recorded answer", limit=0.01)

    def test_a_stream_the_engine_refuses_is_not_read_as_a_quiet_ring(self):
        class RefusingEventApi(StubApi):
            def events(self, path, timeout):
                raise urllib.error.URLError(ConnectionRefusedError("connection refused"))

        api = RefusingEventApi({("GET", "/v1/m/sessions/runs/"): (200, {"state": "running"})})
        with self.assertRaisesRegex(SEEDER.SeedError, "did not accept the output stream.*state running"):
            SEEDER.attach_marker(api, "run-1", "the recorded answer", limit=1)

    def test_a_run_read_that_fails_reports_its_http_status_and_reason(self):
        api = StubApi({("GET", "/v1/m/sessions/runs/"):
                       (503, {"error": "database is locked"})})
        detail = SEEDER.run_detail(api, "run-1")
        self.assertIn("HTTP 503", detail)
        self.assertIn("database is locked", detail)

    def test_every_turn_ends_with_an_answer_no_earlier_turn_wrote(self):
        turns = 0
        for s in SEEDER.dataset_sessions():
            written = []
            for turn in s["turns"]:
                turns += 1
                self.assertTrue(SEEDER.turn_marker(turn, written),
                                "session %r: a turn has no answer of its own" % s["name"])
                written.extend(SEEDER.turn_texts(turn))
        self.assertEqual(turns, 32, "the canonical estate has thirty-two recorded turns")

    def test_a_turn_the_ring_cannot_attribute_is_refused(self):
        with self.assertRaisesRegex(SEEDER.SeedError, "cannot tell them apart"):
            SEEDER.turn_marker({"prompt": "again", "steps": [{"say": "the same answer"}]},
                               ["the same answer"])

    def test_the_next_prompt_waits_until_the_answered_turn_has_ended(self):
        api = StubApi({("GET", "/v1/m/sessions/runs/"): (200, {"state": "idle"})})
        self.assertEqual(SEEDER.settle(api, "run-1", "session", final_turn=False)["state"], "idle")
        with self.assertRaisesRegex(SEEDER.SeedError, "still 'running'"):
            SEEDER.settle(StubApi({("GET", "/v1/m/sessions/runs/"): (200, {"state": "running"})}),
                          "run-1", "session", final_turn=False, limit=0.01)
        with self.assertRaisesRegex(SEEDER.SeedError, "not its last"):
            SEEDER.settle(StubApi({("GET", "/v1/m/sessions/runs/"): (200, {"state": "stopped"})}),
                          "run-1", "session", final_turn=False)
        self.assertEqual(SEEDER.settle(
            StubApi({("GET", "/v1/m/sessions/runs/"): (200, {"state": "stopped"})}),
            "run-1", "session", final_turn=True)["state"], "stopped")

    def test_the_dataset_token_never_travels_in_argv(self):
        proc = subprocess.run([sys.executable, "-I", str(ROOT / "scripts" / "seed-demo-dataset.py"),
                               "http://127.0.0.1:9", "positional-secret-marker", "tenant",
                               tempfile.gettempdir()],
                              capture_output=True, text=True, timeout=30,
                              env={"PATH": os.environ.get("PATH", "")})
        self.assertEqual(proc.returncode, 2)
        self.assertIn("OLIVARES_TOKEN", proc.stderr)
        self.assertNotIn("positional-secret-marker", proc.stdout + proc.stderr,
                         "the refused token was echoed")

    def business_actions_written(self, edition):
        """The approval actions the policies and approval requests of one pass hold, for an estate of `edition`."""
        created, policies = [], []

        def made(body):
            created.append(body)
            return 201, {"id": "id-%d" % len(created)}

        def made_policy(body):
            policies.append(dict(body))
            return 201, {"id": "policy-%d" % len(policies)}

        api = StubApi({
            ("GET", "/v1/m/governance/approvals"): (200, {"items": [], "has_more": False}),
            ("GET", "/v1/m/governance/policies"):
                lambda _: (200, {"items": list(policies), "has_more": False}),
            ("POST", "/v1/m/governance/approvals"): made,
            ("POST", "/v1/m/governance/policies"): made_policy,
            ("POST", "/v1/auth/login"): (200, {"token": "reviewer-token"}),
        })
        ctx = {"edition": edition,
               "session_refs": {session: "sid-" + session
                                for _, _, _, _, session, _, _ in SEEDER.PENDING_APPROVALS}}
        SEEDER.step_policies(api, ctx)
        SEEDER.step_approvals(api, ctx)
        return ({b["action"] for b in created if "decision" not in b}
                | {p["spec"]["match"]["action"] for p in policies})

    def test_a_community_estate_holds_no_policy_or_approval_for_a_business_action(self):
        business = {"deploy.promote", "evidence.export"}
        community = self.business_actions_written("community")
        self.assertTrue(community, "a Community estate holds no approval at all")
        self.assertEqual(community & business, set(), "a Community estate holds a Business approval kind")
        self.assertTrue(business <= self.business_actions_written("enterprise"),
                        "a Business estate lost the approval kinds only it can act on")

    def test_the_queue_is_three_six_one_or_three_eight_one_by_edition(self):
        # Counted from the requests the seeder writes and the decisions it posts, with the expected
        # figures written here as literals: building the expectation with keeps_action would only
        # repeat the filter the test exists to check.
        business = {"deploy.promote", "evidence.export"}
        canonical = ({(a, s) for a, _, s, _, _, _, _ in SEEDER.PENDING_APPROVALS}
                     | {(a, s) for a, _, s, _, _, _ in SEEDER.DECIDED_APPROVALS})
        for edition, created_count, approved, rejected in (
                ("community", 10, 6, 1), ("business", 12, 8, 1), ("enterprise", 12, 8, 1)):
            created, decisions = [], []

            def made(body):
                created.append(body)
                return 201, {"id": "id-%d" % len(created)}

            api = StubApi({
                ("GET", "/v1/m/governance/approvals"): (200, {"items": [], "has_more": False}),
                # The decision prefix must precede the collection prefix: StubApi takes the first
                # route whose prefix matches, and the collection prefix also matches a decision.
                ("POST", "/v1/m/governance/approvals/"):
                    lambda body: (decisions.append(body["decision"]) or (200, {})),
                ("POST", "/v1/m/governance/approvals"): made,
                ("POST", "/v1/auth/login"): (200, {"token": "reviewer-token"}),
            })
            refs = {session: "sid-" + session
                    for _, _, _, _, session, _, _ in SEEDER.PENDING_APPROVALS}
            SEEDER.step_approvals(api, {"edition": edition, "session_refs": refs})
            requests = created
            want = canonical if edition != "community" else {
                key for key in canonical if key[0] not in business}
            self.assertEqual({(b["action"], b["subject_ref"]) for b in requests}, want, edition)
            self.assertEqual(len(requests), created_count, edition)
            self.assertEqual(sorted(decisions),
                             sorted(["approve"] * approved + ["reject"] * rejected), edition)

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

    def test_the_estate_is_refused_outside_the_engine_demo_tenant(self):
        # A loopback URL can still be a tunnel to a real installation. The demo login is minted only by
        # `serve --seed-demo`, and this seeder creates six accounts with a published password: its
        # presence in the roster is the engine's own statement that this tenant is disposable.
        foreign = StubApi({("GET", "/v1/members"): (
            200, {"items": [{"email": "owner@acme.example"}], "has_more": False})})
        with self.assertRaisesRegex(SEEDER.SeedError, "seed-demo"):
            SEEDER.step_demo_estate(foreign, {})
        demo = StubApi({("GET", "/v1/members"): (
            200, {"items": [{"email": SEEDER.DEMO_EMAIL}], "has_more": False})})
        SEEDER.step_demo_estate(demo, {})

    def test_a_canonical_request_left_in_the_wrong_state_is_an_error(self):
        # The queue can exist and still be wrong: a pending request someone denied, or an approval where
        # the estate says a denial. The second pass used to print the counts and exit 0.
        wrong = []
        for action, _, subject, _, _, _, _ in SEEDER.PENDING_APPROVALS:
            wrong.append({"id": "id-" + subject, "action": action, "subject_ref": subject,
                          "status": "rejected"})
        for action, _, subject, _, _, decision in SEEDER.DECIDED_APPROVALS:
            wrong.append({"id": "id-" + subject, "action": action, "subject_ref": subject,
                          "status": "approved" if decision == "approve" else "rejected"})
        api = StubApi({("GET", "/v1/m/governance/approvals"): (
            200, {"items": wrong, "has_more": False})})
        with self.assertRaisesRegex(SEEDER.SeedError, "pending"):
            SEEDER.step_verify_approvals(api, {})

    def test_an_mcp_gateway_answer_without_servers_is_an_error(self):
        api = StubApi({("GET", "/v1/console/mcp-gateway"): (200, {"version": 1})})
        with self.assertRaisesRegex(SEEDER.SeedError, "servers"):
            SEEDER.step_mcp(api, {})

    def test_an_existing_budget_with_a_different_limit_is_an_error(self):
        name, limit, period, dimension, _ = SEEDER.BUDGETS[0]
        api = StubApi({("GET", "/v1/m/finops/budgets"): (
            200, {"items": [{"name": name, "limit_micro_usd": limit + 1, "period": period,
                             "dimension": dimension}], "has_more": False})})
        with self.assertRaisesRegex(SEEDER.SeedError, "budget"):
            SEEDER.step_budgets(api, {})

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

    def test_a_foreign_approval_on_a_canonical_action_is_refused(self):
        # A matching action is not enough: the (action, subject) pair is the estate's identity, so a
        # request for somebody else's subject must stop the seed instead of joining the queue.
        action, _, subject, _, _, _, _ = SEEDER.PENDING_APPROVALS[0]
        api = StubApi({("GET", "/v1/m/governance/approvals"): (200, {"items": [
            {"id": "a", "action": action, "subject_ref": subject + "-other", "status": "pending"}]})})
        with self.assertRaisesRegex(SEEDER.SeedError, "not part of the canonical estate"):
            SEEDER.step_approvals(api, {})

    def test_budgets_skip_only_a_build_that_does_not_serve_them(self):
        refusal = {"error": {"code": "module_error", "message": "FinOps is a Business feature: https://olivares.ai/pricing"}}
        served = StubApi({("GET", "/v1/m/finops/budgets"): (200, {"items": []}),
                          ("POST", "/v1/m/finops/budgets"): (501, refusal)})
        SEEDER.step_budgets(served, {"edition": "community"})  # reported, not raised
        for edition in ("business", "enterprise", None):
            # A paid edition serves FinOps: its 501 is a regression, and an unread edition guesses nothing.
            paid = StubApi({("GET", "/v1/m/finops/budgets"): (200, {"items": []}),
                            ("POST", "/v1/m/finops/budgets"): (501, refusal)})
            with self.subTest(edition=edition), self.assertRaisesRegex(SEEDER.SeedError, "501"):
                SEEDER.step_budgets(paid, {"edition": edition})
        off = StubApi({("GET", "/v1/m/finops/budgets"): (404, {"error": {"code": "module_not_enabled"}})})
        SEEDER.step_budgets(off, {})
        for status in (401, 403, 500, 503):
            broken = StubApi({("GET", "/v1/m/finops/budgets"): (status, {"error": {"code": "x"}})})
            with self.subTest(status=status), self.assertRaises(SEEDER.SeedError):
                SEEDER.step_budgets(broken, {})

    def test_a_session_that_exists_and_did_not_finish_is_an_error(self):
        name = first_finished("claude")["name"]
        api = StubApi({("GET", "/v1/m/sessions/runs"): (200, {"items": [{"name": name, "state": "running"}]})})
        with self.assertRaises(SEEDER.SeedError):
            SEEDER.step_sessions(api, {"workspaces": {}, "profiles": {}, "templates": {}})

    def test_a_live_row_that_holds_or_lingers_in_the_wrong_state_is_an_error(self):
        hold = next(s for s in seeded() if s.get("ending") == "hold")
        linger = next(s for s in seeded() if s.get("ending") == "linger")
        for s, wrong in ((hold, "idle"), (linger, "running")):
            api = StubApi({("GET", "/v1/m/sessions/runs"): (200, {"items": [
                {"name": s["name"], "run_ref": "run-1", "state": wrong}], "has_more": False})})
            with self.subTest(name=s["name"]), self.assertRaisesRegex(SEEDER.SeedError, "leaves it"):
                SEEDER.step_sessions(api, {"workspaces": {}, "profiles": {}, "templates": {}})

    def test_a_live_row_the_plane_did_not_launch_is_an_error(self):
        # The home merges every observed row with the runs, so a row another source wrote (serve
        # --seed-demo) is counted beside the canonical sixteen and moves $6.84.
        def api_with(live):
            runs = []
            for s in seeded():
                runs.append({"name": s["name"], "run_ref": "run-" + s["name"],
                             "cost_micro_usd": s.get("cost_micro_usd"),
                             "state": ("running" if s.get("ending") == "hold" else
                                       "idle" if s.get("ending") == "linger" else "stopped"),
                             "exit_code": 0,
                             "provider_profile_ref": "profile-" + s["profile"],
                             "workspace_ref": "ws-" + s["folder"],
                             "claude_session_id": "sid-" + s["name"]})
            return StubApi({
                ("GET", "/v1/m/sessions/runs"): (200, {"items": runs, "has_more": False}),
                ("GET", "/v1/m/sessions/live"): (200, {"items": live, "has_more": False}),
            })
        managed = [{"live_ref": "live-" + s["name"], "attribution": "managed",
                    "run_ref": "run-" + s["name"], "session_ref": "sid-" + s["name"],
                    "cc_state": "running" if s.get("ending") in ("hold", "linger") else "ended"}
                   for s in seeded()]
        ctx = {
            "workspaces": {name: "ws-" + folder for name, (folder, _, _) in SEEDER.WORKSPACES.items()},
            "profiles": {p["key"]: "profile-" + p["key"] for p in SEEDER.PROFILES},
            "templates": {},
        }
        SEEDER.step_sessions(api_with(managed), ctx)
        legacy = managed + [{"live_ref": "live-demo", "attribution": "legacy",
                             "session_ref": "sess-coder-7a3f", "cc_state": "active"}]
        with self.assertRaisesRegex(SEEDER.SeedError, "outside the canonical estate"):
            SEEDER.step_sessions(api_with(legacy), ctx)

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
        ROOT=/repo
        WORK=/work
        snapshot() { """ + snapshot + """
        }
        snapshot
        """
        result = subprocess.run(["bash", "-eu", "-c", probe], capture_output=True,
                                text=True, timeout=10, env={"PATH": os.environ.get("PATH", "")})
        self.assertEqual(result.returncode, 0, result.stderr)
        received = result.stdout.splitlines()
        self.assertEqual(received[1:], ["-I", "-", "12345", "snapshot-tenant",
                                        "/repo/scripts/demo-tools/conversations.json",
                                        "/work/dataset", "/repo/scripts/lib"],
                         "only the port, tenant, the canonical session table, the dataset root and the "
                         "shared redactor path may reach interpreter argv")
        self.assertEqual(received[0], "synthetic-snapshot-bearer", "bearer missing from environment")


class ErrorBodyClip(unittest.TestCase):
    TOKEN = "synthetic-bearer-" + "ABCDEFGHJKLMNPQRSTUVWXYZ" * 2

    def test_a_secret_the_clip_would_cut_is_redacted_before_it_is_cut(self):
        # The engine writes the error body. Cut at 300 characters first, a token that straddles the
        # limit keeps its first half and no longer equals the remembered secret.
        token = self.TOKEN

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                body = ("x" * 250 + token).encode()
                self.send_response(502)
                self.send_header("Content-Type", "text/plain")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            api = SEEDER.Api("http://127.0.0.1:%d" % server.server_port, "t", "tenant")
            SEEDER._REDACT.recuerda(token)
            with self.assertRaises(SEEDER.SeedError) as caught:
                api.ok("probe", "GET", "/v1/x")
        finally:
            server.shutdown()
            server.server_close()
        printed = SEEDER._REDACT(str(caught.exception))
        self.assertIn("HTTP 502", printed)
        self.assertNotIn(token[:20], printed, "half of a cut token reached the terminal")


    def test_every_dataset_error_site_redacts_before_it_cuts(self):
        token = self.TOKEN
        SEEDER._REDACT.recuerda(token)

        def body(limit):
            # `{"detail": "` is 12 characters; the token then starts 30 characters before the cut.
            return {"detail": "x" * (limit - 42) + token}

        budgets = "/v1/m/finops/budgets"
        sites = {
            "ok": lambda: StubApi({("POST", "/v1/x"): (500, body(300))}).ok("probe", "POST", "/v1/x"),
            "items": lambda: StubApi({("GET", "/v1/x"): (200, body(200))}).items("probe", "/v1/x"),
            "budgets read": lambda: SEEDER.step_budgets(
                StubApi({("GET", budgets): (500, body(300))}), {"edition": "community"}),
            "budget create": lambda: SEEDER.step_budgets(
                StubApi({("GET", budgets): (200, {"items": []}), ("POST", budgets): (500, body(300))}),
                {"edition": "community"}),
        }
        for name, run in sites.items():
            with self.subTest(site=name):
                with self.assertRaises(SEEDER.SeedError) as caught:
                    run()
                self.assertNotIn(token[:20], SEEDER._REDACT(str(caught.exception)),
                                 "half of a cut token reached the terminal")
        detail = SEEDER.run_detail(StubApi({("GET", "/v1/m/sessions/runs/"): (500, body(200))}), "ref")
        self.assertNotIn(token[:20], SEEDER._REDACT(detail), "run_detail printed half of a cut token")

    def refusing(self, body):
        def refuse(req, timeout):
            raise urllib.error.HTTPError(req.full_url, 502, "bad gateway", {}, io.BytesIO(body.encode()))
        return refuse

    def test_the_sibling_seeders_redact_an_error_body_before_cutting_it(self):
        # The adoption and volume seeders cut the engine's error body the same way before they printed it.
        token = self.TOKEN
        otlp = load("seed_adoption_otlp_under_test", "seed-adoption-otlp.py")
        otlp._RED.recuerda(token)
        with mock.patch.object(otlp, "abre", self.refusing("x" * 170 + token)):
            _, text = otlp.postear("http://127.0.0.1:1/v1/x", {})
        self.assertNotIn(token[:20], text, "half of a cut token left the OTLP seeder")
        volume = load("seed_estate_volume_under_test", "seed-estate-volume.py")
        motor = volume.Motor("http://127.0.0.1:1", "t", "tenant")
        volume.redacta.recuerda(token)
        with mock.patch.object(volume, "abre", self.refusing("x" * 270 + token)):
            _, body = motor._pide("/v1/x")
        self.assertNotIn(token[:20], volume.redacta(body), "half of a cut token left the volume seeder")
        motor._pide = lambda ruta, cuerpo=None, metodo="GET": (500, "x" * 90 + token)
        _, why = motor.listar("/v1/x", "items")
        self.assertNotIn(token[:20], volume.redacta(why), "half of a cut token reached a list refusal")
        verify = load("verify_seed_payloads_under_test", "verify-seed-payloads.py")
        verify.redacta.recuerda(token)
        for name, answer in (("a plain body", "x" * 130 + token),
                             ("an error message", json.dumps({"error": {"message": "x" * 170 + token}}))):
            with self.subTest(verifier=name):
                self.assertNotIn(token[:20], verify.redacta(verify.mensaje(answer)),
                                 "half of a cut token reached the verifier's message")


class LiveSnapshotEditionGate(unittest.TestCase):
    """The live test's snapshot program takes a FinOps 501 as the edition's answer only on Community."""

    def snapshot_lines(self, edition):
        script = (ROOT / "scripts" / "test-demo-dataset-live.sh").read_text(encoding="utf8")
        program = re.search(r"<<'PY'\n(.*?)\nPY\n", script, re.S).group(1)

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                path = self.path.split("?")[0]
                code, answer = 200, {"items": [], "servers": []}
                if path == "/v1/server-info":
                    answer = {"edition": edition}
                elif path == "/v1/m/finops/budgets":
                    code, answer = 501, {"error": "FinOps is a Business feature"}
                payload = json.dumps(answer).encode()
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def log_message(self, *args):
                pass

        server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            with tempfile.TemporaryDirectory() as tmp:
                proc = subprocess.run(
                    [sys.executable, "-I", "-c", program, str(server.server_port), "tenant",
                     str(ROOT / "scripts" / "demo-tools" / "conversations.json"), tmp,
                     str(ROOT / "scripts" / "lib")],
                    capture_output=True, text=True, timeout=60,
                    env={"PATH": os.environ.get("PATH", ""), "OLIVARES_TOKEN": "synthetic-token"})
        finally:
            server.shutdown()
            server.server_close()
        return [line for line in proc.stdout.splitlines() if line.startswith("budgets")]

    def test_a_community_engine_may_refuse_budgets(self):
        self.assertEqual([line.split()[1] for line in self.snapshot_lines("community")], ["n/a"])

    def test_a_paid_engine_refusing_budgets_is_a_finding(self):
        for edition in ("business", "enterprise"):
            with self.subTest(edition=edition):
                self.assertEqual(self.snapshot_lines(edition), ["budgets    ?"])


class EngineLogRedaction(unittest.TestCase):
    """The failure paths show engine logs; the first-boot token in them must not reach the terminal."""

    MARKER = "synthetic-first-boot-token-0123456789"

    def make_log(self, directory: str) -> Path:
        log = Path(directory) / "engine.log"
        log.write_text("engine banner\n"
                       "=== FIRST-BOOT SETUP ===\n"
                       "  Token: %s\n"
                       "  URL: http://127.0.0.1:8466/setup\n"
                       "later line repeats %s\n" % (self.MARKER, self.MARKER), encoding="utf8")
        return log

    def test_a_log_tail_replaces_the_first_boot_token(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            log = self.make_log(tmp)
            probe = '. "$1"\ntail -1 "$2" | redact_log "$2"\n'
            proc = subprocess.run(["bash", "-euo", "pipefail", "-c", probe, "_",
                                   str(ROOT / "scripts" / "lib" / "redact-log.sh"), str(log)],
                                  capture_output=True, text=True, timeout=10,
                                  env={"PATH": os.environ.get("PATH", "")})
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertNotIn(self.MARKER, proc.stdout, "the setup token survived the redactor")
            self.assertIn("[redacted]", proc.stdout)

    def run_redactor(self, log: Path, text: str) -> subprocess.CompletedProcess:
        probe = '. "$1"\nprintf "%s\\n" "$3" | redact_log "$2"\n'
        return subprocess.run(["bash", "-euo", "pipefail", "-c", probe, "_",
                               str(ROOT / "scripts" / "lib" / "redact-log.sh"), str(log), text],
                              capture_output=True, text=True, timeout=10,
                              env={"PATH": os.environ.get("PATH", "")})

    def test_a_drifted_banner_withholds_the_log_instead_of_printing_it(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            log = Path(tmp) / "engine.log"
            log.write_text("=== FIRST-BOOT SETUP ===\n"
                           "  token: %s\n" % self.MARKER, encoding="utf8")
            proc = self.run_redactor(log, "text that names %s" % self.MARKER)
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertNotIn(self.MARKER, proc.stdout)
            self.assertNotIn("text that names", proc.stdout,
                             "a banner the parser cannot read must withhold the text, not print it")
            self.assertIn("[log withheld", proc.stdout)

    def test_a_token_line_outside_a_recognized_banner_withholds_the_log(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            log = Path(tmp) / "engine.log"
            log.write_text("2026-10-19T10:00:00Z === FIRST-BOOT SETUP ===\n"
                           "  Token: %s\n" % self.MARKER, encoding="utf8")
            proc = self.run_redactor(log, "text that names %s" % self.MARKER)
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertNotIn(self.MARKER, proc.stdout, "a prefixed banner let the setup token print")
            self.assertIn("[log withheld", proc.stdout)

    def test_every_banner_of_a_restarted_log_is_redacted(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            log = Path(tmp) / "engine.log"
            second = "synthetic-second-boot-token-9876543210"
            log.write_text("=== FIRST-BOOT SETUP ===\n  Token: %s\n"
                           "engine restarted before setup finished\n"
                           "=== FIRST-BOOT SETUP ===\n  Token:    %s\n" % (self.MARKER, second),
                           encoding="utf8")
            proc = self.run_redactor(log, "first %s second %s" % (self.MARKER, second))
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertNotIn(self.MARKER, proc.stdout, "the first banner's token survived")
            self.assertNotIn(second, proc.stdout, "the second banner's token survived")

    def test_an_unreadable_log_withholds_the_text(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            proc = self.run_redactor(Path(tmp) / "missing.log", "text with " + self.MARKER)
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertNotIn(self.MARKER, proc.stdout)
            self.assertNotIn("text with", proc.stdout)
            self.assertIn("[log withheld", proc.stdout)

    def test_the_token_shape_is_masked_without_a_banner(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            log = Path(tmp) / "engine.log"
            log.write_text("engine restarted; the banner was rotated away\n", encoding="utf8")
            shaped = "olst_ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
            proc = self.run_redactor(log, "leaked %s here" % shaped)
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertNotIn(shaped, proc.stdout, "a shaped token survived without a banner")
            self.assertIn("[redacted]", proc.stdout)

    def test_the_redactor_receives_the_log_path_not_the_token(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            log = self.make_log(tmp)
            # Observe the interpreter boundary: only the log path may reach argv.
            probe = """python3() {
              printf '%s\\n' "$@"
              cat >/dev/null
            }
            . "$1"
            printf 'text\\n' | redact_log "$2"
            """
            proc = subprocess.run(["bash", "-euo", "pipefail", "-c", probe, "_",
                                   str(ROOT / "scripts" / "lib" / "redact-log.sh"), str(log)],
                                  capture_output=True, text=True, timeout=10,
                                  env={"PATH": os.environ.get("PATH", "")})
            self.assertEqual(proc.returncode, 0, proc.stderr)
            argv = proc.stdout.splitlines()
            self.assertNotIn(self.MARKER, argv)
            self.assertEqual(argv[-1], str(log), "the redactor reads the token from the log by path")


if __name__ == "__main__":
    unittest.main(verbosity=2)
