#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Exercise the shared Task installer and its workflow callers without services."""

from pathlib import Path
import hashlib
import io
import os
import subprocess
import sys
import tarfile
import tempfile
import unittest

try:
    import yaml
except ImportError:
    print("Task installation checks require PyYAML; could not inspect workflows.", file=sys.stderr)
    sys.exit(2)

ROOT = Path(__file__).resolve().parents[1]
ACTION = "./.github/actions/olivares-tool-cache"
VERSION = "3.51.1"
ARCHIVE_SHA = "da7e92f0ff961ef2aae7cfecbad8d1fd2a08d7b09ba968673adf7ff389b243b5"
CALLERS = {
    "commit-outcome-oracle.yml": ["generated"],
    "compose-ready.yml": ["qualify-compose-ready"],
    "drills-nightly.yml": ["drills"],
    "export-check.yml": ["pattern-net-static", "acceptance-empirical"],
    "mainline-ci.yml": ["sdk-tests"],
    "pr-ci.yml": ["pr-lint", "pr-build", "pr-test-shard", "pr-web"],
    "race-full.yml": ["race-workspace"],
    "release-rehearsal.yml": ["goreleaser-rehearsal"],
    "release.yml": ["goreleaser"],
}


class WorkflowInstallTest(unittest.TestCase):
    def test_remaining_callers_use_the_verified_release_after_checkout(self):
        action = yaml.safe_load((ROOT / ACTION / "action.yml").read_text())
        self.assertEqual(action["inputs"]["task_version"]["default"], VERSION)
        self.assertEqual(action["inputs"]["task_sha256"]["default"], ARCHIVE_SHA)
        for filename, jobs in CALLERS.items():
            workflow = yaml.safe_load((ROOT / ".github/workflows" / filename).read_text())
            for job in jobs:
                with self.subTest(workflow=filename, job=job):
                    steps = workflow["jobs"][job]["steps"]
                    installs = [i for i, step in enumerate(steps) if step.get("uses") == ACTION]
                    self.assertEqual(len(installs), 1, "Expected one shared Task installer")
                    install = steps[installs[0]]
                    inputs = install.get("with", {})
                    self.assertEqual(inputs.get("task_version", VERSION), VERSION)
                    self.assertEqual(inputs.get("task_sha256", ARCHIVE_SHA), ARCHIVE_SHA)
                    self.assertTrue(any(
                        step.get("uses", "").startswith("actions/checkout@")
                        and step.get("with", {}).get("path", ".") == "."
                        for step in steps[:installs[0]]
                    ), "Local actions require an earlier root checkout")
                    self.assertFalse(install.get("continue-on-error", False))
        for path in sorted((ROOT / ".github/workflows").glob("*.yml")):
            workflow = yaml.safe_load(path.read_text())
            for job in workflow.get("jobs", {}).values():
                for step in job.get("steps", []):
                    commands = "\n".join(
                        line for line in step.get("run", "").splitlines()
                        if not line.lstrip().startswith("#")
                    )
                    self.assertNotIn("go install github.com/go-task/task/", commands, path.name)


class InstallerTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="task-install-", dir=os.environ.get("TMPDIR"))
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.fixture = self.root / "release.tar.gz"
        self.binary = b"#!/bin/sh\nprintf '3.51.1\\n'\n"
        with tarfile.open(self.fixture, "w:gz") as archive:
            member = tarfile.TarInfo("task")
            member.size = len(self.binary)
            member.mode = 0o755
            archive.addfile(member, io.BytesIO(self.binary))
        self.digest = hashlib.sha256(self.fixture.read_bytes()).hexdigest()
        self.path_file = self.root / "github-path"
        self.path_file.touch()
        self.stub_dir = self.root / "commands"
        self.stub_dir.mkdir()
        self.stub("task", "#!/bin/sh\nprintf 'foreign Task\\n'\n")
        # Replace only the network interface. The real installer owns validation,
        # cache publication, extraction, executable permission and PATH publication.
        self.stub("curl", """#!/bin/bash
set -eu
printf 'transfer\n' >> "$TRANSFER_LOG"
if [[ "$TRANSFER_MODE" == fail ]]; then exit 7; fi
while [[ $# -gt 0 ]]; do
  if [[ "$1" == --output ]]; then dest="$2"; shift 2; else shift; fi
done
if [[ "$TRANSFER_MODE" == corrupt ]]; then
  printf 'invalid archive\n' > "$dest"
else
  cp "$FIXTURE_ARCHIVE" "$dest"
fi
""")
        self.env = {
            "PATH": str(self.stub_dir) + ":/usr/bin:/bin",
            "RUNNER_TEMP": str(self.root / "job"),
            "RUNNER_TOOL_CACHE": str(self.root / "cache"),
            "GITHUB_PATH": str(self.path_file),
            "TRANSFER_MODE": "ok",
            "TRANSFER_LOG": str(self.root / "transfers"),
            "FIXTURE_ARCHIVE": str(self.fixture),
        }
        self.cached = self.root / "cache/olivares/task" / VERSION / self.digest / "task_linux_amd64.tar.gz"
        self.installed = self.root / "job/olivares-tool-cache/task" / VERSION / "task"

    def stub(self, name, body):
        path = self.stub_dir / name
        path.write_text(body)
        path.chmod(0o755)

    def install(self, digest=None):
        return subprocess.run(
            ["bash", str(ROOT / ACTION / "install.sh"), "task", VERSION, digest or self.digest],
            env=self.env, capture_output=True, text=True, timeout=15,
        )

    def assert_verified_next_step(self):
        directories = self.path_file.read_text().splitlines()
        self.assertEqual(directories, [str(self.installed.parent)])
        self.assertEqual(self.installed.read_bytes(), self.binary)
        env = {**self.env, "PATH": directories[0] + ":" + self.env["PATH"]}
        result = subprocess.run(
            ["bash", "-c", "command -v task; task --version"],
            env=env, capture_output=True, text=True, timeout=5,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), [str(self.installed), VERSION])

    def test_fresh_install_selects_verified_task_ahead_of_foreign_task(self):
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assert_verified_next_step()

    def test_verified_cache_needs_no_network(self):
        self.cached.parent.mkdir(parents=True)
        self.cached.write_bytes(self.fixture.read_bytes())
        self.env["TRANSFER_MODE"] = "fail"
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(Path(self.env["TRANSFER_LOG"]).exists())
        self.assert_verified_next_step()

    def test_corrupt_cache_is_replaced_before_any_binary_is_selected(self):
        self.cached.parent.mkdir(parents=True)
        altered = self.binary + b"# An altered executable can report the same version.\n"
        with tarfile.open(self.cached, "w:gz") as archive:
            member = tarfile.TarInfo("task")
            member.size = len(altered)
            member.mode = 0o755
            archive.addfile(member, io.BytesIO(altered))
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.cached.read_bytes(), self.fixture.read_bytes())
        self.assertTrue(Path(self.env["TRANSFER_LOG"]).exists())
        self.assert_verified_next_step()

    def test_failed_transfer_or_checksum_never_publishes_a_path(self):
        for mode, reason in [("fail", "download of task"), ("corrupt", "checksum mismatch")]:
            with self.subTest(mode=mode):
                self.env["TRANSFER_MODE"] = mode
                result = self.install()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(reason, result.stdout)
                self.assertEqual(self.path_file.read_text(), "")
                self.assertFalse(self.installed.exists())

    def test_invalid_pin_refuses_before_transfer(self):
        result = self.install("not-a-digest")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(Path(self.env["TRANSFER_LOG"]).exists())
        self.assertEqual(self.path_file.read_text(), "")


if __name__ == "__main__":
    unittest.main(verbosity=2)
