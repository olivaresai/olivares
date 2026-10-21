#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise the cost and policy seeders over HTTP, including edition and reread failures."""

import contextlib
import importlib.util
import io
import json
import os
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


class CostSeed(unittest.TestCase):
    def run_seed(self, edition="business", info_status=200, summary_status=200,
                 fraction=1, post_status=202, info_body=None):
        spec = importlib.util.spec_from_file_location(
            "seed_cost", Path(__file__).with_name("seed-demo-work.py"))
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        requests = []
        total = 0

        class API(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def reply(self, status, body):
                requests.append((self.command, self.path))
                self.send_response(status)
                self.end_headers()
                self.wfile.write(json.dumps(body).encode())

            def do_GET(self):
                if self.path == "/v1/server-info":
                    self.reply(info_status, info_body if info_body is not None else {"edition": edition})
                elif self.path == "/v1/m/finops/spend/summary":
                    status = 501 if edition == "community" else summary_status
                    self.reply(status, {"total_micro_usd": int(total * fraction)})
                else:
                    self.reply(404, {})

            def do_POST(self):
                nonlocal total
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                if self.path == "/v1/m/finops/cost":
                    total += body["cost_micro_usd"]
                    self.reply(post_status, {})
                else:
                    self.reply(404, {})

        with ThreadingHTTPServer(("127.0.0.1", 0), API) as server:
            thread = threading.Thread(target=server.serve_forever)
            thread.start()
            output = io.StringIO()
            try:
                with contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
                    rc = module.sembrar_coste(
                        "http://127.0.0.1:%d" % server.server_port,
                        "synthetic", "test-tenant", dias=1)
            finally:
                server.shutdown()
                thread.join()
        return rc, requests, output.getvalue()

    def test_community_skips_before_any_cost_request(self):
        rc, requests, output = self.run_seed(edition="community")
        self.assertEqual(rc, 0, output)
        self.assertEqual(requests, [("GET", "/v1/server-info")])
        self.assertIn("SKIPPED", output)

    def test_paid_editions_ingest_and_reread(self):
        for edition in ("business", "enterprise"):
            with self.subTest(edition=edition):
                rc, requests, output = self.run_seed(edition=edition)
                self.assertEqual(rc, 0, output)
                self.assertEqual(requests, [("GET", "/v1/server-info")]
                                 + [("POST", "/v1/m/finops/cost")] * 3
                                 + [("GET", "/v1/m/finops/spend/summary")])
                self.assertIn("REREAD", output)

    def test_paid_reread_refusal_is_an_error(self):
        for status in (403, 501, 503):
            with self.subTest(status=status):
                rc, _, output = self.run_seed(summary_status=status)
                self.assertEqual(rc, 1, output)
                self.assertIn("could not reread", output)

    def test_paid_missing_spend_is_an_error(self):
        for fraction in (0, 0.49):
            with self.subTest(fraction=fraction):
                rc, _, output = self.run_seed(fraction=fraction)
                self.assertEqual(rc, 1)
                self.assertIn("ingestion did not complete", output)

    def test_ingestion_refusal_is_an_error(self):
        rc, requests, output = self.run_seed(post_status=503)
        self.assertEqual(rc, 1, output)
        self.assertNotIn(("GET", "/v1/m/finops/spend/summary"), requests)

    def test_unknown_or_missing_edition_refuses_before_ingestion(self):
        for edition in ("unknown", None):
            with self.subTest(edition=edition):
                rc, requests, output = self.run_seed(edition=edition)
                self.assertEqual(rc, 1, output)
                self.assertEqual(requests, [("GET", "/v1/server-info")])

    def test_unreadable_edition_refuses_before_ingestion(self):
        rc, requests, output = self.run_seed(info_status=503)
        self.assertEqual(rc, 1, output)
        self.assertEqual(requests, [("GET", "/v1/server-info")])

    def test_malformed_edition_refuses_before_ingestion(self):
        rc, requests, output = self.run_seed(info_body=[])
        self.assertEqual(rc, 1, output)
        self.assertEqual(requests, [("GET", "/v1/server-info")])


class PolicySeed(unittest.TestCase):
    BUSINESS_REFUSAL = {"error": {"message": "Fleet-wide managed settings require Business."}}
    PUBLISH = "/v1/m/claude-policy/managed-settings/publish"
    CHECKIN = "/v1/m/claude-policy/managed-settings/checkin"

    def run_seed(self, edition="business", publish_status=200, publish_body=None,
                 checkin_status=200, checkin_body=None, findings=None):
        spec = importlib.util.spec_from_file_location(
            "seed_policy", Path(__file__).with_name("seed-demo-work.py"))
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        requests = []
        published = {"revision": 3, "artifact": {"artifact_sha256": "ab" * 32,
                                                  "key_fingerprint": "fp-1"}}

        class API(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def reply(self, status, body):
                requests.append((self.command, self.path))
                self.send_response(status)
                self.end_headers()
                self.wfile.write(json.dumps(body).encode())

            def do_GET(self):
                if self.path == "/v1/server-info":
                    self.reply(200, {"edition": edition})
                elif self.path.startswith("/v1/m/security/findings"):
                    self.reply(200, {"items": [{"id": "f1"}] if findings is None else findings})
                else:
                    self.reply(404, {})

            def do_POST(self):
                self.rfile.read(int(self.headers["Content-Length"]))
                if self.path == PolicySeed.PUBLISH:
                    self.reply(publish_status, published if publish_body is None else publish_body)
                elif self.path == PolicySeed.CHECKIN:
                    self.reply(checkin_status,
                               {"drift": [{"kind": "policy_drift"}]} if checkin_body is None
                               else checkin_body)
                else:
                    self.reply(404, {})

        with ThreadingHTTPServer(("127.0.0.1", 0), API) as server:
            thread = threading.Thread(target=server.serve_forever)
            thread.start()
            output = io.StringIO()
            try:
                with contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
                    rc = module.sembrar_politica(
                        "http://127.0.0.1:%d" % server.server_port, "synthetic", "test-tenant")
            finally:
                server.shutdown()
                thread.join()
        return rc, requests, output.getvalue()

    def test_community_skips_after_the_publish_refusal_that_names_business(self):
        rc, requests, output = self.run_seed(edition="community", publish_status=501,
                                             publish_body=self.BUSINESS_REFUSAL)
        self.assertEqual(rc, 0, output)
        self.assertEqual(requests, [("POST", self.PUBLISH), ("GET", "/v1/server-info")])
        self.assertIn("policy seed SKIPPED", output)

    def test_other_refusals_stay_errors(self):
        for edition, status, body in (
                ("community", 501, {"error": {"message": "module not wired"}}),
                ("community", 501, {"error": {"message": "requires business"}}),
                ("community", 500, self.BUSINESS_REFUSAL),
                ("community", 403, self.BUSINESS_REFUSAL),
                # A paid edition has the routes: its 501 naming Business is a real failure.
                ("business", 501, self.BUSINESS_REFUSAL),
                ("enterprise", 501, self.BUSINESS_REFUSAL)):
            with self.subTest(edition=edition, status=status, body=body):
                rc, requests, output = self.run_seed(edition=edition, publish_status=status,
                                                     publish_body=body)
                self.assertEqual(rc, 1, output)
                self.assertNotIn(("POST", self.CHECKIN), requests)
                self.assertIn("ERROR: publish policy", output)

    def test_business_publishes_checks_in_and_rereads(self):
        rc, requests, output = self.run_seed()
        self.assertEqual(rc, 0, output)
        self.assertEqual(requests, [("POST", self.PUBLISH)]
                         + [("POST", self.CHECKIN)] * 3
                         + [("GET", "/v1/m/security/findings?kind=policy_drift&limit=100")])
        self.assertIn("REREAD", output)

    def test_checkin_refusal_is_an_error_even_when_it_names_business(self):
        # Publish succeeded, so the routes exist: a later 501 is never an edition answer.
        for edition in ("community", "business"):
            with self.subTest(edition=edition):
                rc, requests, output = self.run_seed(
                    edition=edition, checkin_status=501, checkin_body=self.BUSINESS_REFUSAL)
                self.assertEqual(rc, 1, output)
                self.assertIn("ERROR: checkin", output)
                self.assertEqual(requests.count(("POST", self.CHECKIN)), 1)


class CaptureManifest(unittest.TestCase):
    def test_configured_held_views_survive_executed_skip_evidence(self):
        source = Path(__file__).with_name("docs-captures.sh").read_text()
        merger = source.split('DOCS_DIR="$ROOT/web/playwright-report/docs" python3 - <<\'PY\' || true\n', 1)[1].split('\nPY\n', 1)[0]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "orchestration.evidence.json").write_text(json.dumps({
                "id": "orchestration", "skipped": "Business capability on a Community build",
            }))
            result = subprocess.run([sys.executable, "-c", merger], env={
                **os.environ, "DOCS_DIR": directory,
                "DOCS_CAPTURE_SKIP_IDS": "voice|sandbox",
            }, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            manifest = json.loads((root / "manifest.json").read_text())
            self.assertEqual(set(manifest["skipped"]), {"voice", "sandbox", "orchestration"})
            self.assertEqual(manifest["skipped"]["orchestration"],
                             "Business capability on a Community build")
            self.assertEqual((manifest["views"], manifest["captures"]), (0, 0))

    def test_only_executed_skips_are_named_and_not_counted_as_captures(self):
        source = Path(__file__).with_name("docs-captures.sh").read_text()
        merger = source.split('DOCS_DIR="$ROOT/web/playwright-report/docs" python3 - <<\'PY\' || true\n', 1)[1].split('\nPY\n', 1)[0]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for theme in ("light", "dark"):
                (root / (theme + ".evidence.json")).write_text(json.dumps({
                    "id": "access-map", "theme": theme,
                }))
            (root / "orchestration.evidence.json").write_text(json.dumps({
                "id": "orchestration", "skipped": "Business capability on a Community build",
            }))
            result = subprocess.run([sys.executable, "-c", merger],
                                    env={**os.environ, "DOCS_DIR": directory},
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            manifest = json.loads((root / "manifest.json").read_text())
            self.assertEqual(manifest["skipped"], {
                "orchestration": "Business capability on a Community build",
            })
            self.assertEqual((manifest["views"], manifest["captures"]), (1, 2))
            self.assertEqual(manifest["unpaired"], [])
            self.assertIn("SKIPPED orchestration", result.stdout)


if __name__ == "__main__":
    unittest.main(verbosity=2)
