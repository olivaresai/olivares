#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Regression battery for scripts/seed-demo-work.py: a demo estate holds only what its edition does.

A Community engine answers 501 to the deployment and evidence routes and its console has no page for
them, so its approval queue must not hold a request for `deploy.promote` or `evidence.export`
(scripts/lib/demo_edition.py). A Business estate keeps both. The edition is read from the engine's
`/v1/server-info`; an answer that names none stops the seeder instead of guessing.

No server, account or network: the API is a local fake wired through `pedir`.

    python3 scripts/test-seed-demo-work-edition.py
"""

from __future__ import annotations

import contextlib
import importlib.util
import io
import sys
import unittest
import urllib.error
from pathlib import Path
from unittest import mock

SEEDER = Path(__file__).resolve().with_name("seed-demo-work.py")
BUSINESS_ACTIONS = {"deploy.promote", "evidence.export"}


def load_seeder():
    spec = importlib.util.spec_from_file_location("seed_demo_work_edition_under_test", SEEDER)
    if spec is None or spec.loader is None:
        print("COULD NOT CHECK: cannot load %s" % SEEDER, file=sys.stderr)
        sys.exit(2)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def fake_engine(server_info):
    """`pedir` for one engine: `server_info` is what /v1/server-info answers; approvals are kept."""
    queue = []

    def pedir(base, token, tenant, method, path, cuerpo=None, cabeceras=None):
        del base, token, tenant, cabeceras
        if (method, path) == ("GET", "/v1/server-info"):
            return server_info
        if (method, path) == ("POST", "/v1/m/governance/approvals"):
            queue.append(cuerpo)
            return 201, {"id": "a%d" % len(queue)}
        if method == "GET" and path.startswith("/v1/m/governance/approvals"):
            return 200, {"items": [dict(item, status="pending") for item in queue]}
        raise AssertionError("unexpected call: %s %s" % (method, path))

    return pedir, queue


class EditionOfTheEstate(unittest.TestCase):
    def seed(self, server_info):
        seeder = load_seeder()
        pedir, queue = fake_engine(server_info)
        seeder.pedir = pedir
        err = io.StringIO()
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(err):
            rc = seeder.sembrar_permisos("http://127.0.0.1:1", "t", "tenant")
        return rc, {item["action"] for item in queue}, err.getvalue()

    def test_a_community_estate_queues_no_approval_for_a_business_action(self):
        rc, actions, _ = self.seed((200, {"edition": "community"}))
        self.assertEqual(rc, 0)
        self.assertTrue(actions, "a Community estate queues no approval at all")
        self.assertEqual(actions & BUSINESS_ACTIONS, set())

    def test_a_business_estate_keeps_the_approvals_only_it_can_act_on(self):
        for edition in ("enterprise", "business"):
            rc, actions, _ = self.seed((200, {"edition": edition}))
            self.assertEqual(rc, 0, edition)
            self.assertTrue(BUSINESS_ACTIONS <= actions, edition)

    def test_an_engine_that_names_no_edition_stops_the_seeder(self):
        for answer in ((200, {}), (200, {"edition": "mystery"}), (503, {"error": "down"})):
            rc, actions, err = self.seed(answer)
            self.assertEqual(rc, 1, answer)
            self.assertEqual(actions, set(), "approvals were seeded without knowing the edition")
            self.assertIn("edition", err)


class CanonicalEstate(unittest.TestCase):
    """--canonical-estate seeds the work-console surfaces and nothing the dataset owns."""

    def test_the_canonical_mode_writes_only_the_work_console_surfaces(self):
        seeder = load_seeder()
        calls, works, status, versions, catalog = [], [], {}, {}, []
        decisions = [1]

        def pedir(base, token, tenant, method, path, cuerpo=None, cabeceras=None):
            del base, token, tenant, cabeceras
            calls.append((method, path, cuerpo))
            if (method, path) == ("GET", "/v1/workspaces"):
                return 200, {"items": [{"id": "ws-1", "is_default": True}]}
            if (method, path) == ("GET", "/v1/auth/whoami"):
                return 200, {"user_id": "user-1"}
            if (method, path) == ("POST", "/v1/m/sessions/work-items?mode=apply"):
                wid = "wi-%d" % (len(works) + 1)
                works.append(wid)
                versions[wid], status[wid] = 1, "draft"
                return 200, {"result_id": wid, "version": 1}
            if method == "POST" and path.startswith("/v1/m/sessions/work-items/") \
                    and path.endswith("/transitions?mode=apply"):
                wid = path.split("/")[5]
                versions[wid] += 1
                status[wid] = {"item.ready": "ready", "item.block": "blocked"}[cuerpo["command"]]
                return 200, {"version": versions[wid]}
            if (method, path) == ("GET", "/v1/m/sessions/work-items?limit=100"):
                return 200, {"items": [{"id": wid, "status": status[wid]} for wid in works]}
            if (method, path) == ("POST", "/v1/m/sessions/decisions?mode=apply"):
                decisions[0] += 1
                return 200, {"version": decisions[0]}
            if path == "/v1/m/sessions/decisions?effective=true&limit=50":
                return 200, {"items": [{"id": "d%d" % i} for i in range(50)],
                             "has_more": True, "next_cursor": "page-2"}
            if path.startswith("/v1/m/sessions/decisions?effective=true&limit=50&cursor="):
                return 200, {"items": [{"id": "d50"}], "has_more": False}
            if (method, path) == ("POST", "/v1/m/catalog/entries"):
                cid = "cat-%d" % (len(catalog) + 1)
                catalog.append(cid)
                return 200, {"id": cid}
            if method == "POST" and path.startswith("/v1/m/catalog/entries/"):
                return 200, {}
            if (method, path) == ("GET", "/v1/m/catalog/entries?limit=100"):
                states = ["approved", "approved", "pending", "approved", "draft", "deprecated"]
                return 200, {"items": [{"id": "c%d" % i, "status": s} for i, s in enumerate(states)]}
            if (method, path) == ("POST", "/v1/m/claude-policy/managed-settings/publish"):
                return 501, {"error": "Fleet managed settings are a Business feature"}
            if (method, path) == ("GET", "/v1/server-info"):
                return 200, {"edition": "community"}
            raise AssertionError("unexpected call: %s %s" % (method, path))

        seeder.pedir = pedir
        old_hook = sys.excepthook
        try:
            with mock.patch.object(
                    sys, "argv",
                    ["seed-demo-work.py", "http://127.0.0.1:1", "t", "tenant",
                     "/tmp/work-root", "--canonical-estate"]), \
                    contextlib.redirect_stdout(io.StringIO()), \
                    contextlib.redirect_stderr(io.StringIO()):
                rc = seeder.main()
        finally:
            sys.excepthook = old_hook
        self.assertEqual(rc, 0, "the canonical mode did not finish")
        writes = [(method, path) for method, path, _ in calls if method != "GET"]
        allowed = ("/v1/m/sessions/work-items", "/v1/m/sessions/decisions",
                   "/v1/m/catalog/entries", "/v1/m/claude-policy/managed-settings/publish")
        outside = [write for write in writes if not write[1].startswith(allowed)]
        self.assertEqual(outside, [], "the canonical estate wrote a surface the dataset owns")
        self.assertEqual(len(works), len(seeder.ITEMS), "the work items were not all created")
        self.assertEqual(
            sum(1 for method, path, _ in calls
                if method == "POST" and path == "/v1/m/sessions/decisions?mode=apply"),
            seeder.DEFAULT_DECISION_COUNT, "the decisions page was not filled")
        self.assertEqual(len(catalog), len(seeder.ENTRADAS), "the catalog was not filled")


class ErrorBodyClip(unittest.TestCase):
    def test_a_secret_the_clip_would_cut_is_redacted_before_it_is_cut(self):
        # The engine writes the error body. Cut at 400 characters first, a token that straddles the
        # limit keeps its first half and no longer equals the remembered secret.
        seeder = load_seeder()
        token = "synthetic-bearer-" + "ABCDEFGHJKLMNPQRSTUVWXYZ" * 2
        body = ("x" * 380 + token).encode()

        def refuse(req, timeout):
            raise urllib.error.HTTPError(req.full_url, 502, "bad gateway", {}, io.BytesIO(body))

        seeder._R.recuerda(token)
        with mock.patch.object(seeder, "abre", refuse):
            code, answer = seeder.pedir("http://127.0.0.1:1", "t", "tenant", "GET", "/v1/x")
        self.assertEqual(code, 502)
        self.assertNotIn(token[:20], seeder._R(answer["error"]), "half of a cut token survived")


if __name__ == "__main__":
    result = unittest.main(verbosity=2, exit=False).result
    sys.exit(0 if result.wasSuccessful() else 1)
