#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Replay the published-source worktree lifecycle on a persistent Git checkout: compat.sh adds
the worktree and each workflow's always() step removes it."""
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[2]
COMPAT = (ROOT / 'scripts/userspace/compat.sh').read_text()


def job(workflow: str, name: str) -> str:
    return (ROOT / '.github/workflows' / workflow).read_text().split(f'  {name}:\n', 1)[1]


class PublishedWorktree(unittest.TestCase):
    JOB = job('pr-ci.yml', 'userspace-compat')

    def setUp(self) -> None:
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.repo = self.root / 'repository'
        self.repo.mkdir()
        self.out = self.root / 'runner temp' / 'userspace'
        self.out.mkdir(parents=True)
        self.source = self.out / 'published-source'
        self.git('init', '-q')
        (self.repo / 'baseline.txt').write_text('published release\n')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 '-c', 'commit.gpgsign=false', 'commit', '-qm', 'baseline')
        self.sha = self.git('rev-parse', 'HEAD').strip()

    def git(self, *args: str) -> str:
        result = subprocess.run(['git', *args], cwd=self.repo, capture_output=True,
                                text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout

    def shell(self, script: str) -> subprocess.CompletedProcess:
        return subprocess.run(['bash', '-e', '-o', 'pipefail', '-c', script],
                              cwd=self.repo, capture_output=True, text=True, timeout=10,
                              env=dict(os.environ, USERSPACE_OUT=str(self.out),
                                       published_sha=self.sha))

    def setup_source(self) -> None:
        command = next(line.strip() for line in COMPAT.splitlines()
                       if line.strip().startswith('git worktree add '))
        result = self.shell(command)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.source / 'baseline.txt').read_text(), 'published release\n')

    def cleanup_source(self) -> subprocess.CompletedProcess:
        step = self.JOB.split('      - name: remove the published source worktree\n', 1)[1]
        body = re.split(r'\n      - ', step, maxsplit=1)[0]
        self.assertIn("if: always() && env.USERSPACE_OUT != ''", body)
        return self.shell(textwrap.dedent(body.split('        run: |\n', 1)[1]))

    def test_second_run_recovers_after_cancelled_job_and_runner_cleanup(self) -> None:
        self.setup_source()
        shutil.rmtree(self.out)  # Runner cleanup cannot remove Git's registration.
        self.git('clean', '-ffdx')
        self.git('reset', '--hard', 'HEAD')
        self.out.mkdir()
        self.setup_source()
        result = self.cleanup_source()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn(str(self.source), self.git('worktree', 'list', '--porcelain'))

    def test_two_normal_runs_remove_only_the_owned_worktree(self) -> None:
        other = self.root / 'unrelated'
        self.git('worktree', 'add', '--detach', str(other), self.sha)
        shutil.rmtree(other)  # Do not prune even unrelated stale registrations.
        for _ in range(2):
            self.setup_source()
            (self.source / 'generated.txt').write_text('capture output\n')
            result = self.cleanup_source()
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertFalse(self.source.exists())
            registered = self.git('worktree', 'list', '--porcelain')
            self.assertNotIn(str(self.source), registered)
            self.assertIn(str(other), registered)

    def test_cleanup_before_setup_is_harmless(self) -> None:
        result = self.cleanup_source()
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_cleanup_failure_is_not_hidden(self) -> None:
        self.setup_source()
        self.git('worktree', 'lock', str(self.source))
        result = self.cleanup_source()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('locked', result.stderr)


class IntegrationCheckWorktree(PublishedWorktree):
    JOB = job('integration-check.yml', 'integration-check')


if __name__ == '__main__':
    unittest.main()
