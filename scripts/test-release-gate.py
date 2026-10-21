#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""The release runs release facts; CI owns batteries, and evidence gates must block."""
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[1]
BATTERIES = ('test-artifact-secrets.py', 'test-native-package-publication.py', 'test-release-environment-tag-policy.sh')


class ReleaseGate(unittest.TestCase):
    def setUp(self):
        self.release = yaml.safe_load((ROOT / '.github/workflows/release.yml').read_text())
        self.ci = yaml.safe_load((ROOT / '.github/workflows/pr-ci.yml').read_text())

    def test_batteries_move_to_ci(self):
        release = '\n'.join(step.get('run', '') for job in self.release['jobs'].values() for step in job.get('steps', []))
        ci = '\n'.join(step.get('run', '') for job in self.ci['jobs'].values() for step in job.get('steps', []))
        self.assertNotIn('task openapi:check', release)
        self.assertIn('task openapi:check', ci)
        for battery in BATTERIES:
            self.assertNotIn(battery, release)
            self.assertIn(battery, ci)
        self.assertIn('scripts/check-release-environment-tag-policy.sh', release)
        self.assertIn('scripts/ensure-nfpm.sh', release)

    def test_both_evidence_gates_block_and_checkout_is_full(self):
        steps = self.release['jobs']['goreleaser']['steps']
        gates = [step for step in steps if 'scripts/require-green-run.sh' in step.get('run', '')]
        self.assertEqual(len(gates), 2)
        self.assertTrue(all(not step.get('continue-on-error', False) for step in gates))
        self.assertIn('release-first-hour.yml "$GITHUB_SHA" --tree-equal-ok', gates[0]['run'])
        self.assertIn('race-full.yml "$GITHUB_SHA"', gates[1]['run'])
        self.assertNotIn('--tree-equal-ok', gates[1]['run'])
        self.assertEqual(steps[0]['with']['fetch-depth'], 0)
        self.assertTrue(any('git rev-parse --is-shallow-repository' in step.get('run', '') for step in steps))

    def test_guard_detects_reintroduced_batteries_and_removed_gates(self):
        path = ROOT / 'scripts/check-release-gate.py'
        self.assertTrue(path.exists(), 'workflow lint must enforce the release-gate rule')
        spec = importlib.util.spec_from_file_location('release_gate', path)
        guard = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(guard)
        ci = [self.ci, yaml.safe_load((ROOT / '.github/workflows/mainline-ci.yml').read_text())]
        self.assertEqual(guard.check(self.release, ci), [])
        for command in ('task openapi:check', 'go test ./core/api', 'python3 scripts/test-artifact-secrets.py',
                        'env X=1 task openapi:check', 'timeout 60 go test ./core/api',
                        './scripts/test-release-environment-tag-policy.sh', 'bash -c "task openapi:check"'):
            with self.subTest(command=command):
                changed = yaml.safe_load(yaml.safe_dump(self.release))
                changed['jobs']['goreleaser']['steps'].append({'run': command})
                self.assertTrue(guard.check(changed, ci))
        for workflow in ('race-full.yml', 'release-first-hour.yml'):
            changed = yaml.safe_load(yaml.safe_dump(self.release))
            steps = changed['jobs']['goreleaser']['steps']
            changed['jobs']['goreleaser']['steps'] = [s for s in steps if workflow not in s.get('run', '')]
            self.assertTrue(guard.check(changed, ci))
            changed = yaml.safe_load(yaml.safe_dump(self.release))
            gate = next(s for s in changed['jobs']['goreleaser']['steps'] if workflow in s.get('run', ''))
            gate['run'] += ' || true'
            self.assertTrue(guard.check(changed, ci))
            gate['run'] = '! ' + gate['run'].removesuffix(' || true')
            self.assertTrue(guard.check(changed, ci))
            for field, value in (('continue-on-error', True), ('if', False)):
                changed = yaml.safe_load(yaml.safe_dump(self.release))
                gate = next(s for s in changed['jobs']['goreleaser']['steps'] if workflow in s.get('run', ''))
                gate[field] = value
                self.assertTrue(guard.check(changed, ci))
        for command in ('# task openapi:check', 'echo "task openapi:check"'):
            changed = yaml.safe_load(yaml.safe_dump(self.release))
            changed['jobs']['goreleaser']['steps'].append({'run': command})
            self.assertEqual(guard.check(changed, ci), [])

    def test_metadata_guard_runs_against_real_full_and_shallow_git(self):
        step = next(s for s in self.release['jobs']['goreleaser']['steps']
                    if 'full release history' in s.get('name', ''))
        with tempfile.TemporaryDirectory(dir=os.environ.get('TMPDIR')) as tmp:
            env = dict(os.environ, GIT_CONFIG_GLOBAL='/dev/null', GIT_CONFIG_NOSYSTEM='1')
            subprocess.run(['git', 'init', '-q', tmp], env=env, check=True)
            subprocess.run(['git', '-C', tmp, '-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                            'commit', '-qm', 'base', '--allow-empty'], env=env, check=True)
            self.assertEqual(subprocess.run(['bash', '-c', step['run']], cwd=tmp, env=env).returncode, 0)
            head = subprocess.check_output(['git', '-C', tmp, 'rev-parse', 'HEAD'], env=env, text=True)
            (Path(tmp) / '.git/shallow').write_text(head)
            self.assertNotEqual(subprocess.run(['bash', '-c', step['run']], cwd=tmp, env=env).returncode, 0)


if __name__ == '__main__':
    unittest.main()
