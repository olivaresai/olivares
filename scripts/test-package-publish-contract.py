#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Exercise the publication selftest's child contract without signing or services.

The parent, publisher and its local R2 double are real. Only the two external
package batteries are replaced with fixed output fixtures. This does not qualify
the signatures or native packages exercised by those batteries in official CI.
"""

from pathlib import Path
import os
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
WRONG_KEY = "ok 21 - mutant-wrong-signing-key (rc=1)\n"
SUMMARY = "test-package-repositories: OK — 26 checks; 9/9 mutants red with positive controls\n"


class PublicationChildContractTest(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory(prefix="package-parent-contract-", dir=os.environ.get("TMPDIR"))
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        for name in ("test-package-publish.sh", "publish-package-repositories.sh", "lib/git-env.sh"):
            dest = self.root / "scripts" / name
            dest.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / "scripts" / name, dest)
        (self.root / "scripts/test-package-publish-pacman.sh").write_text(
            "#!/bin/sh\nprintf '%s\\n' 'ok - unsigned_database' 'ok - wrongly_signed_database' "
            "'test-package-publish-pacman: 18/18 cases green'\n"
        )
        # Refuse accidental native/signing/network execution even if a future
        # parent changes its calls. The fixture has no credentials in its env.
        commands = self.root / "commands"
        commands.mkdir()
        for name in ("gpg", "gpgv", "gpgconf", "openssl", "rpm", "rpmbuild", "repo-add", "curl", "wget", "npx"):
            path = commands / name
            path.write_text("#!/bin/sh\nprintf 'forbidden external tool\\n' >&2\nexit 99\n")
            path.chmod(0o755)
        self.env = {"PATH": str(commands) + ":/usr/bin:/bin", "TMPDIR": str(self.root), "LC_ALL": "C"}

    def parent(self, stdout=WRONG_KEY + SUMMARY, stderr="", rc=0):
        child = self.root / "scripts/test-package-repositories.sh"
        child.write_text(
            "#!/bin/sh\ncat <<'STDOUT'\n" + stdout + "STDOUT\n"
            "cat >&2 <<'STDERR'\n" + stderr + "STDERR\nexit " + str(rc) + "\n"
        )
        return subprocess.run(
            ["bash", str(self.root / "scripts/test-package-publish.sh")],
            cwd=self.root, env=self.env, capture_output=True, text=True, timeout=20,
        )

    def test_current_nine_mutants_reach_parent_success(self):
        result = self.parent()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("ok 10 - real wrong-signing-key mutant", result.stdout)
        self.assertIn("test-package-publish: OK — 10 controls", result.stdout)

    def test_legacy_four_mutants_are_refused(self):
        result = self.parent(WRONG_KEY + SUMMARY.replace("9/9", "4/4"))
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("repository child result contract", result.stderr)

    def test_missing_or_unmeasured_wrong_key_is_not_a_refusal(self):
        for control in ("", WRONG_KEY.replace("rc=1", "rc=2")):
            with self.subTest(control=control):
                result = self.parent(control + SUMMARY)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertIn("repository child result contract", result.stderr)

    def test_child_failure_and_unknown_keep_their_code_and_diagnostics(self):
        for rc in (1, 2):
            with self.subTest(rc=rc):
                result = self.parent("synthetic child stdout\n", "synthetic child stderr\n", rc)
                self.assertEqual(result.returncode, rc, result.stdout + result.stderr)
                self.assertIn("repository child failed (rc=" + str(rc) + ")", result.stderr)
                self.assertIn("synthetic child stdout", result.stderr)
                self.assertIn("synthetic child stderr", result.stderr)

    def test_missing_or_inconsistent_summary_is_refused(self):
        for summary in ("", SUMMARY.replace("9/9", "8/9")):
            with self.subTest(summary=summary):
                result = self.parent(WRONG_KEY + summary)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertIn("repository child result contract", result.stderr)


if __name__ == "__main__":
    unittest.main(verbosity=2)
