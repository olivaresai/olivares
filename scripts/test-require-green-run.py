#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Release admission through the real script and Git; only the HTTP boundary is replaced."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / 'scripts/require-green-run.sh'


class GreenRun(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(dir=os.environ.get('TMPDIR'))
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.env = dict(os.environ, GH_TOKEN='synthetic-token', GITHUB_REPOSITORY='example/product',
                        GIT_CONFIG_GLOBAL='/dev/null', GIT_CONFIG_NOSYSTEM='1',
                        GIT_AUTHOR_NAME='Test', GIT_AUTHOR_EMAIL='test@example.invalid',
                        GIT_COMMITTER_NAME='Test', GIT_COMMITTER_EMAIL='test@example.invalid')
        for key in list(self.env):
            if key.startswith('GIT_') and key not in ('GIT_CONFIG_GLOBAL', 'GIT_CONFIG_NOSYSTEM',
                    'GIT_AUTHOR_NAME', 'GIT_AUTHOR_EMAIL', 'GIT_COMMITTER_NAME', 'GIT_COMMITTER_EMAIL'):
                del self.env[key]
        self.git('init', '-q')
        (self.root / 'product').write_text('base')
        self.git('add', '.')
        self.git('commit', '-qm', 'base')
        self.base = self.git('rev-parse', 'HEAD')
        self.same = self.git('commit-tree', 'HEAD^{tree}', '-p', self.base, '-m', 'same tree')
        (self.root / '.github/workflows').mkdir(parents=True)
        (self.root / '.github/workflows/test.yml').write_text('name: changed')
        self.git('add', '.')
        self.git('commit', '-qm', 'workflow')
        self.workflow_change = self.git('rev-parse', 'HEAD')
        (self.root / 'product').write_text('changed')
        self.git('add', '.')
        self.git('commit', '-qm', 'product')
        self.changed = self.git('rev-parse', 'HEAD')
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        curl = self.bin / 'curl'
        curl.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
assert 'synthetic-token' not in str(sys.argv)
assert '--config' in sys.argv and '-' in sys.argv
assert 'Authorization: Bearer synthetic-token' in sys.stdin.read()
assert '--fail' in sys.argv and '--max-time' in sys.argv
url = sys.argv[sys.argv.index('--url') + 1]
responses = json.loads(os.environ['RESPONSES'])
key = ('jobs2' if '&page=2' in url else 'jobs') if '/jobs?' in url else ('page2' if '&page=2' in url else 'runs')
value = responses.get(key, {'workflow_runs': []})
if value == 'error': sys.exit(22)
print(value if isinstance(value, str) else json.dumps(value))
''')
        curl.chmod(0o755)
        self.env['PATH'] = str(self.bin) + os.pathsep + self.env['PATH']

    def git(self, *args):
        return subprocess.check_output(['git', *args], cwd=self.root, env=self.env, text=True).strip()

    def run_gate(self, workflow='race-full.yml', sha=None, run=None, jobs=None, responses=None, tree=False, **env):
        run = run if run is not None else dict(id=123, head_sha=sha or self.base,
                event='workflow_dispatch', status='completed', conclusion='success')
        responses = responses or dict(runs={'workflow_runs': [run]},
                jobs=jobs if jobs is not None else {'jobs': [{'name': 'release-first-hour', 'conclusion': 'success'}]})
        args = ['bash', str(SCRIPT), workflow, sha or self.base]
        if tree:
            args.append('--tree-equal-ok')
        result = subprocess.run(args, cwd=self.root, env=dict(self.env, RESPONSES=json.dumps(responses), **env),
                                capture_output=True, text=True)
        self.assertNotIn('synthetic-token', result.stdout + result.stderr)
        return result

    def test_exact_success(self):
        for workflow in ('race-full.yml', 'release-first-hour.yml'):
            for event in ('push', 'workflow_dispatch'):
                run = dict(id=123, head_sha=self.base, event=event, status='completed', conclusion='success')
                with self.subTest(workflow=workflow, event=event):
                    result = self.run_gate(workflow, run=run)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn(self.base, result.stdout)

    def test_missing_and_untrusted_runs_refuse(self):
        for field, value in [('event', 'pull_request'), ('event', 'schedule'), ('conclusion', 'failure'),
                             ('conclusion', 'skipped'), ('status', 'in_progress')]:
            run = dict(id=123, head_sha=self.base, event='push', status='completed', conclusion='success')
            run[field] = value
            with self.subTest(field=field, value=value):
                self.assertEqual(self.run_gate(run=run).returncode, 1)
        self.assertEqual(self.run_gate(responses={'runs': {'workflow_runs': []}}).returncode, 1)

    def test_first_hour_requires_successful_job(self):
        for conclusion in ('skipped', 'failure', None):
            result = self.run_gate('release-first-hour.yml', jobs={'jobs': [
                {'name': 'release-first-hour', 'conclusion': conclusion}]})
            self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(self.run_gate('release-first-hour.yml', jobs={'jobs': []}).returncode, 1)

    def test_qualification_may_use_identical_tree_only_when_opted_in(self):
        run = dict(id=123, head_sha=self.base, event='push', status='completed', conclusion='success')
        for workflow in ('race-full.yml', 'release-first-hour.yml'):
            with self.subTest(workflow=workflow):
                result = self.run_gate(workflow, sha=self.same, run=run, tree=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn('tree identical', result.stdout)
                for sha in (self.same, self.workflow_change, self.changed):
                    self.assertEqual(self.run_gate(workflow, sha=sha, run=run).returncode, 1)
                for sha in (self.workflow_change, self.changed):
                    self.assertEqual(self.run_gate(workflow, sha=sha, run=run, tree=True).returncode, 1)
                for event in ('pull_request', 'schedule'):
                    untrusted = dict(run, event=event)
                    self.assertEqual(self.run_gate(workflow, sha=self.same, run=untrusted, tree=True).returncode, 1)
        self.assertEqual(self.run_gate('release-first-hour.yml', sha=self.same, run=run, tree=True,
            jobs={'jobs': [{'name': 'release-first-hour', 'conclusion': 'skipped'}]}).returncode, 1)

    def test_real_merge_reuses_pr_head_for_both_qualifications(self):
        self.git('checkout', '-qb', 'release-candidate', self.base)
        (self.root / 'product').write_text('release')
        self.git('add', 'product')
        self.git('commit', '-qm', 'release candidate')
        head = self.git('rev-parse', 'HEAD')
        self.git('checkout', '-qb', 'integration', self.base)
        self.git('merge', '--no-ff', '-qm', 'merge reviewed candidate', head)
        merged = self.git('rev-parse', 'HEAD')
        self.assertNotEqual(merged, head)
        self.assertEqual(self.git('rev-parse', merged + '^{tree}'), self.git('rev-parse', head + '^{tree}'))
        for workflow in ('race-full.yml', 'release-first-hour.yml'):
            run = dict(id=123, head_sha=head, event='workflow_dispatch', status='completed', conclusion='success')
            result = self.run_gate(workflow, sha=merged, run=run, tree=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn('head ' + head, result.stdout)

    def test_api_and_malformed_responses_fail_closed(self):
        for value in ('error', 'not json', {}, {'workflow_runs': None},
                      {'workflow_runs': [dict(id=1, head_sha='--upload-pack=bad', event='push',
                                              status='completed', conclusion='success')]}):
            self.assertEqual(self.run_gate(responses={'runs': value}).returncode, 2)
        for value in ('error', 'not json', {}, {'jobs': None}):
            self.assertEqual(self.run_gate('release-first-hour.yml', responses={
                'runs': {'workflow_runs': [dict(id=123, head_sha=self.base, event='push', status='completed', conclusion='success')]},
                'jobs': value}).returncode, 2)

    def test_run_pagination(self):
        rows = [dict(id=n + 1, head_sha=self.changed, event='push', status='completed', conclusion='success') for n in range(100)]
        good = dict(id=123, head_sha=self.base, event='push', status='completed', conclusion='success')
        result = self.run_gate(responses={'runs': {'workflow_runs': rows}, 'page2': {'workflow_runs': [good]}})
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_first_hour_job_pagination_and_duplicate_refusal(self):
        jobs = [{'name': 'other', 'conclusion': 'success'} for _ in range(100)]
        run = dict(id=123, head_sha=self.base, event='push', status='completed', conclusion='success')
        responses = dict(runs={'workflow_runs': [run]}, jobs={'jobs': jobs},
                         jobs2={'jobs': [{'name': 'release-first-hour', 'conclusion': 'success'}]})
        result = self.run_gate('release-first-hour.yml', responses=responses)
        self.assertEqual(result.returncode, 0, result.stderr)
        jobs[0] = {'name': 'release-first-hour', 'conclusion': 'success'}
        self.assertEqual(self.run_gate('release-first-hour.yml', responses=responses).returncode, 1)

    def test_missing_identical_tree_commit_is_fetched_without_shallowing(self):
        origin = self.root / 'origin'
        self.git('clone', '-q', '--bare', '.', str(origin))
        missing = subprocess.check_output(['git', '-C', str(origin), 'commit-tree',
            self.base + '^{tree}', '-m', 'missing'], env=self.env, text=True).strip()
        subprocess.run(['git', '-C', str(origin), 'update-ref', 'refs/heads/evidence', missing],
                       env=self.env, check=True)
        self.git('remote', 'add', 'origin', str(origin))
        run = dict(id=123, head_sha=missing, event='push', status='completed', conclusion='success')
        result = self.run_gate('release-first-hour.yml', run=run, tree=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('tree identical', result.stdout)
        self.assertEqual(self.git('rev-parse', '--is-shallow-repository'), 'false')
        run['head_sha'] = '2' * 40
        self.assertEqual(self.run_gate('release-first-hour.yml', run=run, tree=True).returncode, 2)
        good = dict(id=124, head_sha=self.base, event='push', status='completed', conclusion='success')
        result = self.run_gate('release-first-hour.yml', tree=True, responses={
            'runs': {'workflow_runs': [run, good]},
            'jobs': {'jobs': [{'name': 'release-first-hour', 'conclusion': 'success'}]}})
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_invalid_inputs_refuse_before_http(self):
        for kwargs in ({'sha': 'bad'}, {'workflow': '../bad.yml'}, {'GH_TOKEN': ''},
                       {'GITHUB_REPOSITORY': 'invalid'}, {'workflow': 'other.yml', 'tree': True}):
            self.assertEqual(self.run_gate(**kwargs).returncode, 2)

    def test_first_hour_compatibility_entrypoint(self):
        result = subprocess.run(['bash', str(ROOT / 'scripts/check-first-hour-evidence.sh')], cwd=self.root,
            env=dict(self.env, GITHUB_SHA=self.base, RESPONSES=json.dumps({'runs': {'workflow_runs': [
                dict(id=123, head_sha=self.base, event='push', status='completed', conclusion='success')]},
                'jobs': {'jobs': [{'name': 'release-first-hour', 'conclusion': 'success'}]}})),
            capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == '__main__':
    unittest.main()
