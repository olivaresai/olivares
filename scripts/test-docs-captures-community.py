#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Capture Community budgets without cost seeding, using the real engine/browser.

Run through heavy-job with Chromium and web dependencies installed. OLIVARES_BIN
can select a Community binary; otherwise docs-captures.sh builds one.
"""

import hashlib
import json
import os
import subprocess
import unittest
from pathlib import Path


class CommunityCaptures(unittest.TestCase):
    def test_budgets_capture_both_themes_without_cost_samples(self):
        root = Path(__file__).resolve().parent.parent
        result = subprocess.run(
            ["bash", "scripts/docs-captures.sh", "--grep",
             "capture (stored-budgets|orchestration|automations|workflow-runs-error)$"],
            cwd=root, env={**os.environ, "PUBLICAR": "0"},
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
        )
        print(result.stdout, flush=True)
        self.assertEqual(result.returncode, 0)
        self.assertIn("Build: community;", result.stdout)
        self.assertIn("cost seed SKIPPED:", result.stdout)
        directory = root / "web/playwright-report/docs"
        manifest = json.loads((directory / "manifest.json").read_text())
        self.assertNotIn("stored-budgets", manifest["skipped"])
        self.assertEqual(set(manifest["skipped"]), {
            "orchestration", "automations", "workflow-runs-error",
        })
        self.assertEqual((manifest["views"], manifest["captures"]), (1, 2))
        self.assertEqual(manifest["unpaired"], [])
        self.assertEqual(manifest["raw_i18n_keys"], 0)
        captures = manifest["take"]
        self.assertEqual({(c["id"], c["theme"]) for c in captures}, {
            ("stored-budgets", "light"), ("stored-budgets", "dark"),
        })
        for capture in captures:
            self.assertEqual(capture["h1"], "Budgets")
            self.assertGreater(capture["vacios"], 0)
            png = directory / ("stored-budgets-%s.png" % capture["theme"])
            self.assertEqual(hashlib.sha256(png.read_bytes()).hexdigest(),
                             capture["sha256"])
        self.assertNotEqual(captures[0]["sha256"], captures[1]["sha256"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
