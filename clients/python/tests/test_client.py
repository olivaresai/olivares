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

import olivares_client  # noqa: E402
from olivares_client import _operations  # noqa: E402
from olivares_client import (  # noqa: E402
    APIError,
    API_VERSION,
    AuthCapabilityQuestion,
    AuthCapabilityQuestions,
    AuthCapabilityResult,
    AuthCapabilityResults,
    AuthCapabilitySelectors,
    Client,
    DeprecationNotice,
    SessionsCommunicationChannelCreateBody,
    SessionsCommunicationHandoffOfferBody,
    SessionsCommunicationMessageSendBody,
    SessionsCommunicationPublishResult,
)


# STATEMENT_EXPORT_CSV is a faithful answer from
# GET /v1/m/finops/statements/{id}/export: the handler's nine columns and one line.
# 9007199254740993 is 2**53+1 — the oracle, not filler. A consumer that routed this
# value through a JSON number and an IEEE double would return 9007199254740992.
STATEMENT_EXPORT_CSV = (
    "cost_center_code,cost_center_name,model,provider,agent,"
    "input_tokens,output_tokens,cost_micro_usd,sample_count\n"
    "ENG-01,Engineering,claude-opus-5,anthropic,,100,50,9007199254740993,1\n"
)


CAPABILITY_RESULTS_SCHEMA_2 = {
    "schema_version": 2,
    "results": [
        {"id": "sheet", "kind": "operation", "state": "allowed", "code": "authorized",
         "observed_at": "2026-09-07T10:00:00.250Z", "refresh_after_ms": 30000},
        {"id": "held", "kind": "operation", "state": "undisclosed",
         "code": "not_disclosed", "observed_at": "2026-09-07T10:00:00Z"},
        {"id": "list", "kind": "surface", "state": "reachable", "code": "admitted",
         "observed_at": "2026-09-07T10:00:00.100Z", "refresh_after_ms": 12000},
    ],
}


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

    # COMMIT_OUTCOME_UNKNOWN_ENVELOPE is the EXACT production 503 body, byte for
    # byte: the server encodes a map, so encoding/json emits the three top-level keys
    # in sorted order and appends a newline, and the nested error object carries the
    # same code as its message. A shorter invented body would make the control pass
    # against a fixture instead of against the engine.
    COMMIT_OUTCOME_UNKNOWN_ENVELOPE = (
        b'{"code":"commit_outcome_unknown","error":{"code":"commit_outcome_unknown",'
        b'"message":"commit_outcome_unknown"},"verdict":"NO_HE_PODIDO_MIRAR"}\n'
    )
    EVIDENCE_UNAVAILABLE_ENVELOPE = (
        b'{"code":"evidence_unavailable","error":{"code":"evidence_unavailable",'
        b'"message":"evidence_unavailable"},"verdict":"NO_HE_PODIDO_MIRAR"}\n'
    )

    def _raw(self, status, body, content_type):
        """Write exact bytes, so the fixture is the engine's envelope and not a re-encoding."""
        self.send_response(status)
        self.send_header("Content-Type", content_type)
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
        elif "/grants" in url.path and self.command == "GET":
            # There is deliberately NO Retry-After: nothing about an undetermined
            # commit is safe to repeat on a timer.
            envelope = (
                FakeControlPlane.EVIDENCE_UNAVAILABLE_ENVELOPE
                if q.get("state") == ["evidence_unavailable"]
                else FakeControlPlane.COMMIT_OUTCOME_UNKNOWN_ENVELOPE
            )
            self._raw(503, envelope, "application/json; charset=utf-8")
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
        elif url.path.startswith("/v1/m/finops/statements/") and url.path.endswith("/export"):
            # The chargeback statement export: always CSV on 200, never negotiated.
            body = STATEMENT_EXPORT_CSV.encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "text/csv; charset=utf-8")
            self.send_header("Content-Disposition",
                             'attachment; filename="chargeback_ENG-01_2026-06-01.csv"')
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        elif url.path == "/raw-request":
            self._json(200, {})
        elif url.path == "/v1/auth/capabilities" and self.command == "POST":
            # Schema 2 (docs/contracts/CAPABILITY-PROJECTION.md): one positive with its
            # budget, one concealed non-verdict without one, one surface admission.
            self._json(200, CAPABILITY_RESULTS_SCHEMA_2, headers=[("Cache-Control", "no-store")])
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
    def test_typed_communication_required_keys_match_published_contract(self):
        self.assertEqual(
            SessionsCommunicationChannelCreateBody.__required_keys__,
            frozenset({"workspace_id", "slug", "name", "initial_grants"}),
        )
        self.assertEqual(
            SessionsCommunicationMessageSendBody.__required_keys__,
            frozenset({"channel_id", "recipient", "content"}),
        )
        self.assertEqual(
            SessionsCommunicationHandoffOfferBody.__required_keys__,
            frozenset({
                "channel_id", "work_item_id", "recipient", "handoff",
                "ack_deadline",
            }),
        )
        self.assertTrue({
            "delivery_count", "required_count", "ack_quorum",
            "audience_hash", "payload_digest",
        }.issubset(SessionsCommunicationPublishResult.__required_keys__))

    def test_typed_communication_request_is_public(self):
        body: SessionsCommunicationMessageSendBody = {
            "channel_id": "00000000-0000-4000-8000-000000000001",
            "recipient": {
                "kind": "user",
                "ref": "00000000-0000-4000-8000-000000000002",
            },
            "content": {
                "subject": "scheduled",
                "blocks": [{"type": "text", "text": "hello"}],
            },
            "available_at": "2026-09-06T03:00:00Z",
        }
        self.assertEqual(body["available_at"], "2026-09-06T03:00:00Z")

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

    # --- C32: the commit-outcome retry veto ---------------------------------

    def test_commit_outcome_unknown_is_never_retried(self):
        """An automatic retry of an undetermined commit is not a harmless second
        attempt. Even on a GET it re-runs a governed read that commits its own audit
        act, so the client would turn one uncertain write into a second one and the
        operator would see two acts for one intention. The veto covers every method.
        """
        with self.assertRaises(APIError) as cm:
            self.client.get_v1_m_sessions_channels_by_id_grants(
                "01a084d5-988c-7e46-b396-5cff04bf2793",
                workspace_id="01a084d5-988c-7e46-b396-5cff04bf2794",
            )
        e = cm.exception
        self.assertEqual((e.status, e.code), (503, "commit_outcome_unknown"))
        self.assertEqual(
            len(FakeControlPlane.requests), 1,
            "retrying an undetermined commit can produce a second durable effect for one intention",
        )
        self.assertEqual(
            self.slept, [], "there is no interval on which this is safe to repeat"
        )

    def test_other_503_get_still_retried(self):
        """The preservation positive, and it is NEW for this client (N2): the
        503-GET retry that exists for the HA handoff is untouched for every other
        code. C32 adds one named exception, not a retry-policy change.
        """
        with self.assertRaises(APIError) as cm:
            self.client.get_v1_m_sessions_channels_by_id_grants(
                "01a084d5-988c-7e46-b396-5cff04bf2793",
                workspace_id="01a084d5-988c-7e46-b396-5cff04bf2794",
                state="evidence_unavailable",
            )
        self.assertEqual(cm.exception.code, "evidence_unavailable")
        self.assertEqual(
            len(FakeControlPlane.requests), 3,
            "initial plus two retries: the HA handoff retry is not C32's to remove",
        )

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

    def test_statement_export_operation_consumes_csv(self):
        """The GENERATED operation reads a CSV 200 as bytes.

        While the document declared this 200 an application/json object, the emitted
        method called ``self._do`` and ``_core`` demanded a JSON object, so this exact
        response raised APIError(bad_response) — a valid 200 no generated Python client
        could read. The assertion is on the operation, not on ``_do_raw``: the transport
        seam was always right and would have stayed green through the whole defect.
        """
        out = self.client.get_v1_m_finops_statements_by_id_export(
            "01a084d5-988c-7e46-b396-5cff04bf2793")
        self.assertIsInstance(out, bytes)
        self.assertEqual(out, STATEMENT_EXPORT_CSV.encode("utf-8"))
        self.assertIn(b"9007199254740993", out)
        method, path, headers = FakeControlPlane.requests[-1]
        self.assertEqual(method, "GET")
        self.assertEqual(
            path, "/v1/m/finops/statements/01a084d5-988c-7e46-b396-5cff04bf2793/export")
        # A CSV-only route must not demand JSON: the server does not negotiate.
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

    def test_capability_projection_schema_2_round_trip(self):
        # The generated DTOs are the only types used here; they are imported from the
        # package root, and the request they build is the schema 2 wire contract.
        workspace = "00000000-0000-4000-8000-000000000010"
        questions: AuthCapabilityQuestions = {
            "schema_version": 2,
            "questions": [
                AuthCapabilityQuestion(
                    id="sheet", kind="operation",
                    operation="GET /v1/m/sessions/channels/{id}/grants",
                    workspace_id=workspace,
                    selectors=AuthCapabilitySelectors(
                        path={"id": "00000000-0000-4000-8000-000000000001"}),
                ),
                {"id": "held", "kind": "operation",
                 "operation": "PATCH /v1/m/sessions/channels",
                 "workspace_id": workspace,
                 "selectors": {"body": {"channel_id": "00000000-0000-4000-8000-000000000002"}}},
                {"id": "list", "kind": "surface",
                 "operation": "GET /v1/m/sessions/channels", "workspace_id": workspace},
            ],
        }
        out: AuthCapabilityResults = self.client.post_v1_auth_capabilities(questions)

        method, path, headers = FakeControlPlane.requests[0]
        self.assertEqual((method, path), ("POST", "/v1/auth/capabilities"))
        self.assertEqual(headers["Content-Type"], "application/json")
        wire = json.loads(FakeControlPlane.request_bodies[0])
        self.assertEqual(wire["schema_version"], 2)
        self.assertEqual(
            [q["id"] for q in wire["questions"]], ["sheet", "held", "list"])
        self.assertEqual(
            wire["questions"][0]["selectors"],
            {"path": {"id": "00000000-0000-4000-8000-000000000001"}})
        self.assertNotIn("selectors", wire["questions"][2])
        self.assertEqual(wire["questions"][1]["workspace_id"], workspace)

        self.assertEqual(out["schema_version"], 2)
        self.assertEqual(
            [(r["id"], r["state"], r["code"]) for r in out["results"]],
            [("sheet", "allowed", "authorized"),
             ("held", "undisclosed", "not_disclosed"),
             ("list", "reachable", "admitted")])
        positive: AuthCapabilityResult = out["results"][0]
        concealed: AuthCapabilityResult = out["results"][1]
        self.assertEqual(positive["refresh_after_ms"], 30000)
        self.assertEqual(positive["observed_at"], "2026-09-07T10:00:00.250Z")
        # The non-verdict carries no budget at all: absent, not zero.
        self.assertNotIn("refresh_after_ms", concealed)
        self.assertEqual(concealed["observed_at"], "2026-09-07T10:00:00Z")
        # Re-serializing the typed result reproduces the wire exactly.
        self.assertEqual(json.loads(json.dumps(out)), CAPABILITY_RESULTS_SCHEMA_2)

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


class PublicTypedSurface(unittest.TestCase):
    """Every generated typed DTO must be importable the way this package documents.

    The generated module is private, so a DTO that ``__init__`` does not re-export
    cannot be named by a consumer at all. These checks ask the generated module which
    typed DTOs it emitted and require each one to be public, so the two cannot drift.
    """

    @staticmethod
    def _generated_dtos():
        # A generated DTO is a public TypedDict in the generated module. The functional
        # and split-totality helpers the emitter writes are underscore-prefixed and are
        # deliberately not part of the public surface.
        return {
            name: value
            for name, value in vars(_operations).items()
            if not name.startswith("_")
            and isinstance(value, type)
            and hasattr(value, "__required_keys__")
        }

    def test_every_generated_dto_is_publicly_importable(self):
        generated = self._generated_dtos()
        self.assertGreater(len(generated), 25, "the generated DTO census looks empty")
        unexported = sorted(n for n in generated if not hasattr(olivares_client, n))
        self.assertEqual(
            [], unexported, f"generated but not exported from olivares_client: {unexported}"
        )
        for name, value in sorted(generated.items()):
            with self.subTest(dto=name):
                self.assertIn(name, olivares_client.__all__, f"{name} missing from __all__")
                # Identity, not just presence: the public name must BE the generated
                # type, so an accidental shadowing copy cannot satisfy the export.
                self.assertIs(getattr(olivares_client, name), value)

    def test_wildcard_import_exposes_every_generated_dto(self):
        # ``__all__`` is what ``from olivares_client import *`` honours, so the census
        # above is repeated through the wildcard itself rather than trusted to the list.
        namespace: dict[str, object] = {}
        exec("from olivares_client import *", namespace)  # noqa:
        generated = self._generated_dtos()
        missing = sorted(n for n in generated if n not in namespace)
        self.assertEqual([], missing, f"absent from a wildcard import: {missing}")
        for name, value in generated.items():
            with self.subTest(dto=name):
                self.assertIs(namespace[name], value)

    def test_both_typed_families_are_represented(self):
        # Keeps the census from passing vacuously: if a family stopped being generated,
        # "every generated DTO is exported" would be trivially true.
        generated = self._generated_dtos()
        for prefix in ("SessionsCommunication", "AuthCapability"):
            with self.subTest(family=prefix):
                self.assertTrue(
                    any(name.startswith(prefix) for name in generated),
                    f"no generated DTO of the {prefix} family",
                )

    def test_capability_dtos_carry_their_published_fields(self):
        # An export that lost its shape would still satisfy the census above, so the
        # published fields of one family are asserted directly.
        self.assertEqual(
            {"schema_version", "questions"},
            set(olivares_client.AuthCapabilityQuestions.__required_keys__),
        )
        self.assertEqual(
            {"schema_version", "results"},
            set(olivares_client.AuthCapabilityResults.__required_keys__),
        )
        self.assertTrue(
            {"id", "kind", "state", "code", "observed_at"}.issubset(
                olivares_client.AuthCapabilityResult.__required_keys__
            )
        )
        self.assertEqual(
            {"path", "body"},
            set(olivares_client.AuthCapabilitySelectors.__optional_keys__),
        )


if __name__ == "__main__":
    unittest.main()
