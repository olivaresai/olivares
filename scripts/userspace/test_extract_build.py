#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Only the exact PR build's regular-file binary may run."""
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest

from extract_build import extract


class BuildIdentity(unittest.TestCase):
    def bundle(self, root, *, checkout='candidate', digest=None, member='olivares', link=False, attempt='1',
               repository='olivaresai/olivares'):
        binary = b'the existing candidate executable'
        manifest = {'schema': 'olivares.cli-development-verification/v1',
                    'repository': repository, 'checkout_sha': checkout,
                    'pr_head_sha': 'pr-head', 'run_id': '123', 'run_attempt': attempt,
                    'binary': {'path': 'olivares', 'sha256': digest or hashlib.sha256(binary).hexdigest()}}
        files = {member: binary, 'manifest.json': json.dumps(manifest).encode(),
                 'version.json': b'{}', 'toolchain.txt': b'go', 'buildinfo.txt': b'go', 'SHA256SUMS': b'sums'}
        archive = root / 'build.tar.gz'
        with tarfile.open(archive, 'w:gz') as bundle:
            for name, value in files.items():
                entry = tarfile.TarInfo(name)
                entry.size = len(value)
                if link and name == 'olivares':
                    entry.type, entry.linkname, entry.size = tarfile.SYMTYPE, '/bin/sh', 0
                bundle.addfile(entry, io.BytesIO(value))
        return archive

    def run_cli(self, root, archive, repository='olivaresai/olivares', run_attempt='1', build_attempt='1'):
        event = root / 'event.json'
        event.write_text(json.dumps({'pull_request': {'head': {'sha': 'pr-head'}}}))
        env = dict(os.environ, GITHUB_REPOSITORY=repository, GITHUB_SHA='merge-candidate',
                   GITHUB_EVENT_PATH=str(event), GITHUB_RUN_ID='123', GITHUB_RUN_ATTEMPT=run_attempt)
        env.pop('BUILD_ATTEMPT', None)
        if build_attempt is not None:
            env['BUILD_ATTEMPT'] = build_attempt
        return subprocess.run(
            [sys.executable, str(Path(__file__).with_name('extract_build.py')), str(archive), str(root / 'out')],
            env=env, capture_output=True, text=True, timeout=10)

    def test_cli_binds_repository_and_pr_head(self):
        for repository in ('olivaresai/olivares', 'example/development'):
            for artifact_repo, checkout, succeeds in (
                    (repository, 'pr-head', True),
                    ('someone/other', 'pr-head', False),
                    (repository, 'merge-candidate', False)):
                with self.subTest(repository=repository, artifact_repo=artifact_repo, checkout=checkout):
                    with tempfile.TemporaryDirectory() as directory:
                        root = Path(directory)
                        archive = self.bundle(root, repository=artifact_repo, checkout=checkout)
                        result = self.run_cli(root, archive, repository=repository)
                        self.assertEqual(result.returncode == 0, succeeds, result.stderr)
                        if succeeds:
                            self.assertEqual((root / 'out/olivares').read_bytes(),
                                             b'the existing candidate executable')

    def test_cli_binds_the_attempt_pr_build_reported_not_the_consumers_own(self):
        # #2749: "re-run failed jobs" re-runs a failed userspace-compat at attempt 2 while
        # pr-build's attempt-1 success keeps its artifact, so the identity check must read
        # the PRODUCING attempt (pr-build's output, passed as BUILD_ATTEMPT), never the
        # consumer's GITHUB_RUN_ATTEMPT.
        for label, run_attempt, build_attempt, succeeds in (
                ('a rerun accepts the retained attempt-1 artifact', '2', '1', True),
                ('the first attempt accepts its own artifact', '1', '1', True),
                ('an artifact of another producing attempt is refused', '2', '2', False),
                ('a missing producing attempt is refused', '2', None, False),
                ('an empty producing attempt is refused', '2', '', False)):
            with self.subTest(case=label), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                result = self.run_cli(root, self.bundle(root, checkout='pr-head', attempt='1'),
                                      run_attempt=run_attempt, build_attempt=build_attempt)
                self.assertEqual(result.returncode == 0, succeeds, result.stderr)
                if build_attempt in (None, ''):
                    self.assertIn('pr-build reported no attempt', result.stderr)
                    self.assertFalse((root / 'out').exists())

    def test_exact_existing_build_is_usable(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            extract(self.bundle(root), root / 'out', 'candidate', 'pr-head', '123', '1', 'olivaresai/olivares')
            self.assertTrue((root / 'out/olivares').stat().st_mode & 0o111)

    def test_other_checkout_or_run_is_rejected(self):
        for checkout, run in [('other-checkout', '123'), ('candidate', '456')]:
            with self.subTest(checkout=checkout, run=run), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                with self.assertRaises(ValueError):
                    extract(self.bundle(root, checkout=checkout), root / 'out', 'candidate', 'pr-head', run, '1', 'olivaresai/olivares')

    def test_earlier_build_attempt_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with self.assertRaises(ValueError):
                extract(self.bundle(root, attempt='1'), root / 'out', 'candidate', 'pr-head', '123', '2', 'olivaresai/olivares')

    def test_substituted_binary_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with self.assertRaises(ValueError):
                extract(self.bundle(root, digest='0' * 64), root / 'out', 'candidate', 'pr-head', '123', '1', 'olivaresai/olivares')

    def test_link_and_traversal_members_are_rejected_before_extraction(self):
        for member, link in [('olivares', True), ('../olivares', False)]:
            with self.subTest(member=member), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                with self.assertRaises(ValueError):
                    extract(self.bundle(root, member=member, link=link), root / 'out', 'candidate', 'pr-head', '123', '1', 'olivaresai/olivares')
                self.assertFalse(any((root / 'out').iterdir()))


if __name__ == '__main__':
    unittest.main()
