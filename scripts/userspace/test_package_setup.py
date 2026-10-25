#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Keep native packaging commands and their helpers on the checked-out candidate."""
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import tempfile
import textwrap
from types import SimpleNamespace
import unittest
from unittest import mock
from urllib.error import HTTPError

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

    def test_upgrade_baseline_is_a_lower_release_than_the_candidate(self):
        # The DEB/RPM journey installs the baseline, then upgrades to the candidate, which builds
        # as X.Y~ci.<run> and so sorts below a published X.Y. A baseline equal to RELEASE-VERSION
        # turns the upgrade into a downgrade that rpm refuses.
        job = (ROOT / '.github/workflows/pr-ci.yml').read_text().split('  pr-build:\n', 1)[1]
        step = job.split('      - name: install, start, upgrade and uninstall DEB and RPM packages\n', 1)[1]
        body = re.split(r'\n      - ', step, maxsplit=1)[0]
        command = next(line for line in body.splitlines() if 'scripts/userspace/resolve_baseline.py' in line)
        words = shlex.split(command.split('>', 1)[0])
        args = words[words.index('scripts/userspace/resolve_baseline.py') + 1:]
        sys.path.insert(0, str(ROOT / 'scripts/userspace'))
        import resolve_baseline
        published = {tag: {'tag_name': tag, 'published_at': 'date', 'draft': False, 'prerelease': False}
                     for tag in ('0.1', '0.2', '0.3', '0.4')}
        tags = SimpleNamespace(returncode=0, stdout='\n'.join(published) + '\n')

        def release(path: str) -> dict:
            tag = path.rsplit('/', 1)[1]
            if tag not in published:
                raise HTTPError(path, 404, 'not published', {}, None)
            return published[tag]

        # A rebuild of the newest release upgrades from the one before it; a new version still
        # upgrades from the newest release.
        for candidate, expected in (('0.4', '0.3'), ('0.5', '0.4')):
            with self.subTest(candidate=candidate), \
                    mock.patch('resolve_baseline.subprocess.run', return_value=tags), \
                    mock.patch('resolve_baseline.get', side_effect=release):
                baseline = resolve_baseline.resolve(candidate, release_cut='--release-cut' in args)
                self.assertEqual(baseline['tag_name'], expected)


if __name__ == '__main__':
    unittest.main()
