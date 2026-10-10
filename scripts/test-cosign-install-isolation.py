#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Reproduce concurrent installers sharing a home without downloading Cosign."""
import errno
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent


class CosignInstallIsolationTests(unittest.TestCase):
    def test_pr_installers_execute_while_shared_home_binary_is_being_written(self) -> None:
        workflow = (ROOT / ".github/workflows/pr-ci.yml").read_text()
        steps = re.findall(
            r"^      - uses: sigstore/cosign-installer@[^\n]+\n"
            r"(?:(?!      - ).*\n)*", workflow, re.MULTILINE)
        self.assertEqual(len(steps), 2, "exercise build and userspace installers")
        for step in steps:
            # The checkout supports native builds; the system /tmp may be noexec.
            with self.subTest(step=step), tempfile.TemporaryDirectory(
                    prefix=".cosign-isolation-", dir=ROOT) as tmp:
                root = Path(tmp)
                shared = root / "home/.cosign/cosign"
                shared.parent.mkdir(parents=True)
                # A native executable gives the same ETXTBSY as the real bootstrap.
                shutil.copyfile(shutil.which("true"), shared)
                shared.chmod(0o755)
                configured = re.search(r"^          install-dir: (.+)$", step, re.M)
                directory = configured[1].strip("'\"") if configured else "$HOME/.cosign"
                directory = directory.replace("${{ runner.temp }}", str(root / "runner/temp"))
                directory = directory.replace("$HOME", str(root / "home"))
                binary = Path(directory) / "cosign"
                self.assertTrue(binary.is_relative_to(root), "fixture must stay in its tempdir")
                binary.parent.mkdir(parents=True, exist_ok=True)
                # Model curl holding the shared destination open in another job.
                with shared.open("r+b"):
                    with self.assertRaises(OSError) as error:
                        subprocess.run([str(shared)], check=True, timeout=5)
                    self.assertEqual(error.exception.errno, errno.ETXTBSY)
                    if binary != shared:
                        shutil.copyfile(shutil.which("true"), binary)
                        binary.chmod(0o755)
                    result = subprocess.run(
                        ["bash", "-c", 'exec "$1" version', "fixture", str(binary)],
                        capture_output=True, text=True, timeout=5)
                    self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
