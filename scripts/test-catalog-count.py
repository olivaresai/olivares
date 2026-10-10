#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
import json
import os
from pathlib import Path
import runpy
import tempfile
import unittest

catalog_count = runpy.run_path(str(Path(__file__).parent / "lib/catalog_count.py"))["catalog_count"]


class CatalogMeasurementTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR"))
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        (self.root / "modules/compliance").mkdir(parents=True)
        (self.root / "modules/compliance/views_noenterprise.go").touch()
        self.artifact = self.root / "compliance.catalog.json"

    def test_community_is_unavailable_and_leaked_catalog_refuses(self):
        self.assertIsNone(catalog_count(self.root))
        self.artifact.write_text("{}")
        with self.assertRaises(ValueError):
            catalog_count(self.root)

    def test_private_catalog_cannot_silently_be_missing_empty_or_duplicate(self):
        (self.root / "modules/compliance/frameworks.go").touch()
        with self.assertRaises(OSError):
            catalog_count(self.root)
        for frameworks in ([], [{"id": ""}], [{"id": "a"}, {"id": "a"}]):
            with self.subTest(frameworks=frameworks):
                self.artifact.write_text(json.dumps({"frameworks": frameworks}))
                with self.assertRaises(ValueError):
                    catalog_count(self.root)
        self.artifact.write_text(json.dumps({"frameworks": [{"id": "a"}, {"id": "b"}]}))
        self.assertEqual(catalog_count(self.root), 2)

    def test_missing_edition_boundary_refuses(self):
        (self.root / "modules/compliance/views_noenterprise.go").unlink()
        with self.assertRaises(ValueError):
            catalog_count(self.root)


if __name__ == "__main__":
    unittest.main()
