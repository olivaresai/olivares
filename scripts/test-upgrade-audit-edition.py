#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""An edition refusal must never hide changed signed data or a Business failure."""
import importlib.util
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import threading
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("upgrade_journey", Path(__file__).with_name("upgrade-journey.py"))
journey = importlib.util.module_from_spec(spec)
spec.loader.exec_module(journey)

ANCHOR = {"seq": 7, "hash": "saved-hash", "prev_hash": "saved-parent", "sig": "saved-signature"}

class Api:
    def __init__(self, edition="community", status=501, event=None, verdict=None):
        self.edition, self.status = edition, status
        self.event = dict(ANCHOR) if event is None else event
        self.verdict = verdict or {"error": {"code": "audit_export_unavailable"}}

    def call(self, method, path, body=None, want=(200, 201, 202, 204), with_status=False):
        if path.startswith("/v1/audit?"):
            return {"items": [self.event]}
        if path == "/v1/server-info":
            return {"edition": self.edition}
        if path != "/v1/audit/verify":
            raise AssertionError(path)
        if self.status not in want:
            raise journey.Fail(f"HTTP {self.status}")
        return (self.status, self.verdict) if with_status else self.verdict

class AuditUpgrade(unittest.TestCase):
    def check(self, api):
        journey.check_audit_data(api, {"audit_anchor": ANCHOR}, "after upgrade")

    def test_http_client_preserves_status_and_existing_body_only_calls(self):
        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                refused = self.path == "/refused"
                self.send_response(501 if refused else 200)
                self.end_headers()
                self.wfile.write(b'{"error":{"code":"audit_export_unavailable"}}' if refused else b'{"ok":true}')

            def log_message(self, *_args):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            api = journey.Api(f"http://127.0.0.1:{server.server_port}")
            self.assertEqual(api.call("GET", "/ready"), {"ok": True})
            self.assertEqual(api.call("GET", "/ready", with_status=True), (200, {"ok": True}))
            self.assertEqual(api.call("GET", "/refused", want=(501,), with_status=True),
                             (501, {"error": {"code": "audit_export_unavailable"}}))
            with self.assertRaises(journey.Fail):
                api.call("GET", "/refused")
        finally:
            server.shutdown()
            server.server_close()
            thread.join()

    def test_community_keeps_signed_data_when_verification_moves(self):
        self.check(Api())

    def test_previous_community_and_business_still_require_successful_verification(self):
        for edition in ("community", "enterprise"):
            self.check(Api(edition, 200, verdict={"ok": True}))
            with self.assertRaises(journey.Fail):
                self.check(Api(edition, 200, verdict={"ok": False}))

    def test_business_must_not_accept_the_community_seam(self):
        with self.assertRaises(journey.Fail):
            self.check(Api("enterprise"))

    def test_http_200_error_body_is_not_an_edition_refusal(self):
        with self.assertRaises(journey.Fail):
            self.check(Api(status=200))

    def test_no_edition_refusal_hides_changed_data(self):
        for field in ANCHOR:
            with self.subTest(field=field), self.assertRaises(journey.Fail):
                self.check(Api(event={**ANCHOR, field: "changed"}))

    def test_unrelated_errors_are_not_an_edition_refusal(self):
        for status in (403, 500, 503):
            with self.subTest(status=status), self.assertRaises(journey.Fail):
                self.check(Api(status=status))
        with self.assertRaises(journey.Fail):
            self.check(Api(verdict={"error": {"code": "unrelated"}}))

if __name__ == "__main__":
    unittest.main()
