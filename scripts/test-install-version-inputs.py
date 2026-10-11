#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Run the actual install-document version readers for current and historical releases."""
import os
from pathlib import Path
import subprocess
import unittest

ROOT = Path(__file__).resolve().parent.parent


class InstallVersionTests(unittest.TestCase):
    def test_actual_version_readers(self):
        for name, variable in (("check-install-docs.sh", "version"),
                               ("test-install-docs.sh", "VERSION")):
            text = (ROOT / "scripts" / name).read_text()
            start = text.index('case "$' + variable + '" in')
            code = 'blind() { echo "$*" >&2; exit 2; };\n' + text[start:text.index("esac", start) + 4]
            for version, expected in (("1.0", 0), ("1.1", 0), ("26.10.1", 0),
                                      ("v26.9.0", 0), ("bad", 2), ("1x.0", 2), ("1.0.extra", 2)):
                with self.subTest(caller=name, version=version):
                    result = subprocess.run(["bash", "-c", code], text=True, capture_output=True,
                                            env=dict(os.environ, **{variable: version}))
                    self.assertEqual(result.returncode, expected, result.stderr)


if __name__ == "__main__":
    unittest.main()
