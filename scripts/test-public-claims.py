#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Regression probes for committed public-claims provenance."""
import copy
import importlib.util
import json
import os
import sys
import subprocess
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location('claims', ROOT / 'scripts/check-public-claims.py')
claims = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(claims)
PRODUCER = sys.argv.pop(1) if len(sys.argv) > 1 and not sys.argv[1].startswith('-') else None
RETIRED = '852ce430ca6c74d7aa7783798175a3b473861f2c'


class SourceClaimsTests(unittest.TestCase):
    def setUp(self) -> None:
        self.producer = PRODUCER or claims.git(ROOT, 'rev-parse', 'HEAD').decode().strip()
        self.manifest = json.loads(claims.git(ROOT, 'show', f'{self.producer}:{claims.MANIFEST}'))

    def test_committed_measurement_rederives_from_this_repository(self) -> None:
        self.assertEqual(claims.check(ROOT, self.producer, self.manifest), [])

    def test_retired_producer_is_refused_by_command(self) -> None:
        result = subprocess.run(['python3', str(ROOT / 'scripts/check-public-claims.py'),
                                 '--producer', RETIRED], capture_output=True, text=True)
        self.assertEqual(result.returncode, 2)
        self.assertIn('absent from this repository', result.stderr)

    def test_retired_measurement_source_is_refused(self) -> None:
        self.manifest['measurement']['refs']['hub'] = RETIRED
        with self.assertRaisesRegex(claims.Unavailable, 'absent from this repository'):
            claims.check(ROOT, self.producer, self.manifest)

    def test_changed_anchor_is_refused(self) -> None:
        manifest = copy.deepcopy(self.manifest)
        path = manifest['claims'][0]['controlRoutes'][0].split('@')[0]
        manifest['claims'][0]['controlRoutes'][0] = path + '@' + '0' * 40
        self.assertTrue(any('does not match' in error for error in claims.check(ROOT, self.producer, manifest)))

    def test_changed_audit_digest_is_refused(self) -> None:
        self.manifest['measurement']['auditReportSha256'] = '0' * 64
        self.assertIn('measurement audit digest does not match the producer audit',
                      claims.check(ROOT, self.producer, self.manifest))

    def test_missing_control_source_is_refused(self) -> None:
        self.manifest['claims'][0]['controlRoutes'][0] = 'missing-source.go@' + '0' * 40
        self.assertTrue(any('not a regular source blob' in error
                            for error in claims.check(ROOT, self.producer, self.manifest)))

    def test_non_commit_identity_is_refused(self) -> None:
        with self.assertRaisesRegex(claims.Unavailable, 'exact 40-hex commit'):
            claims.commit(ROOT, 'main')

    def test_ambient_git_directory_cannot_redirect_source_reads(self) -> None:
        with patch.dict(os.environ, {'GIT_DIR': '/missing-claims-git-dir'}):
            self.assertEqual(claims.check(ROOT, self.producer, self.manifest), [])

    def test_git_ancestry_error_is_unavailable(self) -> None:
        read_git = claims.git

        def fail_ancestry(repo: Path, *args: str) -> bytes:
            if args[0] == 'merge-base':
                raise subprocess.CalledProcessError(128, ['git', 'merge-base'])
            return read_git(repo, *args)

        with patch.object(claims, 'git', side_effect=fail_ancestry):
            with self.assertRaisesRegex(claims.Unavailable, 'ancestry query failed'):
                claims.check(ROOT, self.producer, self.manifest)


if __name__ == '__main__':
    unittest.main()
