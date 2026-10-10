#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Exercise the actual orchestration with fake external tools, never a daemon."""
import fcntl
import hashlib
from http.server import ThreadingHTTPServer
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import threading
import unittest
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
TOOL = r'''#!/usr/bin/env python3
import hashlib, json, os, pathlib, sys
args = sys.argv[1:]
name = pathlib.Path(sys.argv[0]).name
case = os.environ.get('CASE', '')
binary = b'canonical release binary'
with pathlib.Path(os.environ['RUNNER_TEMP'], 'calls.jsonl').open('a') as calls:
    calls.write(json.dumps([name, *args]) + '\n')
if name == 'goreleaser':
    assert args[:1] == ['build'] and '--snapshot' in args and '--single-target' in args
    assert args[args.index('--id') + 1] == 'olivares'
    config = pathlib.Path(args[args.index('--config') + 1]).read_text()
    # goreleaser rejects a second top-level key: exactly one snapshot section, and the
    # canonical config differs from it in that section's version_template line alone.
    original = pathlib.Path('.goreleaser.yaml').read_text().splitlines()
    lines = config.splitlines()
    assert lines.count('snapshot:') == 1 and len(lines) == len(original)
    changed = [i for i, (a, b) in enumerate(zip(original, lines)) if a != b]
    assert changed == [lines.index('snapshot:') + 1]
    assert lines[changed[0]] == "  version_template: '{{ .Env.FIRST_HOUR_VERSION }}'"
    pathlib.Path(args[args.index('--output') + 1]).write_bytes(binary)
    # The real before hook (license-gate.py --notice) writes the release notice.
    pathlib.Path('.license-notices').mkdir()
    pathlib.Path('.license-notices/NOTICE-community').write_text('generated notice')
elif name == 'pnpm':
    pathlib.Path(os.environ['RUNNER_TEMP'], 'browser-ran').touch()
    pathlib.Path(os.environ['RUNNER_TEMP'], 'base-url').write_text(os.environ['PLAYWRIGHT_BASE_URL'])
    if case == 'browser': sys.exit(7)
elif name == 'docker':
    if args[0] == 'build':
        assert pathlib.Path(args[-1], 'olivares').read_bytes() == binary
        assert pathlib.Path(args[-1], '.license-notices/NOTICE-community').read_text() == 'generated notice'
    elif args[0] == 'pull' and case == 'pull':
        sys.exit('pull failed')
    elif args[:2] == ['image', 'rm'] and case == 'rm-busy':
        sys.exit('image is in use')
    elif args[0] == 'compose':
        if 'ps' in args: print('container')
        if 'config' in args:
            print(json.dumps(dict(services=dict(olivares=dict(ports=[dict(
                mode='ingress', host_ip='0.0.0.0' if case == 'config-public' else '127.0.0.1',
                target=8443, protocol='tcp')])))))
        if 'config' not in args and 'port' in args: print('0.0.0.0:49153' if case == 'port-public' else '127.0.0.1:49153')
        if 'logs' in args:
            if case == 'logs' and '--timestamps' in args: sys.exit('log collection failed')
            if case != 'setup-banner': print('FIRST-BOOT SETUP')
            print('setup token: olst_TESTONLY')
            print('========================')
            print('api_key="test-only-key-material" Authorization: Bearer test-only-bearer')
            if pathlib.Path(os.environ['RUNNER_TEMP'], 'browser-ran').exists():
                print('late engine warning: provider request stalled')
    elif args[0] == 'port':
        print('8443/tcp -> ' + ('0.0.0.0' if case == 'bind-all' else '127.0.0.1') + ':49153')
    elif args[0] == 'inspect':
        print('sha256:stale' if case == 'image' else 'sha256:candidate')
    elif args[:2] == ['image', 'inspect']:
        # Before the build the two daemon-wide tags are absent, unless another workload owns them.
        built = any(json.loads(line)[:2] == ['docker', 'build']
                    for line in pathlib.Path(os.environ['RUNNER_TEMP'], 'calls.jsonl').read_text().splitlines())
        if not built and case != 'foreign-tags': sys.exit('no such image')
        print('sha256:candidate' if built else 'sha256:foreign')
    elif args[0] == 'cp':
        assert args[1] == 'container:/usr/local/bin/olivares'
        if case == 'copy':
            sys.exit('engine copy failed')
        pathlib.Path(args[2]).write_bytes(b'wrong' if case == 'hash' else binary)
    elif args[0] == 'exec':
        if args[2] != '/usr/local/bin/olivares':
            print('OCI runtime exec failed: executable file not found in $PATH', file=sys.stderr)
            sys.exit(127)
        if args[3:6] == ['agent', 'tool', 'detect']:
            assert args[6] == '--driver' and args[8:] == ['--output', 'json']
            if case == 'detect':
                sys.exit('tool detection failed')
            print(json.dumps([{'path': '/usr/bin/codex'}] if args[7] == 'codex' else []))
        elif 'version' in args:
            import base64
            fp = lambda key: 'release/' + hashlib.sha256(base64.b64decode(os.environ[key])).hexdigest()[:8]
            print(json.dumps(dict(version='0.0.0' if case == 'version' else os.environ['EXPECTED_VERSION'],
                commit='bad' if case == 'commit' else '1111111',
                license_key='dev/' + '0' * 8 if case == 'key' else fp('OLIVARES_LICENSE_PUBKEY'),
                ota_key=fp('OLIVARES_OTA_PUBKEY'))))
        else:
            raise AssertionError(args)
elif name == 'git':
    if 'rev-parse' in args: print('1' * 40)
    if 'status' in args and case == 'dirty': print(' M scripts/release-first-hour.sh')
else: raise AssertionError(name)
'''


class ReleaseFirstHourTest(unittest.TestCase):
    def prepare(self, tmp: str, case: str, candidate: str = '1.0', latest: str = '0.9',
                previous: str = '0.8', **overrides: str | None) -> tuple[Path, dict[str, str]]:
        # A private lock and no ambient overrides: a real run holding the host's lock must not
        # decide these cases.
        root = Path(tmp)
        (root / 'scripts').mkdir()
        (root / 'scripts/lib').mkdir()
        shutil.copy(ROOT / 'scripts/lib/git-env.sh', root / 'scripts/lib')
        shutil.copy(ROOT / 'scripts/release-first-hour.sh', root / 'scripts')
        shutil.copy(ROOT / 'scripts/assemble-runtime-context.sh', root / 'scripts')
        shutil.copy(ROOT / 'scripts/sanitize-first-hour.py', root / 'scripts')
        shutil.copytree(ROOT / 'scripts/userspace', root / 'scripts/userspace')
        for name in ['.goreleaser.yaml', 'Dockerfile.release', 'LICENSE', 'NOTICE',
                     'LICENSING.md', 'DISCLAIMER.md']:
            shutil.copy(ROOT / name, root / name)
        for name in ['LICENSES', 'packaging/container']:
            shutil.copytree(ROOT / name, root / name)
        (root / 'RELEASE-VERSION').write_text(candidate + '\n')
        bindir = root / 'bin'
        bindir.mkdir()
        # Any release metadata lookup would reintroduce the obsolete predecessor dependency.
        (bindir / 'sitecustomize.py').write_text("import urllib.request\ndef urlopen(*args, **kwargs):\n    raise AssertionError('first-hour must not require a published predecessor')\nurllib.request.urlopen = urlopen\n")
        for name in ['docker', 'pnpm', 'goreleaser', 'git']:
            tool = bindir / name
            tool.write_text(TOOL)
            tool.chmod(0o755)
        env = dict(os.environ, PATH=f'{bindir}:{os.environ["PATH"]}',
                   GITHUB_ACTIONS='true', RUNNER_ENVIRONMENT='github-hosted',
                   RUNNER_TEMP=tmp, GITHUB_RUN_ID='123', GITHUB_RUN_ATTEMPT='1',
                   GITHUB_SHA='1' * 40, CASE=case, PYTHONPATH=str(bindir),
                   CANDIDATE=candidate, LATEST=latest, PREVIOUS=previous,
                   EXPECTED_VERSION=candidate,
                   OLIVARES_LICENSE_PUBKEY='AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=',
                   OLIVARES_OTA_PUBKEY='AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI=')
        env['FIRST_HOUR_LOCK'] = f'{tmp}/lock'
        for key, value in overrides.items():
            if value is None:
                env.pop(key)
            else:
                env[key] = value
        return root, env

    def run_case(self, case: str, expected: int, candidate: str = '1.0', latest: str = '0.9',
                 previous: str = '0.8', message: str = '', **overrides: str | None) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root, env = self.prepare(tmp, case, candidate, latest, previous, **overrides)
            result = subprocess.run(['bash', str(root / 'scripts/release-first-hour.sh')],
                                    env=env, capture_output=True, text=True, timeout=30)
            self.assertEqual(result.returncode, expected, result.stdout + result.stderr)
            self.assertIn(message, result.stderr)
            self.assertEqual((root / 'browser-ran').exists(), expected == 0 or case in ['browser', 'logs', 'rm-busy'])
            self.assertEqual(list(root.glob('first-hour.*')), [])
            artifacts = root / 'first-hour-artifacts-candidate'
            if case != 'logs' and not case.startswith('releases-'):
                log = (artifacts / 'engine.log').read_text()
                self.assertNotIn('olst_TESTONLY', log)
                self.assertNotIn('test-only-key-material', log)
                self.assertNotIn('test-only-bearer', log)
                if (root / 'browser-ran').exists():
                    self.assertIn('late engine warning: provider request stalled', log)
            if expected and case not in ['copy', 'detect', 'browser', 'logs', 'setup-banner', 'pull', 'port-public', 'dirty', 'rm-busy', 'config-public', 'bind-all'] and not case.startswith('releases-'):
                self.assertIn('O1 J1-release-container: FAIL', result.stdout)
                self.assertIn('O7 J7-release-container: FAIL', result.stdout)
            image = 'docker.io/olivaresai/olivares:'
            calls_file = root / 'calls.jsonl'
            calls = [json.loads(line) for line in calls_file.read_text().splitlines()] if calls_file.exists() else []
            # Only the tags this run created are removed, after a build and on any outcome.
            tagged = any(call[:2] == ['docker', 'pull'] for call in calls) and case != 'pull'
            built = any(call[:2] == ['docker', 'build'] for call in calls)
            removals = [i for i, call in enumerate(calls) if call[:3] == ['docker', 'image', 'rm']]
            owned = [image + 'latest'] * tagged + [image + candidate] * built
            self.assertEqual([sorted(calls[i][3:]) for i in removals], [sorted(owned)] if owned else [])
            # After the run's containers are gone, so a tag in use is a failure, not a forced removal.
            downs = [i for i, call in enumerate(calls) if call[:2] == ['docker', 'compose'] and 'down' in call]
            if removals:
                self.assertTrue(downs and max(downs) < removals[0])
            if not expected:
                # Every Compose call layers the run-private names and ports over the README file.
                layers = '-f deploy/compose/docker-compose.yml -f deploy/compose/docker-compose.first-hour.ci.yml'
                compose_calls = [call for call in calls if call[:2] == ['docker', 'compose']]
                self.assertTrue(compose_calls)
                for call in compose_calls:
                    self.assertIn(layers, ' '.join(call))
                # The journey reaches the console on the port Compose reported, never a fixed one.
                self.assertEqual((root / 'base-url').read_text(), 'https://127.0.0.1:49153')
                self.assertTrue(any(call[-3:] == ['port', 'olivares', '8443'] for call in compose_calls))
                if case == 'foreign-tags':
                    # A tag another workload owned goes back to its image, after the removal.
                    back = [calls.index(['docker', 'tag', 'sha256:foreign', tag])
                            for tag in [image + 'latest', image + candidate]]
                    self.assertTrue(min(back) > removals[0])
                self.assertIn(['docker', 'pull', image + 'latest'], calls)
                self.assertFalse((root / 'release-api-called').exists())
                artifacts = root / 'first-hour-artifacts-candidate'
                self.assertEqual((artifacts / 'engine-sha256.txt').read_text().split()[0],
                                 hashlib.sha256(b'canonical release binary').hexdigest())
                self.assertEqual((artifacts / 'tools-before-setup.txt').read_text(),
                                 'claude: absent\ncodex: present\nopencode: absent\n')

    def test_release_image_needs_no_probe_utilities(self) -> None:
        self.run_case('', 0)

    def test_runs_on_a_shared_self_hosted_daemon(self) -> None:
        self.run_case('', 0, RUNNER_ENVIRONMENT='self-hosted')

    def test_refuses_outside_github_actions(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root, env = self.prepare(tmp, '', GITHUB_ACTIONS=None)
            result = subprocess.run(['bash', str(root / 'scripts/release-first-hour.sh')],
                                    env=env, capture_output=True, text=True, timeout=30)
            self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
            self.assertFalse((root / 'calls.jsonl').exists())

    def test_second_run_does_not_race_for_the_daemon_tags(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root, env = self.prepare(tmp, '')
            fd = os.open(env['FIRST_HOUR_LOCK'], os.O_CREAT | os.O_RDWR)
            try:
                fcntl.flock(fd, fcntl.LOCK_EX)
                result = subprocess.run(['bash', str(root / 'scripts/release-first-hour.sh')],
                                        env=env, capture_output=True, text=True, timeout=30)
            finally:
                os.close(fd)
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
            self.assertIn('owns this Docker daemon', result.stderr)
            self.assertFalse((root / 'calls.jsonl').exists())

    def test_console_must_be_on_loopback(self) -> None:
        self.run_case('port-public', 1, message='console is not published on loopback')
        self.run_case('bind-all', 1, message='beyond loopback')

    def test_failed_pull_leaves_no_tag_to_remove(self) -> None:
        self.run_case('pull', 1, message='pull failed')

    def test_failure_before_the_build_removes_only_the_cache_tag(self) -> None:
        self.run_case('dirty', 1)

    def test_foreign_tags_are_put_back(self) -> None:
        self.run_case('foreign-tags', 0)

    def test_unlayered_compose_config_stops_before_anything_starts(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root, env = self.prepare(tmp, 'config-public')
            result = subprocess.run(['bash', str(root / 'scripts/release-first-hour.sh')],
                                    env=env, capture_output=True, text=True, timeout=30)
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
            self.assertIn('not the run-private layer', result.stderr)
            calls = [json.loads(line) for line in (root / 'calls.jsonl').read_text().splitlines()]
            self.assertNotIn('pull', [call[1] for call in calls if call[0] == 'docker'])
            self.assertFalse((root / 'browser-ran').exists())

    def test_candidate_build_keeps_one_snapshot_key(self) -> None:
        # A canonical config whose snapshot section changed shape stops the gate before the build.
        for name, edit in [('no-template', lambda text: text.replace('  version_template:', '  name_template:', 1)),
                           ('second-section', lambda text: text + "\nsnapshot:\n  version_template: 'x'\n")]:
            with self.subTest(name), tempfile.TemporaryDirectory() as tmp:
                root, env = self.prepare(tmp, '')
                config = root / '.goreleaser.yaml'
                config.write_text(edit(config.read_text()))
                result = subprocess.run(['bash', str(root / 'scripts/release-first-hour.sh')],
                                        env=env, capture_output=True, text=True, timeout=30)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertIn('exactly one snapshot version_template', result.stderr)
                calls = [json.loads(line) for line in (root / 'calls.jsonl').read_text().splitlines()]
                self.assertNotIn('goreleaser', [call[0] for call in calls])
                self.assertFalse((root / 'browser-ran').exists())

    def test_tag_still_in_use_fails_the_run(self) -> None:
        self.run_case('rm-busy', 1, message='image is in use')

    def test_overlay_publishes_only_the_console_on_loopback(self) -> None:
        # Compose is not available to the battery; pin the layer's whole effective content.
        lines = [line.strip() for line in
                 (ROOT / 'deploy/compose/docker-compose.first-hour.ci.yml').read_text().splitlines()
                 if line.strip() and not line.lstrip().startswith('#')]
        self.assertEqual(lines, ['services:', 'olivares:', 'container_name: !reset null',
                                 'ports: !override', '- "127.0.0.1::8443"'])

    def test_versions_follow_release_line(self) -> None:
        for candidate, latest, previous in [('1.0', '0.9', '0.8'),
                                             ('1.10', '1.9', '1.8'),
                                             ('2.0', '1.299', '1.298')]:
            with self.subTest(candidate=candidate):
                self.run_case('', 0, candidate, latest, previous)

    def test_first_release_does_not_require_a_published_predecessor(self) -> None:
        for case in ['releases-error', 'releases-empty', 'releases-no-older']:
            with self.subTest(case=case):
                self.run_case(case, 0)

    def test_identity_drift_stops_before_browser(self) -> None:
        for case in ['image', 'hash', 'version', 'commit', 'key']:
            with self.subTest(case=case):
                self.run_case(case, 1)

    def test_probe_errors_stop_before_browser(self) -> None:
        for case in ['copy', 'detect']:
            with self.subTest(case=case):
                self.run_case(case, 1)

    def test_failed_journey_retains_whole_run_log(self) -> None:
        self.run_case('browser', 7)

    def test_session_journey_confirms_stop_before_resume(self) -> None:
        # The fresh-install preference opens a dialog; Stop alone never stops.
        # Pin the shared tool journey's ordering, not a substitute session flow.
        for path in ['web/e2e-release/first-hour.spec.ts', 'web/e2e/onboarding-first-hour.spec.ts']:
            with self.subTest(path=path):
                source = (ROOT / path).read_text()
                stop = source.index("await page.getByRole('button', { name: 'Stop', exact: true }).click()")
                resume = source.index(".getByRole('button', { name: 'Resume', exact: true })", stop)
                sequence = ' '.join(source[stop:resume].split())
                self.assertIn(".getByRole('dialog', { name: 'Stop this session?', exact: true }) "
                              ".getByRole('button', { name: 'Stop the session', exact: true }) .click()",
                              sequence, 'fresh-install Stop confirmation must be clicked before Resume')

    def test_log_collection_error_fails_qualification(self) -> None:
        self.run_case('logs', 1)

    def test_token_outside_readme_banner_does_not_qualify(self) -> None:
        self.run_case('setup-banner', 1)

    def test_cookie_log_redacts_every_cookie(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source, output, secrets = [root / name for name in ['raw.log', 'engine.log', 'secrets.json']]
            source.write_text('Cookie: first=test-only-first; session=test-only-second\nlate warning\n')
            secrets.write_text('[]')
            subprocess.run(['python3', str(ROOT / 'scripts/sanitize-first-hour.py'),
                            str(source), str(output), str(secrets)], check=True)
            self.assertNotIn('test-only-', output.read_text())
            self.assertIn('late warning', output.read_text())

    def test_stand_in_answers_only_after_the_command_ran(self) -> None:
        # An approval the tool refused, timed out or failed must not answer the
        # sentinel the journey waits for: only the command's own output does.
        spec = importlib.util.spec_from_file_location('provider', ROOT / 'web/e2e-release/provider.py')
        provider = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(provider)
        server = ThreadingHTTPServer(('127.0.0.1', 0), provider.Provider)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)

        def post(path: str, body: dict) -> str:
            request = urllib.request.Request(f'http://127.0.0.1:{server.server_port}{path}',
                                             json.dumps(body).encode(), {'Content-Type': 'application/json'})
            with urllib.request.urlopen(request) as response:
                return response.read().decode()

        turns = {
            '/v1/messages': lambda out: {'messages': [{'role': 'user', 'content': [
                {'type': 'tool_result', 'tool_use_id': 'toolu_first_hour', 'is_error': True, 'content': out}]}]},
            '/v1/responses': lambda out: {'input': [
                {'type': 'function_call_output', 'call_id': 'call_first_hour', 'output': out}]},
            '/v1/chat/completions': lambda out: {'messages': [
                {'role': 'tool', 'tool_call_id': 'call_first_hour', 'content': out}]},
        }
        for path, turn in turns.items():
            with self.subTest(path):
                refused = post(path, turn('Permission to use Bash has been denied.'))
                self.assertNotIn('FIRST_HOUR_PROVIDER_STUB_OK', refused)
                self.assertIn('FIRST_HOUR_STUB_TOOL_NOT_RUN', refused)
                self.assertIn('Permission to use Bash has been denied.', refused)
                self.assertIn('FIRST_HOUR_PROVIDER_STUB_OK', post(path, turn('first-hour-42\n')))


if __name__ == '__main__':
    unittest.main()
