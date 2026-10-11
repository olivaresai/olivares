# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: Apache-2.0

import os
import json
import sys
import threading
import unittest
from http.client import IncompleteRead
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "src"))

from olivares_client import APIError, Client  # noqa: E402


class RedirectAndBodyTests(unittest.TestCase):
    def peer(self, handler):
        server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
        thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01})
        thread.start()

        def stop():
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)

        self.addCleanup(stop)
        return f"http://127.0.0.1:{server.server_port}"

    def test_truncated_body_is_a_transport_failure_not_success(self):
        for status in (200, 429, 503):
            with self.subTest(status=status):
                calls = []

                class Truncated(BaseHTTPRequestHandler):
                    def log_message(self, *args):
                        pass

                    def do_GET(self):
                        calls.append(self.path)
                        body = b'{"error":{"code":"busy","message":"try later"}}'
                        self.send_response(status)
                        self.send_header("Content-Length", str(len(body) + 1))
                        self.send_header("Connection", "close")
                        self.end_headers()
                        self.wfile.write(body)
                        self.close_connection = True

                client = Client(self.peer(Truncated), "synthetic-client-token", timeout=2,
                                _sleep=lambda _: None)
                with self.assertRaises(IncompleteRead):
                    client.get_metrics()
                self.assertEqual(calls, ["/metrics"])

    def test_authenticated_redirects_stay_refused_on_both_origins(self):
        seen = []
        state = {}

        class Peer(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_POST(self):
                body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
                seen.append((self.path, self.command, self.headers, body))
                if self.path == "/v1/auth/login":
                    self.send_response(state["status"])
                    self.send_header("Location", state["target"] + "/destination")
                    self.send_header("Content-Length", "0")
                    self.end_headers()
                else:
                    body = b'{"redirected":true}'
                    self.send_response(200)
                    self.send_header("Content-Length", str(len(body)))
                    self.end_headers()
                    self.wfile.write(body)

            do_GET = do_POST

        target = self.peer(Peer)
        origin = self.peer(Peer)
        client = Client(origin, "synthetic-client-token", tenant="synthetic-tenant", timeout=2)
        for status in (301, 302, 303, 307, 308):
            for cross_origin in (False, True):
                with self.subTest(status=status, cross_origin=cross_origin):
                    state.update(status=status, target=target if cross_origin else origin)
                    seen.clear()
                    with self.assertRaises(APIError) as cm:
                        client.post_v1_auth_login({"sentinel": "synthetic-body"})
                    self.assertEqual(cm.exception.status, status)
                    self.assertEqual(cm.exception.code, f"http_{status}")
                    self.assertEqual(len(seen), 1)
                    path, method, headers, body = seen[0]
                    self.assertEqual((path, method), ("/v1/auth/login", "POST"))
                    self.assertEqual(headers["Authorization"], "Bearer synthetic-client-token")
                    self.assertEqual(headers["X-Olivares-Tenant"], "synthetic-tenant")
                    self.assertEqual(json.loads(body), {"sentinel": "synthetic-body"})

    def test_complete_and_unframed_bodies_keep_their_bytes(self):
        state = {}

        class Complete(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                body = b"metric 1\n"
                self.send_response(200)
                if state["framed"]:
                    self.send_header("Content-Length", str(len(body)))
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.write(body)
                self.close_connection = True

        client = Client(self.peer(Complete), timeout=2)
        for framed in (False, True):
            with self.subTest(framed=framed):
                state["framed"] = framed
                self.assertEqual(client.get_metrics(), b"metric 1\n")

    def test_existing_body_limit_is_preserved(self):
        limit = 64 << 20

        class Large(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                self.send_response(200)
                self.send_header("Content-Length", str(limit + 1))
                self.send_header("Connection", "close")
                self.end_headers()
                for _ in range(limit // 65536):
                    self.wfile.write(b"x" * 65536)
                self.close_connection = True

        raw = Client(self.peer(Large), timeout=5).get_metrics()
        self.assertEqual(len(raw), limit)
        self.assertEqual(raw[:16], b"x" * 16)
