#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Check class selection through the compiled misspell census CLI."""

import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest


class MisspellCensusTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR"))
        cls.addClassCleanup(cls.temp.cleanup)
        cls.root = Path(cls.temp.name)
        cls.tree = cls.root / "tree"
        cls.tree.mkdir()
        cls.binary = cls.root / "census"
        source = Path(__file__).with_name("misspell-census.go").resolve()
        subprocess.run(["go", "build", "-p", "2", "-o", str(cls.binary), str(source)],
                       check=True, capture_output=True, text=True, timeout=120)

    def census(self, findings, *args):
        return subprocess.run([str(self.binary), *args], input=findings,
                              cwd=self.tree, capture_output=True, text=True, timeout=10)

    def test_every_displayed_class_and_legacy_alias_returns_counted_locations(self):
        (self.tree / "source.go").write_text(
            'package fixture\n// colour\n// colour\nvar colour = "colour"\nvar Number = 1\n',
            encoding="utf-8")
        (self.tree / "source_test.go").write_text(
            'package fixture\nvar colour = "colour"\n', encoding="utf-8")
        unreadable_alias = "ILEGIBLE"  # language-data: legacy selector
        cases = [
            ("comment", "comentario", ["source.go:2:4", "source.go:3:4"]),
            ("identifier", "identificador", ["source.go:4:5"]),
            ("production string", "cadena de produccion", ["source.go:4:14"]),
            ("test string", "cadena de test", ["source_test.go:2:14"]),
            ("other", "otro", ["source.go:5:14"]),
            ("UNREADABLE", unreadable_alias, ["missing.go:1:1"]),
            ("PATH-FROM-ANOTHER-TREE", "RUTA-DE-OTRO-ARBOL", ["../missing.go:1:1"]),
            ("OUT-OF-RANGE", "FUERA-DE-RANGO", ["source.go:999:1"]),
        ]
        findings = "".join(
            f"{location}: `colour` is a misspelling of `color` (misspell)\n"
            for _, _, locations in cases for location in locations)
        summary = self.census(findings)
        # Unreadable/out-of-range findings must still refuse with exit 2.
        self.assertEqual(summary.returncode, 2, summary.stdout + summary.stderr)
        counts = dict(re.findall(r"^(.+?)\s+(\d+)$", summary.stdout.split("TOTAL")[0], re.M))
        self.assertEqual(counts, {label: str(len(locations)) for label, _, locations in cases})

        for label, legacy, locations in cases:
            expected = sorted(
                f"{(self.tree / path).resolve()}:{line}:{column}"
                for path, line, column in (location.rsplit(":", 2) for location in locations))
            for selector in (label, legacy):
                with self.subTest(label=label, selector=selector):
                    result = self.census(findings, "--clase", selector)
                    self.assertEqual(result.returncode, summary.returncode, result.stderr)
                    header = f'\ndetails for class "{label}": {counts[label]} location(s)\n'
                    self.assertIn(header, result.stdout)
                    self.assertEqual(result.stdout.split(header)[1].splitlines(),
                                     [f"  {location}" for location in expected])
                    self.assertEqual(result.stderr, summary.stderr)
                    isolated = "".join(
                        f"{location}: `colour` is a misspelling of `color` (misspell)\n"
                        for location in locations)
                    result = self.census(isolated, "--clase", selector)
                    self.assertEqual(result.returncode,
                                     2 if label in ("UNREADABLE", "OUT-OF-RANGE") else 0,
                                     result.stdout + result.stderr)
                    self.assertIn(header, result.stdout)
                    self.assertEqual(result.stdout.split(header)[1].splitlines(),
                                     [f"  {location}" for location in expected])


if __name__ == "__main__":
    unittest.main()
