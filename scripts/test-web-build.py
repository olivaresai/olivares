#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Exercise the real web task with a deterministic pnpm fixture, without Node."""
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parent.parent
TASK = shutil.which("task")


class WebConsumerTests(unittest.TestCase):
    def test_pr_web_checks_openapi_after_installing_codegen_tools(self) -> None:
        workflow = (ROOT / ".github/workflows/pr-ci.yml").read_text()
        job = workflow.split("  pr-web:\n", 1)[1].split("\n  userspace-compat:\n", 1)[0]
        steps = re.split(r"(?m)^      - ", job.split("    steps:\n", 1)[1])[1:]
        checks = [i for i, step in enumerate(steps)
                  if re.search(r"(?m)^        run: task openapi:check$", step)]
        self.assertEqual(len(checks), 1, "pr-web must reject stale OpenAPI output")
        check = checks[0]
        for provider in ["actions/setup-node@", "pnpm/action-setup@",
                         "actions/setup-go@", "./.github/actions/olivares-tool-cache",
                         "run: python3 scripts/capture-web-generation.py"]:
            self.assertTrue(any(provider in step for step in steps[:check]), provider)
        self.assertNotIn("continue-on-error:", steps[check])
        self.assertNotIn("        if:", steps[check])
        self.assertRegex(steps[check], r"(?m)^        timeout-minutes: [1-9][0-9]*$")

    def test_control_plane_checks_history_before_embedding_console(self):
        workflow = (ROOT / ".github/workflows/mainline-ci.yml").read_text()
        job = workflow.split("  control-plane:\n", 1)[1]
        job = re.split(r"(?m)^  [\w-]+:\n", job, maxsplit=1)[0]
        steps = re.split(r"(?m)^      - ", job.split("    steps:\n", 1)[1])[1:]
        checkout = next(i for i, step in enumerate(steps)
                        if step.startswith("uses: actions/checkout@"))
        guard = next(i for i, step in enumerate(steps) if "        id: checkout-history\n" in step)
        embed = next(i for i, step in enumerate(steps)
                     if step.startswith("name: embed the verified console output\n"))
        env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
        for shallow in [False, True]:
            with self.subTest(shallow=shallow), tempfile.TemporaryDirectory() as tmp:
                top = Path(tmp)
                source = top / "source"
                subprocess.run(["git", "init", "-q", str(source)], env=env, check=True)
                for message in ["first", "second"]:
                    subprocess.run(["git", "-c", "user.name=Fixture",
                                    "-c", "user.email=fixture@example.invalid",
                                    "-c", "commit.gpgsign=false", "commit", "-q",
                                    "--allow-empty", "-m", message],
                                   cwd=source, env=env, check=True)
                repo = top / "checkout"
                subprocess.run(["git", "clone", "-q", *(["--depth", "1"] if shallow else []),
                                source.as_uri(), str(repo)], env=env, check=True)
                depth = subprocess.check_output(["git", "rev-parse", "--is-shallow-repository"],
                                                cwd=repo, env=env, text=True).strip()
                self.assertEqual(depth, "true" if shallow else "false")
                scripts = repo / "scripts"
                scripts.mkdir()
                shutil.copyfile(ROOT / "scripts/ci-postgres-service.sh",
                                scripts / "ci-postgres-service.sh")
                # Stage a valid downloaded artifact; run the real shell steps in
                # workflow order, each in its own shell just as Actions does.
                head = subprocess.check_output(["git", "rev-parse", "HEAD"],
                                               cwd=repo, env=env, text=True).strip()
                artifact = top / "web-generation-artifact"
                artifact.mkdir()
                dist = source / "dist"
                dist.mkdir()
                (dist / "index.html").write_text("generated console")
                subprocess.run(["tar", "-czf", str(artifact / "dist.tar.gz"), "dist"],
                               cwd=source, check=True)
                manifest = {"schema": "olivares.web-generation/v1", "checkout_sha": head,
                            "pr_head_sha": head, "repository": "example-org/example-repo",
                            "run_id": "1", "run_attempt": "1"}
                (artifact / "manifest.json").write_text(json.dumps(manifest) + "\n")
                (artifact / "SHA256SUMS").write_bytes(subprocess.check_output(
                    ["sha256sum", "dist.tar.gz", "manifest.json"], cwd=artifact))
                output = repo / "core/internal/webui"
                output.mkdir(parents=True)
                low_port, high_port = map(int, Path(
                    "/proc/sys/net/ipv4/ip_local_port_range").read_text().split())
                self.assertLess(low_port, high_port)
                run_env = dict(env, RUNNER_TEMP=tmp, GITHUB_WORKSPACE=str(repo),
                               GITHUB_ENV=str(top / "github-env"),
                               PGPORT_HOST=str(low_port), PGPORT_OTHER=str(high_port),
                               GITHUB_SHA=head, GITHUB_REPOSITORY="example-org/example-repo",
                               GITHUB_RUN_ID="1", GITHUB_RUN_ATTEMPT="1", GENERATION_ATTEMPT="1")
                for step in steps[checkout + 1:max(guard, embed) + 1]:
                    if "        run: |\n" not in step:
                        continue
                    script = textwrap.dedent(step.split("        run: |\n", 1)[1])
                    result = subprocess.run(["bash", "-euo", "pipefail", "-c", script],
                                            cwd=repo, env=run_env, capture_output=True,
                                            text=True, timeout=10)
                    if result.returncode:
                        break
                    if "ci-postgres-service.sh resolve" in script:
                        self.assertIn("resolved Postgres host port:", result.stdout)
                self.assertEqual(result.returncode, 2 if shallow else 0,
                                 result.stdout + result.stderr)
                history = repo / "history-count"
                if result.returncode == 0:
                    with history.open("w") as count:
                        subprocess.run(["git", "rev-list", "--count", "HEAD"],
                                       cwd=repo, env=env, stdout=count, check=True)
                self.assertEqual((output / "dist/index.html").exists(), not shallow,
                                 "console extraction ran before shallow-checkout refusal")
                self.assertEqual(history.exists(), not shallow)
                if not shallow:
                    self.assertEqual(history.read_text().strip(), "2")

    def test_race_consumers_receive_one_verified_bundle_and_require_csp(self):
        workflow = (ROOT / '.github/workflows/race-full.yml').read_text()
        jobs = dict(re.findall(r'^  ([\w-]+):\n(.*?)(?=^  [\w-]+:\n|\Z)',
                               workflow, re.M | re.S))
        self.assertEqual(workflow.count('run: python3 scripts/capture-web-generation.py'), 1)
        producer = next(name for name, job in jobs.items()
                        if 'run: python3 scripts/capture-web-generation.py' in job)
        self.assertIn('actions/upload-artifact@', jobs[producer])
        self.assertIn('bundle-artifact: web-generation-', jobs[producer])
        self.assertIn('bundle-attempt: ${{ github.run_attempt }}', jobs[producer])
        for name in ['race-root', 'race-workspace']:
            with self.subTest(job=name):
                job = jobs[name]
                self.assertRegex(job, rf'needs:.*\b{producer}\b')
                self.assertIn('actions/download-artifact@', job)
                self.assertIn('name: ${{ needs.' + producer + '.outputs.bundle-artifact }}', job)
                self.assertIn('OLIVARES_REQUIRE_WEB_BUNDLE: "1"', job)
                steps = job.split('      - name: embed the verified console output\n', 1)[1]
                embed_step = steps.split('\n      - ', 1)[0]
                embed = steps.split('        run: |\n', 1)[1].split('\n      - ', 1)[0]
                self.assertLess(job.index('tar -xzf dist.tar.gz'), job.index('bash scripts/race-groups.sh'))
                # The shape that makes the step read the producing attempt: the bindings below
                # are what the executed cases inject, so a wrong binding fails here, not only in CI.
                self.assertIn('PR_HEAD: ${{ github.sha }}', embed_step)
                self.assertIn('GENERATION_ATTEMPT: ${{ needs.' + producer + '.outputs.bundle-attempt }}',
                              embed_step)
                self.assertNotIn('--arg attempt "$GITHUB_RUN_ATTEMPT"', embed)
                # Execute the consumer: only the producer's own identity may extract, and a
                # foreign manifest or a corrupt archive must fail before extraction.
                env = {k: v for k, v in os.environ.items() if not k.startswith('GIT_')}
                # (manifest changes, corrupt archive, stale candidate, setup's attempt, this job's
                # attempt): each refusal changes one thing. A stale candidate names an older commit
                # in the manifest and PR_HEAD, while the checkout stays on the candidate; only the
                # checkout guard can refuse it. The manifest carries the PRODUCING job's attempt
                # ('1'): "re-run failed jobs" re-runs a race job at attempt 2 against setup's
                # earlier success, so this job's own attempt never decides (#2749).
                cases = {
                    'matching identity': ({}, False, False, '1', '1'),
                    're-run of a failed race job': ({}, False, False, '1', '2'),
                    'other checkout': ({'checkout_sha': '0' * 40}, False, False, '1', '1'),
                    'other pr head': ({'pr_head_sha': '0' * 40}, False, False, '1', '1'),
                    'other schema': ({'schema': 'olivares.web-generation/v0'}, False, False, '1', '1'),
                    'other run': ({'run_id': '41'}, False, False, '1', '1'),
                    # The manifest names this job's own attempt, not the producer's: refused.
                    'other attempt': ({'run_attempt': '2'}, False, False, '1', '2'),
                    'other repository': ({'repository': 'fork/project'}, False, False, '1', '1'),
                    'checkout is not the candidate': ({}, False, True, '1', '1'),
                    'corrupt archive': ({}, True, False, '1', '1'),
                    'empty producing attempt': ({}, False, False, '', '1'),
                }
                for case, (changed, corrupt, stale, producer_attempt, job_attempt) in cases.items():
                    with self.subTest(case=case), tempfile.TemporaryDirectory() as tmp:
                        root = Path(tmp)
                        subprocess.run(['git', 'init', '-q', tmp], env=env, check=True)
                        for message in ['previous', 'candidate']:
                            subprocess.run(['git', '-c', 'user.name=Fixture',
                                            '-c', 'user.email=fixture@example.invalid',
                                            '-c', 'commit.gpgsign=false', 'commit', '-q',
                                            '--allow-empty', '-m', message],
                                           cwd=tmp, env=env, check=True)
                        head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=tmp,
                                                       env=env, text=True).strip()
                        identity = subprocess.check_output(['git', 'rev-parse', 'HEAD~1'], cwd=tmp,
                                                           env=env, text=True).strip() if stale else head
                        artifact = root / 'web-generation-artifact'
                        artifact.mkdir()
                        dist = root / 'source/dist'
                        dist.mkdir(parents=True)
                        (dist / 'index.html').write_text('generated console')
                        subprocess.run(['tar', '-czf', str(artifact / 'dist.tar.gz'), 'dist'],
                                       cwd=dist.parent, check=True)
                        manifest = {'schema': 'olivares.web-generation/v1', 'checkout_sha': identity,
                                    'pr_head_sha': identity, 'repository': 'example/project',
                                    'run_id': '42', 'run_attempt': '1', **changed}
                        (artifact / 'manifest.json').write_text(json.dumps(manifest) + '\n')
                        sums = subprocess.check_output(['sha256sum', 'dist.tar.gz', 'manifest.json'],
                                                       cwd=artifact)
                        (artifact / 'SHA256SUMS').write_bytes(sums)
                        if corrupt:
                            (artifact / 'dist.tar.gz').write_text('corrupt')
                        output = root / 'core/internal/webui'
                        output.mkdir(parents=True)
                        result = subprocess.run(
                            ['bash', '-euo', 'pipefail', '-c', textwrap.dedent(embed)], cwd=tmp,
                            env=dict(env, RUNNER_TEMP=tmp, GITHUB_WORKSPACE=tmp,
                                     PR_HEAD=identity, GENERATION_ATTEMPT=producer_attempt,
                                     GITHUB_REPOSITORY='example/project',
                                     GITHUB_RUN_ID='42', GITHUB_RUN_ATTEMPT=job_attempt),
                            capture_output=True, text=True, timeout=10)
                        valid = not changed and not corrupt and not stale and producer_attempt != ''
                        self.assertEqual(result.returncode == 0, valid, result.stdout + result.stderr)
                        self.assertEqual((output / 'dist/index.html').exists(), valid)
                        if not producer_attempt:
                            # jq alone would refuse too; the reviewer-visible reason is the error.
                            self.assertIn('::error::setup reported no attempt', result.stdout + result.stderr)

    def test_pr_build_rejects_unsuccessful_console_before_consuming_artifacts(self):
        workflow = (ROOT / ".github/workflows/pr-ci.yml").read_text()
        job = workflow.split('  pr-build:\n', 1)[1].split('\n  pr-test-shard:\n', 1)[0]
        first_step = job.split('    steps:\n', 1)[1].split('\n      - ', 1)[0]
        self.assertNotIn('continue-on-error:', first_step)
        command = re.search(r'^        run: (.+)$', first_step, re.M)
        self.assertIsNotNone(command, "check the producer result before checkout or download")
        self.assertIn('${{ needs.pr-web.result }}', command[1])
        for result in ['success', 'failure', 'cancelled', 'skipped', '']:
            with self.subTest(producer_result=result):
                script = command[1].replace('${{ needs.pr-web.result }}', result)
                run = subprocess.run(['bash', '-euo', 'pipefail', '-c', script],
                                     capture_output=True, text=True, timeout=5)
                self.assertEqual(run.returncode, 0 if result == 'success' else 1,
                                 run.stdout + run.stderr)

    def test_docker_web_stages_need_only_tracked_source(self):
        # A local generated index can conceal a broken clean Docker context.
        if (ROOT / ".git").exists():
            tracked = set(subprocess.check_output(
                ["git", "ls-files"], cwd=ROOT, text=True,
                env={k: v for k, v in os.environ.items() if not k.startswith("GIT_")}).splitlines())
        else:
            # Private assembly is a git archive plus reviewed overlays, without .git.
            # Inspect that source tree, never the enclosing repository or generated output.
            generated = {".git", "node_modules", "dist", ".build", "build", "coverage", "__pycache__"}
            tracked = {p.relative_to(ROOT).as_posix() for p in ROOT.rglob("*")
                       if p.is_file() and not generated.intersection(p.relative_to(ROOT).parts)}
        for name in ["Dockerfile"]:
            with self.subTest(dockerfile=name):
                stage = (ROOT / name).read_text().split(" AS web\n", 1)[1].split("\nFROM ", 1)[0]
                for line in stage.splitlines():
                    if line.startswith("COPY "):
                        for source in shlex.split(line)[1:-1]:
                            self.assertTrue(source in tracked or any(
                                p.startswith(source.rstrip("/") + "/") for p in tracked),
                                f"{name}: clean context lacks {source}")

    def test_nightly_builds_console_before_compiling_engine(self):
        workflow = (ROOT / ".github/workflows/drills-nightly.yml").read_text()
        walk = workflow.split("        id: walk\n", 1)[1].split("        run: |\n", 1)[1]
        setup = textwrap.dedent(walk.split('          mkdir -p "$RUNNER_TEMP/walk-estate"', 1)[0])
        # Execute the actual setup commands with bounded stand-ins. Compilation
        # must never see a placeholder-only tree, or proceed after a failed build.
        for mode in ["good", "web", "browser", "engine"]:
            with self.subTest(failed_stage=mode), tempfile.TemporaryDirectory() as tmp:
                prelude = '''
task() { test "$*" = 'build:web' || return 90; test "$FAIL_STAGE" != web || return 23; touch "$RUNNER_TEMP/index.html"; }
pnpm() { test "$FAIL_STAGE" != browser; }
go() { test -f "$RUNNER_TEMP/index.html" || return 91; test "$FAIL_STAGE" != engine || return 24; touch "$RUNNER_TEMP/compiled"; }
export -f task pnpm go
'''
                env = dict(os.environ, RUNNER_TEMP=tmp, GITHUB_OUTPUT=tmp + "/outputs",
                           FAIL_STAGE=mode, RUNNER_ENVIRONMENT="self-hosted")
                result = subprocess.run(["bash", "-euo", "pipefail", "-c", prelude + setup],
                                        env=env, text=True, capture_output=True, timeout=10)
                if mode != "good":
                    self.assertFalse((Path(tmp) / "compiled").exists())
                    self.assertTrue((Path(tmp) / "outputs").exists(), "failure was not reported")
                    self.assertIn("failed=true", (Path(tmp) / "outputs").read_text())
                else:
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertTrue((Path(tmp) / "compiled").exists())

    def test_mainline_consumers_receive_the_verified_bundle(self):
        workflow = (ROOT / ".github/workflows/mainline-ci.yml").read_text()
        jobs = dict(re.findall(r"^  ([\w-]+):\n(.*?)(?=^  [\w-]+:\n|\Z)",
                               workflow, re.M | re.S))
        self.assertIn("actions/upload-artifact@", jobs["web-bundle"])
        self.assertIn("capture-web-generation.py", jobs["web-bundle"])
        for name in ["functional-shard", "control-plane", "race-rest", "race-hot-manifest"]:
            with self.subTest(job=name):
                job = jobs[name]
                self.assertRegex(job, r"needs: \[[^\n]*\bweb-bundle\b")
                self.assertIn("actions/download-artifact@", job)
                self.assertIn("sha256sum -c SHA256SUMS", job)
                self.assertIn('tar -xzf dist.tar.gz -C "$GITHUB_WORKSPACE/core/internal/webui"', job)
                self.assertRegex(job, r'(?m)^    env:\n      OLIVARES_REQUIRE_WEB_BUNDLE: "1"$')
                self.assertIn('name: ${{ needs.web-bundle.outputs.bundle-artifact }}', job)
                self.assertLess(job.index('tar -xzf dist.tar.gz'), job.index('run: task test:')
                                if 'run: task test:' in job else job.index('task test:functional:shard'))
        self.assertRegex(jobs['web-bundle'], r'(?m)^    outputs:\n      bundle-artifact: web-generation-')
        self.assertNotIn('bundle-artifact', jobs['web'])

    def test_mainline_embed_refuses_foreign_manifest_before_extraction(self):
        workflow = (ROOT / ".github/workflows/mainline-ci.yml").read_text()
        jobs = dict(re.findall(r"^  ([\w-]+):\n(.*?)(?=^  [\w-]+:\n|\Z)",
                               workflow, re.M | re.S))
        outputs = jobs['web-bundle'].split('    needs:', 1)[0]
        self.assertIn('      bundle-attempt: ${{ github.run_attempt }}\n', outputs)
        head, other = 'a' * 40, 'b' * 40
        repository, run_id = 'example-org/example-repo', '1001'
        manifest = {'schema': 'olivares.web-generation/v1', 'checkout_sha': head,
                    'pr_head_sha': head, 'repository': repository, 'run_id': run_id,
                    'run_attempt': '1'}
        # (case, manifest overrides, producing attempt, GITHUB_RUN_ATTEMPT, embeds)
        cases = [
            ('valid manifest', {}, '1', '1', True),
            ('re-run of a failed consumer', {}, '1', '2', True),
            ('wrong head', {'checkout_sha': other, 'pr_head_sha': other}, '1', '1', False),
            ('wrong checkout only', {'checkout_sha': other}, '1', '1', False),
            ('wrong pr head only', {'pr_head_sha': other}, '1', '1', False),
            ('wrong run', {'run_id': '1002'}, '1', '1', False),
            ('wrong repository', {'repository': 'olivaresai/olivares'}, '1', '1', False),
            ('wrong schema', {'schema': 'olivares.web-generation/v0'}, '1', '1', False),
            ('foreign attempt', {'run_attempt': '2'}, '1', '1', False),
            ('empty producing attempt', {}, '', '1', False),
        ]
        for name in ['control-plane', 'functional-shard', 'race-rest', 'race-hot-manifest']:
            job = jobs[name]
            step = job.split('      - name: embed the verified console output\n', 1)[1]
            self.assertIn('GENERATION_ATTEMPT: ${{ needs.web-bundle.outputs.bundle-attempt }}',
                          step.split('        run: |\n', 1)[0])
            embed = step.split('        run: |\n', 1)[1].split('\n      - ', 1)[0]
            for case, overrides, attempt, runner_attempt, embeds in cases:
                with self.subTest(job=name, case=case), tempfile.TemporaryDirectory() as tmp:
                    root = Path(tmp)
                    artifact = root / 'web-generation-artifact'
                    artifact.mkdir()
                    dist = root / 'source/dist'
                    dist.mkdir(parents=True)
                    (dist / 'index.html').write_text('generated console')
                    subprocess.run(['tar', '-czf', str(artifact / 'dist.tar.gz'), 'dist'],
                                   cwd=dist.parent, check=True)
                    (artifact / 'manifest.json').write_text(
                        json.dumps(dict(manifest, **overrides)) + '\n')
                    (artifact / 'SHA256SUMS').write_bytes(subprocess.check_output(
                        ['sha256sum', 'dist.tar.gz', 'manifest.json'], cwd=artifact))
                    output = root / 'core/internal/webui'
                    output.mkdir(parents=True)
                    env = dict(os.environ, RUNNER_TEMP=tmp, GITHUB_WORKSPACE=tmp,
                               GITHUB_SHA=head, GITHUB_REPOSITORY=repository,
                               GITHUB_RUN_ID=run_id, GITHUB_RUN_ATTEMPT=runner_attempt,
                               GENERATION_ATTEMPT=attempt)
                    result = subprocess.run(['bash', '-euo', 'pipefail', '-c', textwrap.dedent(embed)],
                                            env=env, capture_output=True, text=True, timeout=10)
                    self.assertEqual(result.returncode == 0, embeds, result.stdout + result.stderr)
                    self.assertEqual((output / 'dist/index.html').exists(), embeds)
                    if attempt == '':
                        self.assertIn('::error::', result.stdout + result.stderr)

    def test_pr_tests_require_the_producers_bundle(self):
        workflow = (ROOT / ".github/workflows/pr-ci.yml").read_text()
        job = workflow.split('  pr-test-shard:\n', 1)[1].split('\n  pr-test:\n', 1)[0]
        self.assertRegex(job, r'(?m)^    env:\n      OLIVARES_REQUIRE_WEB_BUNDLE: "1"$')
        self.assertIn('name: ${{ needs.pr-web.outputs.bundle-artifact }}', job)


class WebBuildTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="web-build-")
        self.addCleanup(self.tmp.cleanup)
        self.repo = Path(self.tmp.name)
        for name in ["Taskfile.yml", "Taskfile.deployment.yml", "scripts/build-web.sh", "scripts/web-bundle-source-digest.sh",
                     "scripts/check-web-bundle-freshness.sh", "scripts/lib/git-env.sh"]:
            source = ROOT / name
            if source.exists():
                target = self.repo / name
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(source, target)
        self.dist = self.repo / "core/internal/webui/dist"
        self.dist.mkdir(parents=True)
        (self.dist / "index.html").write_text("stale committed console")
        (self.repo / "web/src").mkdir(parents=True)
        (self.repo / "web/public").mkdir()
        (self.repo / "web/public/PLACEHOLDER").write_text("placeholder")
        (self.repo / "web/src/main.ts").write_text("new console")
        self.git("init", "-q", "-b", "main")
        self.git("add", ".")
        self.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                 "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")
        fakebin = self.repo / "fake-bin"
        fakebin.mkdir()
        pnpm = fakebin / "pnpm"
        pnpm.write_text('''#!/usr/bin/env python3
import os, pathlib, shutil, sys
root = pathlib.Path.cwd()
mode = os.environ.get('FIXTURE_MODE', 'good')
if sys.argv[1:] == ['--dir', 'web', 'install', '--frozen-lockfile']:
    sys.exit(17 if mode == 'install-failed' else 0)
assert sys.argv[1:] == ['--dir', 'web', 'run', 'build'], sys.argv
with (root / 'build-count').open('a') as count:
    count.write('build\\n')
if mode == 'no-output':
    sys.exit(0)
if mode == 'mutate':
    (root / 'web/src/main.ts').write_text('mutated during build')
if mode == 'failed':
    sys.exit(23)
dist = root / 'core/internal/webui/dist'
if dist.exists():
    shutil.rmtree(dist)
dist.mkdir()
if mode != 'missing-index':
    (dist / 'index.html').write_text('<script src="/assets/app.js"></script>')
if mode != 'missing-assets':
    (dist / 'assets').mkdir()
    (dist / 'assets/app.js').write_text((root / 'web/src/main.ts').read_text())
''')
        pnpm.chmod(0o755)
        self.env = {k: v for k, v in os.environ.items() if not k.startswith(("GIT_", "OLIVARES_"))}
        self.env["PATH"] = str(fakebin) + os.pathsep + os.environ["PATH"]

    def git(self, *args):
        return subprocess.check_output(["git", *args], cwd=self.repo, text=True)

    def build(self, mode="good"):
        return subprocess.run([TASK, "web:check"], cwd=self.repo,
                              env=dict(self.env, FIXTURE_MODE=mode),
                              capture_output=True, text=True, timeout=30)

    def test_source_only_change_builds_once_without_committing_output(self):
        (self.repo / "web/src/main.ts").write_text("source-only PR")
        self.git("add", "web/src/main.ts")
        self.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                 "-c", "commit.gpgsign=false", "commit", "-qm", "console change")
        result = self.build()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.repo / "build-count").read_text(), "build\n")
        self.assertEqual((self.dist / "assets/app.js").read_text(), "source-only PR")
        self.assertNotEqual(self.git("diff", "--", "core/internal/webui/dist"), "")
        fast = subprocess.run(["bash", "scripts/check-web-bundle-freshness.sh"],
                              cwd=self.repo, env=self.env, capture_output=True, text=True)
        self.assertEqual(fast.returncode, 0, fast.stdout + fast.stderr)

    def test_edition_summary_document_is_a_bundle_input(self):
        # web/vite.config.ts quotes docs/editions.md into the bundle, so editing it must
        # change the source digest in the working tree and in a commit; otherwise a bundle
        # built from an older copy keeps a fresh stamp.
        def digest(*ref):
            return subprocess.check_output(["bash", "scripts/web-bundle-source-digest.sh", *ref],
                                           cwd=self.repo, env=self.env, text=True)

        def commit():
            self.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                     "-c", "commit.gpgsign=false", "commit", "-qam", "edition summary")

        doc = self.repo / "docs/editions.md"
        doc.parent.mkdir()
        doc.write_text("- Session cockpit.\n")
        self.git("add", "docs/editions.md")
        commit()
        committed, worktree = digest("HEAD"), digest()
        doc.write_text("- Session cockpit and fleet deployment.\n")
        self.assertNotEqual(digest(), worktree)
        commit()
        self.assertNotEqual(digest("HEAD"), committed)

    def test_golden_path_autobuild_includes_console_and_propagates_failure(self):
        # Execute the real harness up to engine setup, with the real builders.
        # Only pnpm and the Go compiler are stand-ins; no live account is used.
        for name in ["scripts/e2e-golden-path.sh", "scripts/lib/build-bin.sh",
                     "scripts/lib/exec-tmpdir.sh", "scripts/build-ldflags.sh",
                     "scripts/module-catalog-go.sh"]:
            shutil.copyfile(ROOT / name, self.repo / name)
        (self.repo / "RELEASE-VERSION").write_text("26.11\n")
        harness = self.repo / "scripts/e2e-golden-path.sh"
        setup = harness.read_text().split("# A CLEAN DATA DIRECTORY", 1)[0]
        compiler = self.repo / "fake-bin/go"
        compiler.write_text('''#!/bin/sh
set -eu
test -s core/internal/webui/dist/index.html || { echo 'console missing' >&2; exit 91; }
echo compiled >> compiled
printf '%s\\n' "$@" > compiler-args
''')
        compiler.chmod(0o755)
        binary = self.repo / "custom engine"
        for mode in ["good", "failed", "existing"]:
            with self.subTest(mode=mode):
                shutil.rmtree(self.dist)
                self.dist.mkdir()
                (self.dist / "PLACEHOLDER").write_text("compile-only placeholder")
                for name in ["build-count", "compiled", "compiler-args"]:
                    (self.repo / name).unlink(missing_ok=True)
                if mode == "existing":
                    binary.write_text("#!/bin/sh\nexit 0\n")
                    binary.chmod(0o755)
                env = dict(self.env, FIXTURE_MODE=mode,
                           OLIVARES_EXEC_TMPDIR=str(self.repo / "exec-tmp"))
                result = subprocess.run(
                    ["bash", "-c", setup, str(harness), "--bin", str(binary),
                     "--no-model-turn", "--evidence", str(self.repo / "evidence")],
                    cwd=self.repo.parent, env=env, capture_output=True, text=True, timeout=15)
                output = result.stdout + result.stderr
                self.assertEqual(result.returncode, 23 if mode == "failed" else 0, output)
                self.assertEqual((self.repo / "compiled").exists(), mode == "good")
                if mode == "good":
                    self.assertIn("MODULE-CATALOG-NOT-APPLICABLE", output)
                    args = (self.repo / "compiler-args").read_text().splitlines()
                    flags = shlex.split(args[args.index("-ldflags") + 1])
                    self.assertIn("main.version=26.11", flags)
                if mode == "existing":
                    self.assertFalse((self.repo / "build-count").exists())
                else:
                    self.assertEqual((self.repo / "build-count").read_text(), "build\n")

    def test_failed_or_incomplete_build_is_rejected(self):
        for mode in ["install-failed", "failed", "no-output", "missing-index", "missing-assets", "mutate"]:
            with self.subTest(mode=mode):
                result = self.build(mode)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertFalse((self.repo / "core/internal/webui/bundle-source.stamp").exists())

    def test_source_archive_builds_without_git_metadata(self):
        shutil.rmtree(self.repo / ".git")
        result = self.build()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.repo / "build-count").read_text(), "build\n")

    def test_check_rejects_source_change_after_build(self):
        self.assertEqual(self.build().returncode, 0)
        (self.repo / "web/src/main.ts").write_text("newer than the bundle")
        result = subprocess.run(["bash", "scripts/check-web-bundle-freshness.sh", "--built"],
                                cwd=self.repo, env=self.env, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("stale", result.stderr)

    def test_builder_refuses_redirected_output(self):
        outside = self.repo / "outside"
        outside.mkdir()
        (outside / "keep").write_text("preserve")
        shutil.rmtree(self.dist)
        self.dist.symlink_to(outside, target_is_directory=True)
        result = self.build()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((outside / "keep").read_text(), "preserve")


if __name__ == "__main__":
    unittest.main(verbosity=2)
