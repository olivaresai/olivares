#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Catch missing optional readers before native smoke/browser credential setup."""

from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import threading
import unittest


class SmokeModulePrerequisites(unittest.TestCase):
    root = Path(__file__).resolve().parent.parent

    def test_shared_readers_are_selected_before_each_dependent_journey(self):
        catalog = json.loads((self.root / "core/modulespec/modules.json").read_text())
        required = {"inventory", "observability", "security"}
        # These are the client families reached by the demo route tables and the
        # full docs capture view table, plus each launcher's connector seeding.
        # The published module catalog owns dependency closure; a selected model
        # reader also runs finops, governance and source-scope.
        browser_readers = {
            "web-e2e-demo.sh": {
                "accessmap", "catalog", "compliance", "evals", "finops", "health",
                "inventory", "knowledge", "models", "notify", "observability",
                "orchestration", "recording", "redteam", "reporting", "security", "sessions",
            },
            "docs-captures.sh": {
                "accessmap", "adoption", "catalog", "claude-agents", "compliance", "deploy",
                "evals", "finops", "gitpublish", "health", "inferenceproxy", "inventory",
                "knowledge", "models", "notify", "observability", "orchestration", "posture",
                "recording", "redteam", "reporting", "sandbox", "security", "sessions", "skills", "voice",
            },
        }
        by_name = {m["namespace"]: m for m in catalog}
        self.assertTrue(set.union(*browser_readers.values()).issubset(by_name),
                        "every published reader must be registered in the module catalog")

        def running_modules(request):
            running = {m["namespace"] for m in catalog if m["standard"]}
            running.update(set(request.split()) & by_name.keys())
            while True:
                requires = set().union(*(set(by_name[name].get("requires", [])) for name in running))
                if requires.issubset(running):
                    return running
                running.update(requires)
        callers = (
            ("quickstart-smoke.sh", "check_findings\n", 0),
            ("quickstart-smoke.sh", "# PATH B —", 1),
            ("web-e2e-demo.sh", "# Only authenticated 200", 0),
            ("docs-captures.sh", "# Seed the three guide connectors", 0),
        )
        for name, boundary, journey in callers:
            with self.subTest(script=name, journey=journey):
                source = (self.root / "scripts" / name).read_text()
                if journey:
                    source = source[source.index(boundary):]
                    boundary = 'kill "$PID"'
                before = source[:source.index(boundary)]
                requests = re.findall(r"^enable_console_modules[^\n]+", before, re.M)
                # Path B already selects these readers through the registered API.
                # Its existing inline flow is valid before sharing that same flow.
                inline_selection = ('-X PUT "$BASE/v1/console/modules"' in before
                                    and "api_get '/v1/console/modules'" in before
                                    and all('"' + name + '"' in before for name in required))
                needed = browser_readers.get(name, required)
                self.assertTrue(inline_selection or any(needed.issubset(running_modules(line)) for line in requests),
                                "shared readers are queried before their optional modules are selected")

    def exercise_helper(self, *, poll_responses=(), put_status=200, names=None, prelude="", conditional=False):
        """Labelled local HTTP fixture: transport/selection proof, never product proof."""
        selected = ["sessions", "finops"]
        required = ["inventory", "observability", "security", "accessmap"]
        route = "/v1" + re.search(r'v1.Route\("([^\"]+)", func\(r chi.Router\) \{\n\s*coreRoute\(r, "GET", "/", s.systemRoute\("system:admin"\), s.handleModuleSelection\)',
                                  (self.root / "core/api/server.go").read_text())[1]
        observed = {"gets": 0, "updates": [], "authorization": []}

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def reply(self, status, body):
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(body).encode())

            def authorized(self):
                valid = (self.path == route and self.headers.get("Authorization") == "Bearer fixture-header-only"
                         and self.headers.get("X-Olivares-Tenant") == "fixture-tenant")
                observed["authorization"].append(valid)
                return valid

            def do_GET(self):
                if not self.authorized():
                    self.reply(401, {})
                    return
                observed["gets"] += 1
                poll = observed["gets"] - 2
                if observed["updates"] and poll < len(poll_responses):
                    status, body = poll_responses[poll]
                    self.reply(status, body)
                    return
                self.reply(200, {"modules": [
                    {"name": name, "selected": name in selected,
                     "running": bool(observed["updates"]) or name in selected}
                    for name in selected + required
                ]})

            def do_PUT(self):
                if not self.authorized():
                    self.reply(401, {})
                    return
                observed["updates"].append(json.loads(self.rfile.read(int(self.headers["Content-Length"]))))
                self.reply(put_status, {})

        with tempfile.TemporaryDirectory(prefix="smoke-module-fixture-") as directory:
            scratch = Path(directory)
            payloads = scratch / "payloads"
            payloads.mkdir()
            record = scratch / "curl-argv.jsonl"
            shim = scratch / "curl"
            shim.write_text("#!/usr/bin/env python3\nimport json,os,sys\n"
                            "with open(os.environ['MODULE_FIXTURE_ARGV'], 'a') as f:\n"
                            "    f.write(json.dumps(sys.argv[1:])+'\\n')\n"
                            "os.execv(os.environ['MODULE_FIXTURE_CURL'], [os.environ['MODULE_FIXTURE_CURL'], *sys.argv[1:]])\n")
            shim.chmod(0o700)
            environment = dict(os.environ, PATH=str(scratch) + os.pathsep + os.environ["PATH"],
                               TMPDIR=str(payloads), MODULE_FIXTURE_ARGV=str(record),
                               MODULE_FIXTURE_CURL=shutil.which("curl"))
            server = HTTPServer(("127.0.0.1", 0), Handler)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                command = ('. "$1"; unset PID; TOKEN=fixture-header-only; TENANT=fixture-tenant; ' + prelude
                           + 'enable_console_modules "$2" ' + ' '.join(names or required))
                if conditional:
                    command += ' || exit 97'
                result = subprocess.run(["bash", "-euo", "pipefail", "-c", command, "fixture",
                                         str(self.root / "scripts/lib/select-console-modules.sh"),
                                         "http://127.0.0.1:" + str(server.server_port)],
                                        env=environment, capture_output=True, text=True, timeout=20)
                argv = [json.loads(line) for line in record.read_text().splitlines()]
                self.assertTrue(argv, "fixture must exercise the actual curl boundary")
                self.assertTrue(all("fixture-header-only" not in argument for args in argv for argument in args),
                                "bearer entered curl argv")
                self.assertTrue(all(observed["authorization"]), "authenticated registered API must be used")
                self.assertEqual(list(payloads.iterdir()), [], "owned request bodies must be cleaned up")
                return result, observed
            finally:
                server.shutdown()
                server.server_close()
                thread.join()

    def test_selection_preserves_existing_modules_and_waits_through_restart(self):
        result, observed = self.exercise_helper(poll_responses=[(503, {}), (200, {"modules": []})])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(observed["updates"], [{"selected": ["sessions", "finops", "inventory", "observability", "security", "accessmap"]}])
        self.assertEqual(observed["gets"], 4, "wait for running readers after temporary restart responses")

    def test_permanent_update_and_readiness_failures_are_immediate(self):
        for status, polls, message in (
            (403, (), "module selection update returned HTTP 403"),
            (200, [(401, {})], "module readiness returned HTTP 401"),
            (200, [(403, {})], "module readiness returned HTTP 403"),
            (200, [(200, {"unexpected": []})], "invalid module readiness response"),
        ):
            with self.subTest(update_status=status, message=message):
                result, observed = self.exercise_helper(put_status=status, poll_responses=polls)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(message, result.stderr)
                self.assertEqual(observed["gets"], 1 if status == 403 else 2)

    def test_selection_generation_failure_survives_a_conditional_caller(self):
        result, observed = self.exercise_helper(names=["not-in-catalog"], conditional=True)
        self.assertEqual(result.returncode, 97)
        self.assertIn("invalid module selection or unavailable requested module", result.stderr)
        self.assertEqual(observed["updates"], [], "failed generation must never submit an empty selection")

    def test_dead_owned_engine_stops_readiness_polling(self):
        result, observed = self.exercise_helper(prelude='sleep 0 & PID=$!; wait "$PID"; ')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("owned engine exited", result.stderr)
        self.assertEqual(observed["gets"], 1)

    def test_elapsed_deadline_bounds_restart_polling(self):
        result, observed = self.exercise_helper(poll_responses=[(503, {})],
                                                prelude='sleep() { SECONDS=$((SECONDS + 121)); }; ')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("never became running after restart", result.stderr)
        self.assertEqual(observed["gets"], 2)


if __name__ == "__main__":
    unittest.main(verbosity=2)
