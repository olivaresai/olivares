#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Keep native packaging commands and their helpers on the checked-out candidate."""
import os
from pathlib import Path
import re
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[2]


class PackageSetup(unittest.TestCase):
    def test_candidate_has_working_version_helper(self):
        result = subprocess.run(['sh', 'scripts/build-ldflags.sh', '--version'],
                                cwd=ROOT, capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        canon = [line.strip() for line in (ROOT / 'RELEASE-VERSION').read_text().splitlines()
                 if line.strip() and not line.lstrip().startswith('#')]
        self.assertEqual(result.stdout.splitlines(), canon)

    def test_workflow_uses_candidate_packaging_and_propagates_failure(self):
        job = (ROOT / '.github/workflows/pr-ci.yml').read_text().split('  pr-build:\n', 1)[1]
        step = job.split('      - name: package the candidate for the native service journey\n', 1)[1]
        body = re.split(r'\n      - ', step, maxsplit=1)[0]
        run = body.split('        run: ', 1)[1]
        script = textwrap.dedent(run.split('\n', 1)[1]) if run.startswith('|\n') else run.strip()
        for status in (0, 19):
            with self.subTest(status=status), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / 'scripts').mkdir()
                # Represents a candidate whose packaging implementation differs
                # from main. The merged workflow must execute that implementation.
                (root / 'scripts/package-service-candidate.sh').write_text(
                    '#!/bin/bash\nprintf "candidate\\n"\nexit "$CANDIDATE_STATUS"\n')
                result = subprocess.run(['bash', '-e', '-o', 'pipefail', '-c', script],
                                        cwd=root, env=dict(os.environ, CANDIDATE_STATUS=str(status)),
                                        capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, status, result.stderr)
                self.assertEqual(result.stdout, 'candidate\n')


if __name__ == '__main__':
    unittest.main()
