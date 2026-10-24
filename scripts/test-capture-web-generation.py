#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Filesystem/git qualification with fake task, Node and pnpm; no real build."""

import hashlib
import json
import os
import re
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import textwrap
import unittest


SOURCE = Path(os.environ.get(
    "WEB_CAPTURE_UNDER_TEST", Path(__file__).with_name("capture-web-generation.py")
))
HELPER = "scripts/capture-web-generation.py"
DIST = "core/internal/webui/dist"


class CaptureTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="web-capture-test-")
        self.addCleanup(self.tmp.cleanup)
        self.top = Path(self.tmp.name)
        self.repo = self.top / "repo"
        self.repo.mkdir()
        self.runner = self.top / "runner"
        self.runner.mkdir()
        self.bin = self.top / "fake-bin"
        self.bin.mkdir()
        self.write(HELPER, SOURCE.read_text())
        for name in ["Taskfile.yml", ".node-version", "package.json", "pnpm-lock.yaml",
                     "web/package.json", "web/pnpm-lock.yaml", "web/vite.config.ts",
                     "web/tsconfig.json", "web/index.html", "web/src/main.tsx",
                     ".github/workflows/pr-ci.yml", ".olivares-public-export"]:
            self.write(name, "fixture input\n")
        self.write(f"{DIST}/index.html", "old index\n")
        self.write(f"{DIST}/assets/old.js", "obsolete asset\n")
        self.write(f"{DIST}/assets/same.js", "unchanged asset\n")
        self.git("init", "-q")
        self.git("add", ".")
        self.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                 "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
        self.head = self.git("rev-parse", "HEAD").strip()
        for name in ["node", "pnpm", "task"]:
            exe = self.bin / name
            exe.write_text("#!/usr/bin/env python3\n" + (
                "import sys\nprint('fixture version 1')\n" if name != "task" else
                '''import os, pathlib, sys
if sys.argv[1:] == ['--version']:
    print('fixture task version 1')
    sys.exit(0)
assert sys.argv[1:] == ['build:web'], sys.argv
root = pathlib.Path.cwd()
mode = os.environ.get('FIXTURE_MODE', 'good')
if mode == 'missing':
    sys.exit(0)
dist = root / 'core/internal/webui/dist'
dist.mkdir(exist_ok=True)
(dist / 'index.html').write_text('<script src="/assets/new.js"></script>')
if mode == 'partial':
    sys.exit(0)
(dist / 'assets').mkdir(exist_ok=True)
(dist / 'assets/new.js').write_text('new asset\\n')
(dist / 'assets/same.js').write_text('unchanged asset\\n')
(dist / '.hidden').write_text('hidden output\\n')
(dist / 'empty-directory').mkdir(exist_ok=True)
(dist / 'restricted').mkdir(exist_ok=True)
(dist / 'restricted/required.js').write_text('nested required asset\\n')
if mode == 'failed':
    sys.exit(23)
if mode == 'mutate':
    (root / 'web/src/main.tsx').write_text('changed while building\\n')
if mode == 'new-input':
    (root / 'web/src/new.tsx').write_text('new while building\\n')
if mode == 'symlink':
    (dist / 'assets/link.js').symlink_to(root / 'web/src/main.tsx')
if mode == 'fifo':
    os.mkfifo(dist / 'pipe')
if mode == 'unreadable':
    (dist / 'assets/new.js').chmod(0)
if mode == 'empty':
    (dist / 'index.html').write_text('')
'''))
            exe.chmod(0o755)
        self.env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ["PATH"],
                        RUNNER_TEMP=str(self.runner), GITHUB_SHA=self.head,
                        PR_HEAD=self.head, GITHUB_REPOSITORY="olivaresai/olivares",
                        GITHUB_RUN_ID="123", GITHUB_RUN_ATTEMPT="2",
                        # The embed step reads the producing attempt from pr-web's output
                        # (#2749), so the extracted script needs the same environment.
                        GENERATION_ATTEMPT="2", CONSOLE_REBUILT_IN_THIS_JOB="false")
        # Ignore the invoking shell's git overrides; only this fixture is in scope.
        for name in list(self.env):
            if name.startswith("GIT_"):
                self.env.pop(name)

    def write(self, name, text):
        path = self.repo / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)

    def git(self, *args):
        return subprocess.check_output(["git", *args], cwd=self.repo, text=True)

    def capture(self, mode="good", fault=None):
        command = [sys.executable, HELPER]
        if fault:
            # Faults affect the real helper's archive/manifest writes, not its grader.
            self.write("fault-driver.py", '''import errno, os, pathlib, runpy, subprocess, sys, tarfile
from unittest.mock import patch
fault = sys.argv[1]
if fault in ('archive', 'tar', 'value'):
    error = {'archive': OSError('fixture archive failure'), 'tar': tarfile.TarError('fixture tar failure'),
             'value': ValueError('fixture value text')}[fault]
    with patch.object(tarfile, 'open', side_effect=error):
        runpy.run_path('scripts/capture-web-generation.py', run_name='__main__')
elif fault.startswith('enumeration-'):
    original_run, original_scan = subprocess.run, os.scandir
    build_succeeded = False
    root = pathlib.Path.cwd()
    target = root / 'core/internal/webui/dist/restricted'
    def run(args, *pos, **kwargs):
        global build_succeeded
        result = original_run(args, *pos, **kwargs)
        if args == ['task', 'build:web']:
            (root / 'fixture-build.exit').write_text(str(result.returncode))
            build_succeeded = result.returncode == 0
        return result
    def scan(path):
        if build_succeeded and isinstance(path, (str, os.PathLike)) and pathlib.Path(path) == target:
            (root / 'fixture-enumeration-fault').write_text(fault)
            if fault == 'enumeration-permission':
                raise PermissionError(errno.EACCES, 'fixture directory read denied')
            raise OSError(errno.EIO, 'fixture directory read failed')
        return original_scan(path)
    with patch.object(subprocess, 'run', run), patch.object(os, 'scandir', scan):
        runpy.run_path('scripts/capture-web-generation.py', run_name='__main__')
else:
    original = pathlib.Path.write_text
    def write(self, *args, **kwargs):
        if self.name == 'manifest.json':
            raise OSError('fixture manifest failure')
        return original(self, *args, **kwargs)
    with patch.object(pathlib.Path, 'write_text', write):
        runpy.run_path('scripts/capture-web-generation.py', run_name='__main__')
''')
            command = [sys.executable, "fault-driver.py", fault]
        return subprocess.run(command, cwd=self.repo, env=dict(self.env, FIXTURE_MODE=mode),
                              capture_output=True, text=True, timeout=10)

    def refuse(self, result):
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse((self.runner / "web-generation-artifact").exists())
        self.assertEqual(list(self.runner.iterdir()), [])

    def test_complete_tree_replaces_old_names_and_records_actual_inputs(self):
        result = self.capture()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        artifact = self.runner / "web-generation-artifact"
        manifest = json.loads((artifact / "manifest.json").read_text())
        self.assertEqual(manifest["checkout_sha"], self.head)
        self.assertEqual(manifest["pr_head_sha"], self.head)
        self.assertEqual(manifest["build_command"], ["task", "build:web"])
        self.assertEqual(manifest["build_exit"], 0)
        inputs = {p["path"]: p["sha256"] for p in manifest["inputs"]}
        self.assertEqual(inputs["web/src/main.tsx"], hashlib.sha256(b"fixture input\n").hexdigest())
        for required in [".node-version", "Taskfile.yml", HELPER, "web/pnpm-lock.yaml"]:
            self.assertIn(required, inputs)
        with tarfile.open(artifact / "dist.tar.gz") as archive:
            files = {m.name: archive.extractfile(m).read() for m in archive if m.isfile()}
            self.assertEqual(set(files), {"dist/index.html", "dist/assets/new.js",
                                         "dist/assets/same.js", "dist/.hidden",
                                         "dist/restricted/required.js"})
            self.assertIn("dist/empty-directory", archive.getnames())
            self.assertTrue(all(m.isfile() or m.isdir() for m in archive))
        output = {"dist/" + p["path"]: p["sha256"] for p in manifest["outputs"] if p["type"] == "file"}
        self.assertEqual(output, {p: hashlib.sha256(b).hexdigest() for p, b in files.items()})
        for line in (artifact / "SHA256SUMS").read_text().splitlines():
            digest, name = line.split("  ", 1)
            self.assertEqual(digest, hashlib.sha256((artifact / name).read_bytes()).hexdigest())

    def test_head_console_is_captured_when_merge_console_differs(self):
        self.write("web/src/main.tsx", "merge console input\n")
        self.git("add", "web/src/main.tsx")
        self.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                 "-c", "commit.gpgsign=false", "commit", "-qm", "merge fixture")
        self.env["GITHUB_SHA"] = self.git("rev-parse", "HEAD").strip()
        # The producer must reject the merge checkout even though GITHUB_SHA matches.
        self.refuse(self.capture())
        self.git("checkout", "--detach", self.head)
        result = self.capture()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        artifact = self.runner / "web-generation-artifact"
        manifest = json.loads((artifact / "manifest.json").read_text())
        self.assertEqual(manifest["checkout_sha"], self.head)
        self.assertEqual(manifest["pr_head_sha"], self.head)
        inputs = {p["path"]: p["sha256"] for p in manifest["inputs"]}
        self.assertEqual(inputs["web/src/main.tsx"], hashlib.sha256(b"fixture input\n").hexdigest())

    def test_workflow_producer_and_consumers_checkout_the_head(self):
        workflow = SOURCE.parent.parent / '.github/workflows/pr-ci.yml'
        for job in ['pr-web', 'pr-build', 'pr-test-shard', 'userspace-compat']:
            body = workflow.read_text().split(f'  {job}:\n', 1)[1]
            body = re.split(r'\n  [a-z][a-z0-9-]*:', body, maxsplit=1)[0]
            checkout = body.split('- uses: actions/checkout@', 1)[1].split('      - ', 1)[0]
            self.assertIn('ref: ${{ github.event.pull_request.head.sha }}', checkout, job)

    def consumer_script(self, job):
        workflow = SOURCE.parent.parent / '.github/workflows/pr-ci.yml'
        body = workflow.read_text().split(f'  {job}:\n', 1)[1]
        body = re.split(r'\n  [a-z][a-z0-9-]*:', body, maxsplit=1)[0]
        step = body.split('      - name: embed the verified console output\n', 1)[1]
        step = step.split('      - ', 1)[0]
        self.assertIn('PR_HEAD: ${{ github.event.pull_request.head.sha }}', step, job)
        return textwrap.dedent(step.split('        run: |\n', 1)[1])

    def test_self_hosted_development_rebuilds_without_console_transfer(self):
        workflow = (SOURCE.parent.parent / '.github/workflows/pr-ci.yml').read_text()
        local = "github.repository != 'olivaresai/olivares' && runner.environment == 'self-hosted'"
        for job in ['pr-build', 'pr-test-shard']:
            body = workflow.split(f'  {job}:\n', 1)[1]
            body = re.split(r'\n  [a-z][a-z0-9-]*:', body, maxsplit=1)[0]
            steps = body.split('      - ')[1:]
            rebuild = next((step for step in steps
                            if step.startswith('name: rebuild the console on the development runner\n')), None)
            self.assertIsNotNone(rebuild, job + ' still requires the Actions console artifact')
            self.assertIn('        if: ' + local + '\n', rebuild)
            self.assertIn('PR_HEAD: ${{ github.event.pull_request.head.sha }}', rebuild)
            self.assertNotIn('continue-on-error:', rebuild)
            self.assertEqual(rebuild.split('        run: ', 1)[1].strip(),
                             'python3 scripts/capture-web-generation.py')
            download = next(step for step in steps if 'uses: actions/download-artifact@' in step)
            self.assertIn('        if: ' + '${{ !(' + local + ') }}\n', download)
            self.assertNotIn('continue-on-error:', download)
            node = next(step for step in steps if 'uses: actions/setup-node@' in step)
            pnpm = next(step for step in steps if 'uses: pnpm/action-setup@' in step)
            for setup in [node, pnpm]:
                self.assertIn('        if: ' + local + '\n', setup)
            self.assertIn('node-version-file: .node-version', node)
            self.assertIn('dest: ${{ runner.temp }}/setup-pnpm', pnpm)
            self.assertIn('standalone: true', pnpm)
            self.assertLess(body.index('uses: actions/setup-node@'), body.index('uses: pnpm/action-setup@'))
            self.assertLess(body.index('uses: ./.github/actions/olivares-tool-cache'),
                            body.index('run: python3 scripts/capture-web-generation.py'))
            self.assertLess(body.index('run: python3 scripts/capture-web-generation.py'),
                            body.index('name: embed the verified console output'))
            # The embed step's local-rebuild branch must fire on exactly the runs that
            # rebuild locally (#2749): same condition as the rebuild/download steps above.
            embed = next(step for step in steps
                         if step.startswith('name: embed the verified console output\n'))
            self.assertIn('CONSOLE_REBUILT_IN_THIS_JOB: ${{ ' + local + ' }}', embed)
        producer = workflow.split('  pr-web:\n', 1)[1].split('\n  userspace-compat:', 1)[0]
        upload = next(step for step in producer.split('      - ')
                      if step.startswith('name: retain the generated console bundle\n'))
        self.assertIn("if: success() && steps.web-generation.outcome == 'success' && !(" + local + ')', upload)
        self.assertNotIn('continue-on-error:', upload)

    def test_local_rebuild_failure_cannot_reach_console_embedding(self):
        workflow = (SOURCE.parent.parent / '.github/workflows/pr-ci.yml').read_text()
        for job in ['pr-build', 'pr-test-shard']:
            body = workflow.split(f'  {job}:\n', 1)[1]
            body = re.split(r'\n  [a-z][a-z0-9-]*:', body, maxsplit=1)[0]
            marker = '      - name: rebuild the console on the development runner\n'
            self.assertIn(marker, body, job + ' has no local rebuild to propagate failure')
            rebuild = body.split(marker, 1)[1]
            rebuild = rebuild.split('      - ', 1)[0]
            command = rebuild.split('        run: ', 1)[1].strip()
            for mode in ['good', 'failed', 'partial', 'mutate']:
                with self.subTest(job=job, mode=mode):
                    artifact = self.runner / 'web-generation-artifact'
                    if artifact.exists():
                        shutil.rmtree(artifact)
                    self.git('checkout', '--', 'web/src/main.tsx')
                    script = command + '\n' + self.consumer_script(job)
                    result = subprocess.run(['bash', '-euo', 'pipefail', '-c', script],
                                            cwd=self.repo,
                                            env=dict(self.env, FIXTURE_MODE=mode,
                                                     GITHUB_WORKSPACE=str(self.repo),
                                                     GITHUB_REPOSITORY='fixture/development',
                                                     CONSOLE_REBUILT_IN_THIS_JOB='true'),
                                            capture_output=True, text=True, timeout=10)
                    self.assertEqual(result.returncode == 0, mode == 'good',
                                     result.stdout + result.stderr)
                    self.assertEqual(artifact.exists(), mode == 'good')

    def consume(self, script):
        return subprocess.run(['bash', '-e', '-o', 'pipefail', '-c', script],
                              cwd=self.repo,
                              env=dict(self.env, GITHUB_WORKSPACE=str(self.repo)),
                              capture_output=True, text=True, timeout=10)

    def test_consumers_reject_console_from_another_candidate_or_run(self):
        result = self.capture()
        self.assertEqual(result.returncode, 0, result.stderr)
        artifact = self.runner / 'web-generation-artifact'
        manifest_path = artifact / 'manifest.json'
        original = json.loads(manifest_path.read_text())
        destination = self.repo / DIST / 'index.html'
        for job in ['pr-build', 'pr-test-shard']:
            script = self.consumer_script(job)
            for field in [None, 'schema', 'checkout_sha', 'pr_head_sha', 'repository',
                          'run_id', 'run_attempt', 'digest']:
                with self.subTest(job=job, field=field):
                    manifest = dict(original)
                    if field:
                        manifest[field] = 'different'
                    manifest_path.write_text(json.dumps(manifest))
                    (artifact / 'SHA256SUMS').write_text(''.join(
                        hashlib.sha256((artifact / name).read_bytes()).hexdigest() + '  ' + name + '\n'
                        for name in ['dist.tar.gz', 'manifest.json']))
                    if field == 'digest':
                        manifest_path.write_text('{}')
                    destination.write_text('consumer sentinel')
                    result = self.consume(script)
                    if field:
                        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                        self.assertEqual(destination.read_text(), 'consumer sentinel')
                    else:
                        self.assertEqual(result.returncode, 0, result.stderr)
                        self.assertIn('/assets/new.js', destination.read_text())

    def test_consumers_reject_head_console_in_merge_checkout(self):
        result = self.capture()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.write('web/src/main.tsx', 'merge console input\n')
        self.git('add', 'web/src/main.tsx')
        self.git('-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid',
                 '-c', 'commit.gpgsign=false', 'commit', '-qm', 'merge fixture')
        self.env['GITHUB_SHA'] = self.git('rev-parse', 'HEAD').strip()
        destination = self.repo / DIST / 'index.html'
        for job in ['pr-build', 'pr-test-shard']:
            with self.subTest(job=job):
                script = self.consumer_script(job)
                destination.write_text('consumer sentinel')
                result = self.consume(script)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(destination.read_text(), 'consumer sentinel')
                self.git('checkout', '--detach', self.head)
                result = self.consume(script)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn('/assets/new.js', destination.read_text())
                self.git('checkout', '--detach', self.env['GITHUB_SHA'])

    def embed_step(self, job):
        workflow = SOURCE.parent.parent / '.github/workflows/pr-ci.yml'
        body = workflow.read_text().split(f'  {job}:\n', 1)[1]
        body = re.split(r'\n  [a-z][a-z0-9-]*:', body, maxsplit=1)[0]
        return body.split('      - name: embed the verified console output\n', 1)[1] \
                  .split('      - ', 1)[0]

    def test_consumer_embeds_the_attempt_the_generation_job_reported(self):
        # #2749: "re-run failed jobs" re-runs a failed consumer at attempt 2 while pr-web's
        # attempt-1 success keeps its artifact, so the identity check must read the
        # PRODUCING attempt — pr-web's own output on the download path, this job's own
        # attempt after a local rebuild — never the consumer's GITHUB_RUN_ATTEMPT, or
        # every such rerun fails at the embed step before a test runs.
        # pr-web uploaded the console at attempt 1; only the consumer re-runs, at 2.
        self.env["GITHUB_RUN_ATTEMPT"] = "1"
        result = self.capture()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        manifest = self.runner / 'web-generation-artifact' / 'manifest.json'
        self.assertEqual(json.loads(manifest.read_text())["run_attempt"], "1")
        destination = self.repo / DIST / 'index.html'
        for job in ['pr-build', 'pr-test-shard']:
            script = self.consumer_script(job)
            for label, attempt, rebuilt, accepted in [
                ('a rerun embeds the retained attempt-1 artifact', '1', 'false', True),
                ('an artifact of another producing attempt is still refused', '2', 'false', False),
                ('a rebuild flag over an artifact this job did not rebuild is refused', '1', 'true', False),
                ('a missing producing attempt is refused loudly', '', 'false', False),
            ]:
                with self.subTest(job=job, case=label):
                    destination.write_text('consumer sentinel')
                    result = subprocess.run(
                        ['bash', '-e', '-o', 'pipefail', '-c', script], cwd=self.repo,
                        env=dict(self.env, GITHUB_WORKSPACE=str(self.repo),
                                 GITHUB_RUN_ATTEMPT="2", GENERATION_ATTEMPT=attempt,
                                 CONSOLE_REBUILT_IN_THIS_JOB=rebuilt),
                        capture_output=True, text=True, timeout=10)
                    if label == 'a missing producing attempt is refused loudly':
                        self.assertIn('::error::', result.stdout + result.stderr)
                    if accepted:
                        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                        self.assertIn('/assets/new.js', destination.read_text())
                    else:
                        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                        self.assertEqual(destination.read_text(), 'consumer sentinel')

        # A development self-hosted runner rebuilt the console in THIS rerun job: the
        # manifest carries this job's attempt 2 while pr-web's output still says 1.
        self.env["GITHUB_RUN_ATTEMPT"] = "2"
        result = self.capture()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(json.loads(manifest.read_text())["run_attempt"], "2")
        for job in ['pr-build', 'pr-test-shard']:
            with self.subTest(job=job, case='a local rebuild at the rerun attempt'):
                destination.write_text('consumer sentinel')
                result = subprocess.run(
                    ['bash', '-e', '-o', 'pipefail', '-c', self.consumer_script(job)],
                    cwd=self.repo,
                    env=dict(self.env, GITHUB_WORKSPACE=str(self.repo),
                             GENERATION_ATTEMPT="1", CONSOLE_REBUILT_IN_THIS_JOB="true"),
                    capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn('/assets/new.js', destination.read_text())

        # The workflow shape that makes both paths read the producing attempt: pr-web
        # exports its own attempt, the embed steps take theirs from that output, and
        # neither compares the manifest against the consumer's GITHUB_RUN_ATTEMPT.
        workflow = (SOURCE.parent.parent / '.github/workflows/pr-ci.yml').read_text()
        self.assertIn('bundle-attempt: ${{ github.run_attempt }}',
                      workflow.split('  pr-web:\n', 1)[1])
        for job in ['pr-build', 'pr-test-shard']:
            with self.subTest(job=job, case='embed reads the producing attempt'):
                step = self.embed_step(job)
                self.assertIn('GENERATION_ATTEMPT: ${{ needs.pr-web.outputs.bundle-attempt }}', step)
                self.assertIn('CONSOLE_REBUILT_IN_THIS_JOB: ${{ github.repository'
                              " != 'olivaresai/olivares'", step)
                self.assertNotIn('--arg attempt "$GITHUB_RUN_ATTEMPT"', step)

    def test_userspace_compat_binds_the_attempt_pr_build_reported(self):
        # #2749, second consumer: userspace-compat downloads pr-build's artifact by name and
        # must check it against the attempt pr-build reported, not its own GITHUB_RUN_ATTEMPT
        # (scripts/userspace/test_extract_build.py runs the check itself).
        workflow = (SOURCE.parent.parent / '.github/workflows/pr-ci.yml').read_text()
        build = re.split(r'\n  [a-z][a-z0-9-]*:', workflow.split('  pr-build:\n', 1)[1], maxsplit=1)[0]
        self.assertIn('    outputs:\n', build.split('steps:', 1)[0])
        self.assertIn('      build-attempt: ${{ github.run_attempt }}\n', build.split('steps:', 1)[0])
        compat = workflow.split('  userspace-compat:\n', 1)[1]
        compat = re.split(r'\n  [a-z][a-z0-9-]*:', compat, maxsplit=1)[0]
        self.assertIn('    needs: pr-build\n', compat.split('steps:', 1)[0])
        step = compat.split('      - name: bind the existing binary to this candidate\n', 1)[1]
        step = step.split('      - ', 1)[0]
        self.assertIn('BUILD_ATTEMPT: ${{ needs.pr-build.outputs.build-attempt }}', step)
        self.assertIn('scripts/userspace/extract_build.py', step)

    def test_failed_and_partial_generation_never_publish(self):
        for mode in ["failed", "partial", "empty", "missing"]:
            with self.subTest(mode=mode):
                self.refuse(self.capture(mode))

    def test_changed_or_new_input_never_publishes(self):
        self.refuse(self.capture("mutate"))
        self.git("checkout", "--", "web/src/main.tsx")
        self.refuse(self.capture("new-input"))

    def test_nonregular_or_unreadable_output_never_publishes(self):
        for mode in ["symlink", "fifo", "unreadable"]:
            with self.subTest(mode=mode):
                self.refuse(self.capture(mode))
                shutil.rmtree(self.repo / DIST)

    def test_archive_and_manifest_failure_never_publish(self):
        for fault in ["archive", "manifest"]:
            with self.subTest(fault=fault):
                self.refuse(self.capture(fault=fault))

    def enumeration_failure(self, fault):
        result = self.capture(fault=fault)
        # An early failure to execute a fake tool is setup failure, not a kill.
        self.assertEqual((self.repo / "fixture-build.exit").read_text(), "0")
        self.assertEqual((self.repo / "fixture-enumeration-fault").read_text(), fault)
        self.assertEqual((self.repo / DIST / "restricted/required.js").read_text(),
                         "nested required asset\n")
        artifact = self.runner / "web-generation-artifact"
        print("ENUMERATION_WITNESS: " + json.dumps({
            "fake_build_exit": 0, "injected_fault": fault, "generated_leaf_exists": True,
            "capture_exit": result.returncode, "artifact_published": artifact.exists(),
        }))
        self.refuse(result)

    def test_directory_permission_failure_after_build_never_publishes(self):
        self.enumeration_failure("enumeration-permission")

    def test_directory_io_failure_after_build_never_publishes(self):
        self.enumeration_failure("enumeration-io")

    def test_dirty_input_and_wrong_identity_refuse_before_build(self):
        self.write("web/src/main.tsx", "dirty before build")
        self.refuse(self.capture())
        self.assertTrue((self.repo / DIST / "assets/old.js").exists())
        self.git("checkout", "--", "web/src/main.tsx")
        self.env["PR_HEAD"] = "c" * 40
        self.refuse(self.capture())

    def test_symlinked_ancestor_cannot_clear_external_directory(self):
        core = self.repo / "core"
        external = self.top / "external"
        core.rename(external)
        core.symlink_to(external, target_is_directory=True)
        self.refuse(self.capture())
        self.assertTrue((external / "internal/webui/dist/assets/old.js").exists())

    def test_committed_source_symlink_is_not_hashed_through_its_target(self):
        source = self.repo / "web/src/main.tsx"
        source.unlink()
        source.symlink_to(self.top / "outside-input")
        (self.top / "outside-input").write_text("outside fixture input")
        self.git("add", "web/src/main.tsx")
        self.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                 "-c", "commit.gpgsign=false", "commit", "-qm", "symlink fixture")
        self.env["PR_HEAD"] = self.git("rev-parse", "HEAD").strip()
        self.refuse(self.capture())
        self.assertTrue((self.repo / DIST / "assets/old.js").exists())

    def test_ignored_vite_environment_is_not_an_unrecorded_input(self):
        self.write(".git/info/exclude", ".env.local\n")
        self.write("web/.env.local", "VITE_FIXTURE=untracked\n")
        self.refuse(self.capture())
        self.assertTrue((self.repo / DIST / "assets/old.js").exists())

    def leave_artifact_of_earlier_job(self):
        stale = self.runner / "web-generation-artifact"
        stale.mkdir()
        (stale / "manifest.json").write_text("left by an earlier job\n")
        return stale

    def test_artifact_left_by_an_earlier_job_is_replaced(self):
        # A self-hosted runner can keep RUNNER_TEMP between jobs, and a leftover must not stop
        # the next capture. (2026-10-17: batteries jobs died 50 ms into this step, no reason logged.)
        stale = self.leave_artifact_of_earlier_job()
        result = self.capture()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        manifest = json.loads((stale / "manifest.json").read_text())
        self.assertEqual(manifest["run_id"], "123")
        self.assertEqual(sorted(p.name for p in stale.iterdir()),
                         ["SHA256SUMS", "dist.tar.gz", "manifest.json"])

    def test_failed_capture_leaves_no_artifact_of_an_earlier_job(self):
        # Once the destination is established, a failure of the build inputs and a failure of the
        # build alike: a later step must not take the artifact of an earlier job for this one.
        for label, prepare, mode in [
            ("build failure", lambda: None, "failed"),
            ("dirty input", lambda: self.write("web/src/main.tsx", "dirty before build"), "good"),
        ]:
            with self.subTest(label):
                stale = self.leave_artifact_of_earlier_job()
                prepare()
                result = self.capture(mode)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertFalse(stale.exists(), "the old artifact outlived a failed capture")
                self.git("checkout", "--", "web/src/main.tsx")

    def test_destination_that_is_not_a_plain_directory_is_refused_untouched(self):
        outside = self.top / "outside"
        outside.mkdir()
        (outside / "keep").write_text("keep")
        destination = self.runner / "web-generation-artifact"
        regular = self.top / "regular"
        regular.write_text("a regular file")
        for label, plant, kept in [
            ("symlink", lambda: destination.symlink_to(outside, target_is_directory=True),
             lambda: destination.is_symlink() and (outside / "keep").read_text() == "keep"),
            ("regular file", lambda: destination.write_text("a regular file"),
             lambda: destination.is_file() and destination.read_text() == "a regular file"),
            ("dangling symlink", lambda: destination.symlink_to(self.top / "nowhere"),
             lambda: destination.is_symlink() and not destination.exists()),
        ]:
            with self.subTest(label):
                plant()
                result = self.capture()
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn("artifact destination is not a plain directory", result.stderr)
                self.assertTrue(kept())
                destination.unlink()

    def test_every_refusal_names_its_reason(self):
        git_only = self.top / "git-only"
        git_only.mkdir()
        (git_only / "git").symlink_to(shutil.which("git"))
        fixture_path = str(self.bin) + os.pathsep + os.environ["PATH"]
        for label, reason, mutate, restore in [
            ("identity", "unexpected PR checkout identity",
             lambda: self.env.update(PR_HEAD="c" * 40), lambda: self.env.update(PR_HEAD=self.head)),
            ("environment", "missing environment variable GITHUB_RUN_ID",
             lambda: self.env.pop("GITHUB_RUN_ID"), lambda: self.env.update(GITHUB_RUN_ID="123")),
            ("dirty input", "a tracked build input differs from HEAD",
             lambda: self.write("web/src/main.tsx", "dirty before build"),
             lambda: self.git("checkout", "--", "web/src/main.tsx")),
            ("untracked configuration", "untracked build configuration",
             lambda: self.write("web/.env.local", "VITE_FIXTURE=untracked\n"),
             lambda: (self.repo / "web/.env.local").unlink()),
            ("missing tool", "node not found",
             lambda: self.env.update(PATH=str(git_only)), lambda: self.env.update(PATH=fixture_path)),
        ]:
            with self.subTest(label):
                mutate()
                result = self.capture()
                restore()
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn("capture failed (" + reason + ")", result.stderr)
        with self.subTest("build failure"):
            result = self.capture("failed")
            self.assertIn("capture failed (task exited 23)", result.stderr)
        with self.subTest("git diff cannot look"):
            # Exit 1 is "differs"; any other non-zero exit must refuse too, never read as clean.
            broken = self.top / "broken-git"
            broken.mkdir()
            wrapper = broken / "git"
            wrapper.write_text('#!/bin/sh\nfor arg in "$@"; do [ "$arg" = diff ] && exit 128; done\n'
                               'exec ' + shutil.which("git") + ' "$@"\n')
            wrapper.chmod(0o755)
            self.env["PATH"] = str(broken) + os.pathsep + fixture_path
            result = self.capture()
            self.env["PATH"] = fixture_path
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn("capture failed (git exited 128)", result.stderr)

    def test_refusal_log_carries_no_exception_text_path_or_environment_value(self):
        self.env["FIXTURE_TOKEN"] = "fixture-token-7f3a91"
        for fault, error_text, shown in [
                ("archive", "fixture archive failure", "I/O error"),
                ("manifest", "fixture manifest failure", "I/O error"),
                ("enumeration-permission", "fixture directory read denied", "I/O error EACCES"),
                ("tar", "fixture tar failure", "TarError"),
                ("value", "fixture value text", "ValueError)")]:
            with self.subTest(fault):
                result = self.capture(fault=fault)
                log = result.stdout + result.stderr
                self.assertNotEqual(result.returncode, 0, log)
                self.assertIn("capture failed (" + shown, result.stderr)
                self.assertNotIn("Traceback", log)
                for leaked in [error_text, str(self.top), "fixture-token-7f3a91"]:
                    self.assertNotIn(leaked, log)


if __name__ == "__main__":
    unittest.main(verbosity=2)
