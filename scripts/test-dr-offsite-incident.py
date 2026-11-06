#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise the workflow's incident step with real curl and a local API."""
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer

import yaml

ROOT = Path(__file__).resolve().parents[1]
TITLE = "dr-offsite-nightly: qualification failed"


class IncidentTest(unittest.TestCase):
    def test_incident_without_gh(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/dr-offsite-nightly.yml").read_text())
        step = next(s for s in workflow["jobs"]["offsite"]["steps"]
                    if s.get("name") == "open or update qualification incident")
        token = secrets.token_hex(16)
        requests = []
        existing = []
        denied = False

        class API(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                self.assert_auth()
                self.send_response(403 if denied else 200)
                self.end_headers()
                self.wfile.write(json.dumps({"items": existing}).encode())

            def assert_auth(self):
                if self.headers.get("Authorization") != f"Bearer {token}":
                    raise AssertionError("missing API authorization")
                requests.append((self.command, self.path, None))

            def do_POST(self):
                self.assert_auth()
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                requests[-1] = (self.command, self.path, body)
                self.send_response(201)
                self.end_headers()
                self.wfile.write(b"{}")

        server = HTTPServer(("127.0.0.1", 0), API)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with tempfile.TemporaryDirectory() as tmp:
                bin_dir = Path(tmp) / "bin"
                bin_dir.mkdir()
                # Only real runner tools; gh is deliberately absent.
                for tool in ("bash", "curl", "python3", "rm", "head"):
                    (bin_dir / tool).symlink_to(shutil.which(tool))
                env = dict(os.environ, PATH=str(bin_dir), GH_TOKEN=token,
                           GH_REPO="example/project", GITHUB_REPOSITORY="example/project",
                           GITHUB_API_URL=f"http://127.0.0.1:{server.server_port}",
                           RUNNER_TEMP=tmp, RUN_URL="https://example.invalid/run/1",
                           SOURCE_SHA="a" * 40)
                for number in (None, 42):
                    with self.subTest(existing=number):
                        requests.clear()
                        existing[:] = ([{"title": TITLE, "number": number}] if number else [])
                        result = subprocess.run(["bash", "-c", step["run"]], env=env,
                                                capture_output=True, text=True, timeout=15)
                        self.assertEqual(result.returncode, 0, result.stderr)
                        posts = [r for r in requests if r[0] == "POST"]
                        self.assertEqual(len(posts), 1)
                        _, path, payload = posts[0]
                        self.assertEqual(path, "/repos/example/project/issues" +
                                         ("/42/comments" if number else ""))
                        if not number:
                            self.assertEqual(payload["title"], TITLE)
                        self.assertIn(env["RUN_URL"], payload["body"])
                        self.assertIn(env["SOURCE_SHA"], payload["body"])
                        self.assertNotIn(token, result.stdout + result.stderr)
                        self.assertFalse((Path(tmp) / "offsite-api-auth").exists())
                denied = True
                requests.clear()
                result = subprocess.run(["bash", "-c", step["run"]], env=env,
                                        capture_output=True, text=True, timeout=15)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(any(r[0] == "POST" for r in requests))
                self.assertFalse((Path(tmp) / "offsite-api-auth").exists())
        finally:
            server.shutdown()
            thread.join()
            server.server_close()


if __name__ == "__main__":
    unittest.main()
