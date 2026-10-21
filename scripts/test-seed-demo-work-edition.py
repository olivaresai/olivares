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
from pathlib import Path

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


if __name__ == "__main__":
    result = unittest.main(verbosity=2, exit=False).result
    sys.exit(0 if result.wasSuccessful() else 1)
