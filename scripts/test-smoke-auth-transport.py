#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Execute smoke-script auth requests at a local curl boundary, without networking."""

import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest


class SmokeAuthTransport(unittest.TestCase):
    scripts = {"quickstart-smoke.sh": 4, "web-e2e-demo.sh": 1, "docs-captures.sh": 1}
    request = re.compile(
        r"(?:printf[^\n]*\|[ \t]*)?curl -sf -X POST "
        r'"[^"\n]+/v1/(?:auth/login|setup)"[ \t]*(?:\\\n[ \t]*)?'
        r"-H 'Content-Type: application/json' \\\n"
        r'''[ \t]*(?:-d (?:"(?:\\.|[^"\\])*"|'[^']*')|--data-binary @-)'''
    )

    def observed(self):
        root = Path(__file__).resolve().parent
        with tempfile.TemporaryDirectory(prefix="smoke-auth-transport-") as directory:
            scratch = Path(directory)
            curl = scratch / "curl"
            curl.write_text(
                "#!/usr/bin/env python3\n"
                "import json,os,sys\n"
                "from pathlib import Path\n"
                "Path(os.environ['SMOKE_REQUEST_RECORD']).write_text("
                "json.dumps({'argv':sys.argv[1:],'stdin':sys.stdin.read()}))\n"
            )
            curl.chmod(0o700)
            record = scratch / "request.json"
            environment = dict(os.environ, PATH=str(scratch) + os.pathsep + os.environ["PATH"],
                               BASE="http://smoke.invalid", PORT="18972",
                               SETUP_TOKEN="synthetic-single-use-setup-marker",
                               SMOKE_REQUEST_RECORD=str(record))
            for name, count in self.scripts.items():
                requests = list(self.request.finditer((root / name).read_text()))
                self.assertEqual(len(requests), count, name + ": every auth request must be exercised")
                for index, match in enumerate(requests):
                    subprocess.run(["bash", "-euo", "pipefail", "-c", match.group()], env=environment,
                                   input="", capture_output=True, text=True, check=True)
                    yield name, index, match.group(), json.loads(record.read_text())

    def test_auth_material_never_enters_curl_arguments(self):
        for name, index, _request, record in self.observed():
            with self.subTest(script=name, request=index):
                self.assertFalse(any('"password"' in argument or '"token"' in argument
                                     or "synthetic-single-use-setup-marker" in argument
                                     for argument in record["argv"]),
                                 "authentication material entered curl arguments")
                self.assertIn("--data-binary", record["argv"])
                self.assertIn("@-", record["argv"])

    def test_stdin_preserves_the_original_setup_and_login_payloads(self):
        for name, index, request, record in self.observed():
            with self.subTest(script=name, request=index):
                self.assertTrue(record["stdin"], "auth JSON must reach curl on stdin")
                payload = json.loads(record["stdin"])
                if "/v1/setup" in request:
                    expected = {"token": "synthetic-single-use-setup-marker", "email": "admin@local",
                                "password": "correct-horse-battery-staple"}
                elif "admin@local" in request:
                    expected = {"email": "admin@local", "password": "correct-horse-battery-staple"}
                else:
                    expected = {"email": "demo@olivares.local", "password": "olivares-demo-estate"}
                self.assertEqual(payload, expected)


if __name__ == "__main__":
    unittest.main(verbosity=2)
