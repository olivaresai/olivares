# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: Apache-2.0
"""Smoke tests for the Python client against a local fake control plane.

Run from clients/python:  python3 -m unittest discover -s tests -v
(`task sdk:test:python` from the repo root.)
"""

import json
import os
import sys
import threading
import unittest
import warnings
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "src"))

from olivares_client import APIError, API_VERSION, Client, DeprecationNotice  # noqa: E402


class FakeControlPlane(BaseHTTPRequestHandler):
    """Routes the smoke tests need; records every request for assertions."""

    requests = []  # (method, path, headers) — reset per test via .clear()
    request_bodies = []
    rate_calls = 0

    def log_message(self, *args):  # keep test output quiet
        pass

    def _json(self, status, payload, headers=()):
        body = json.dumps(payload).encode()
        self.send_response(status)
        for k, v in headers:
            self.send_header(k, v)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _handle(self):
        length = int(self.headers.get("Content-Length", "0"))
        FakeControlPlane.request_bodies.append(
            self.rfile.read(length) if length else b""
        )
        FakeControlPlane.requests.append(
            (self.command, self.path, dict(self.headers))
        )
        url = urlparse(self.path)
        q = parse_qs(url.query)
        if url.path == "/v1/agents" and self.command == "GET":
            if q.get("cursor") == ["c1"]:
                self._json(200, {"items": [{"id": "c"}], "has_more": False})
            else:
                self._json(200, {"items": [{"id": "a"}, {"id": "b"}],
                                 "cursor": "c1", "has_more": True})
        elif url.path.startswith("/v1/agents/"):
            self._json(404, {"error": {"code": "not_found", "message": "no such agent"}},
                       headers=[("X-Request-ID", "req-42")])
        elif url.path == "/v1/server-info":
            self._json(200, {"version": "test"}, headers=[
                ("Deprecation", "@1780272000"),
                ("Sunset", "Thu, 01 Jun 2028 00:00:00 GMT"),
                ("Link", '<https://docs.olivares.invalid/how-to/migrate-example/>; rel="deprecation"'),
            ])
        elif url.path == "/v1/tokens" and self.command == "POST":
            FakeControlPlane.rate_calls += 1
            if FakeControlPlane.rate_calls == 1:
                self._json(429, {"error": {"code": "rate_limited", "message": "slow down"}},
                           headers=[("Retry-After", "3")])
            else:
                self._json(201, {"ok": True})
        elif url.path.startswith("/v1/tokens/") and self.command == "DELETE":
            # Deprecated parameterized route: one warning per ENDPOINT expected.
            self._json(200, {}, headers=[("Deprecation", "@1780272000")])
        elif url.path == "/metrics":
            body = b"# HELP olivares_requests_total Requests.\nolivares_requests_total 42\n"
            self.send_response(200)
            self.send_header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        elif url.path == "/raw-request":
            self._json(200, {})
        elif url.path == "/v1/memberships" and self.command == "POST":
            self._json(400, {"error": {"code": "bad_request", "message": "nope"}})
        elif url.path == "/v1/users":
            # Hostile shape: a 200 whose body is not a JSON object.
            self._json(200, [1, 2])
        elif url.path == "/v1/audit":
            self.send_response(302)
            self.send_header("Location", "http://attacker.example/v1/audit")
            self.send_header("Content-Length", "0")
            self.end_headers()
        else:
            self._json(400, {"error": {"code": "bad_request", "message": "nope"}})

    do_GET = do_POST = do_PUT = do_PATCH = do_DELETE = _handle


class ClientSmokeTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = ThreadingHTTPServer(("127.0.0.1", 0), FakeControlPlane)
        threading.Thread(target=cls.server.serve_forever, daemon=True).start()
        cls.endpoint = f"http://127.0.0.1:{cls.server.server_port}"

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()

    def setUp(self):
        FakeControlPlane.requests.clear()
        FakeControlPlane.request_bodies.clear()
        FakeControlPlane.rate_calls = 0
        self.slept = []
        self.client = Client(
            self.endpoint, "olvk_test_secret", tenant="t-default",
            _sleep=self.slept.append,
        )

    def test_request_shape(self):
        out = self.client.get_v1_agents(limit="5", tenant="t-override")
        self.assertEqual([i["id"] for i in out["items"]], ["a", "b"])
        method, path, headers = FakeControlPlane.requests[0]
        self.assertEqual((method, path), ("GET", "/v1/agents?limit=5"))
        self.assertEqual(headers["Authorization"], "Bearer olvk_test_secret")
        self.assertEqual(headers["X-Olivares-Tenant"], "t-override")
        self.assertIn("olivares-client-python/", headers["User-Agent"])
        self.assertIn(f"api {API_VERSION}", headers["User-Agent"])

    def test_repeats_a_list_query_param(self):
        # The API has repeatable query parameters (GET /v1/audit's exclude_action is
        # the first). urlencode without doseq writes the Python repr of the list as a
        # single value, which no server can read back as N occurrences.
        self.client.get_v1_agents(limit="5", tag=["a", "b"])
        _, path, _ = FakeControlPlane.requests[0]
        self.assertEqual(path, "/v1/agents?limit=5&tag=a&tag=b")

    def test_path_escaping_through_generated_op(self):
        with self.assertRaises(APIError):
            self.client.get_v1_agents_by_id("a/b c")
        _, path, _ = FakeControlPlane.requests[0]
        self.assertEqual(path, "/v1/agents/a%2Fb%20c")

    def test_error_envelope(self):
        with self.assertRaises(APIError) as cm:
            self.client.get_v1_agents_by_id("missing")
        e = cm.exception
        self.assertEqual(
            (e.status, e.code, e.message, e.request_id),
            (404, "not_found", "no such agent", "req-42"),
        )

    def test_retry_429_honours_retry_after(self):
        out = self.client.post_v1_tokens(body={"name": "ci"})
        self.assertTrue(out["ok"])
        self.assertEqual(FakeControlPlane.rate_calls, 2)
        self.assertEqual(self.slept, [3.0])

    def test_no_retry_on_400(self):
        with self.assertRaises(APIError):
            self.client.post_v1_memberships(body={})
        self.assertEqual(len(FakeControlPlane.requests), 1)

    def test_pagination(self):
        ids = [i["id"] for i in self.client.paginate("/v1/agents")]
        self.assertEqual(ids, ["a", "b", "c"])

    def test_deprecation_warns_once_per_endpoint(self):
        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            self.client.get_v1_server_info()
            self.client.get_v1_server_info()
        deps = [w for w in caught if issubclass(w.category, DeprecationWarning)]
        self.assertEqual(len(deps), 1)
        self.assertIn("/v1/server-info is deprecated", str(deps[0].message))
        self.assertIn("migrate-example", str(deps[0].message))

    def test_deprecation_callback(self):
        notices = []
        c = Client(self.endpoint, "olvk_x", on_deprecation=notices.append)
        c.get_v1_server_info()
        c.get_v1_server_info()
        self.assertEqual(len(notices), 1)
        n = notices[0]
        self.assertIsInstance(n, DeprecationNotice)
        self.assertEqual(
            (n.method, n.path, n.deprecation, n.sunset, n.link),
            ("GET", "/v1/server-info", "@1780272000",
             "Thu, 01 Jun 2028 00:00:00 GMT",
             "https://docs.olivares.invalid/how-to/migrate-example/"),
        )

    def test_rejects_relative_endpoint(self):
        with self.assertRaises(ValueError):
            Client("not-a-url")

    def test_raw_operation_metrics(self):
        out = self.client.get_metrics()
        self.assertIsInstance(out, bytes)
        self.assertTrue(out.startswith(b"# HELP"))
        _, _, headers = FakeControlPlane.requests[0]
        self.assertNotEqual(headers.get("Accept"), "application/json")

    def test_raw_request_preserves_declared_content_type(self):
        self.client._do(
            "POST", "/raw-request", "/raw-request", body=b"{}\n",
            raw_request_content_type="application/x-ndjson",
        )
        self.assertEqual(
            FakeControlPlane.requests[-1][2].get("Content-Type"),
            "application/x-ndjson",
        )

        self.client._do(
            "PUT", "/raw-request", "/raw-request", body=b"raw",
            raw_request=True,
        )
        self.assertEqual(
            FakeControlPlane.requests[-1][2].get("Content-Type"),
            "application/octet-stream",
        )

        self.client._do(
            "POST", "/raw-request", "/raw-request", body=None,
            raw_request_content_type="application/x-ndjson",
        )
        self.assertIsNone(FakeControlPlane.requests[-1][2].get("Content-Type"))

    def test_required_json_none_is_null_while_optional_none_is_absent(self):
        self.client._do_json_required(
            "POST", "/raw-request", "/raw-request", body=None,
        )
        self.assertEqual(FakeControlPlane.request_bodies[-1], b"null")
        self.assertEqual(
            FakeControlPlane.requests[-1][2].get("Content-Type"),
            "application/json",
        )

        self.client._do(
            "POST", "/raw-request", "/raw-request", body=None,
        )
        self.assertEqual(FakeControlPlane.request_bodies[-1], b"")
        self.assertIsNone(FakeControlPlane.requests[-1][2].get("Content-Type"))

    def test_redirects_are_refused(self):
        with self.assertRaises(APIError) as cm:
            self.client.get_v1_audit()
        self.assertEqual(cm.exception.status, 302)
        # The credentialed request must never follow to the foreign origin.
        self.assertEqual(len(FakeControlPlane.requests), 1)

    def test_non_object_json_raises_apierror(self):
        with self.assertRaises(APIError) as cm:
            self.client.get_v1_users()
        self.assertEqual(cm.exception.code, "bad_response")

    def test_deprecation_dedup_per_route_template(self):
        notices = []
        c = Client(self.endpoint, "olvk_x", _sleep=self.slept.append,
                   on_deprecation=notices.append)
        c.delete_v1_tokens_by_id("tok_001")
        c.delete_v1_tokens_by_id("tok_002")
        self.assertEqual(len(notices), 1)
        self.assertEqual(notices[0].path, "/v1/tokens/tok_001")

    def test_retry_after_unicode_digit_does_not_raise(self):
        import io
        from email.message import Message
        from urllib.error import HTTPError

        from olivares_client._core import ClientCore

        for value in ("²", "１２３", "soon", "-1"):
            headers = Message()
            headers["Retry-After"] = value
            err = ClientCore._api_error(
                HTTPError("http://x/", 429, "rl", headers, io.BytesIO(b""))
            )
            self.assertEqual(err._retry_after, 0.0)


if __name__ == "__main__":
    unittest.main()
