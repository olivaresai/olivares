#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Keep edition probes and visual fixtures on the real registered server-info route."""

import argparse
from pathlib import Path
import re
import sys
import unittest


class EditionProbeRoutes(unittest.TestCase):
    root = Path(__file__).resolve().parent.parent
    extra_fixtures = []
    callers = (
        "scripts/quickstart-smoke.sh",
        "scripts/web-e2e-demo.sh",
        "scripts/docs-captures.sh",
        "web/e2e/demo-graph.spec.ts",
        "web/e2e/console-func-l4.spec.ts",
        "web/e2e/docs-captures.spec.ts",
    )

    def registered_route(self):
        source = (self.root / "core/api/server.go").read_text()
        routes = re.findall(
            r'coreRoute\(v1,\s*"GET",\s*"([^"]+)",\s*publicRoute,\s*s\.handleServerInfo\)',
            source,
        )
        self.assertEqual(len(routes), 1, "edition evidence must come from the real server-info registrar")
        return "/v1" + routes[0]

    def test_script_and_browser_probes_use_the_registered_route(self):
        route = self.registered_route()
        for name in self.callers:
            with self.subTest(caller=name):
                requested = re.findall(r"/v1/(?:info|server-info)\b", (self.root / name).read_text())
                self.assertTrue(requested, "caller must retain its real server-info probe")
                self.assertEqual(requested, [route] * len(requested), "edition probe points outside the real registrar")

    def test_visual_fixtures_offer_only_the_registered_info_route(self):
        route = self.registered_route()
        for path in [self.root / "web/e2e-visual/fixtures.ts", *self.extra_fixtures]:
            with self.subTest(fixture=path.name):
                offered = re.findall(r"/v1/(?:info|server-info)\b", path.read_text())
                self.assertEqual(offered, [route], "visual fixture masks an unregistered info endpoint")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--extra-fixture", type=Path, action="append", default=[])
    args = parser.parse_args()
    EditionProbeRoutes.extra_fixtures = args.extra_fixture
    unittest.main(argv=[sys.argv[0]], verbosity=2)
