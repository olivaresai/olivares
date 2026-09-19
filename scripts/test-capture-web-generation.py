#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Filesystem/git qualification with fake task, Node and pnpm; no real build."""

import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
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
                        PR_HEAD="b" * 40, GITHUB_REPOSITORY="olivaresai/olivares",
                        GITHUB_RUN_ID="123", GITHUB_RUN_ATTEMPT="2")
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
if fault == 'archive':
    with patch.object(tarfile, 'open', side_effect=OSError('fixture archive failure')):
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
        self.assertEqual(manifest["pr_head_sha"], "b" * 40)
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
        self.env["GITHUB_SHA"] = "c" * 40
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
        self.env["GITHUB_SHA"] = self.git("rev-parse", "HEAD").strip()
        self.refuse(self.capture())
        self.assertTrue((self.repo / DIST / "assets/old.js").exists())

    def test_ignored_vite_environment_is_not_an_unrecorded_input(self):
        self.write(".git/info/exclude", ".env.local\n")
        self.write("web/.env.local", "VITE_FIXTURE=untracked\n")
        self.refuse(self.capture())
        self.assertTrue((self.repo / DIST / "assets/old.js").exists())


if __name__ == "__main__":
    unittest.main(verbosity=2)
