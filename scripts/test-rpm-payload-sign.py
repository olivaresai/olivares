#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Check that a failed key import preserves GnuPG's diagnostic evidence."""

import contextlib
import importlib.util
import io
import shutil
import tempfile
import unittest
from pathlib import Path


spec = importlib.util.spec_from_file_location("rpm_payload_sign", Path(__file__).with_name("rpm-payload-sign.py"))
signer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(signer)


class ImportDiagnostics(unittest.TestCase):
    def test_failed_import_reports_gpg_diagnostics(self):
        gpg = shutil.which("gpg")
        self.assertIsNotNone(gpg, "gpg is required")
        self.assertIsNotNone(shutil.which("gpgconf"), "gpgconf is required")
        with tempfile.TemporaryDirectory(prefix="rpm-import-") as temporary:
            secret = Path(temporary) / "invalid.asc"
            secret.write_text("invalid OpenPGP input\n", encoding="utf-8")
            with signer.gnupg_home() as home:
                output = io.StringIO()
                with contextlib.redirect_stderr(output), self.assertRaises(SystemExit) as failure:
                    signer.import_secret(gpg, home, secret)
                self.assertEqual(failure.exception.code, 2)
                diagnostic = output.getvalue()
                self.assertIn("no valid OpenPGP data found", diagnostic)
                self.assertIn(f"{gpg} --homedir {home} --batch --import {secret}", diagnostic)
                self.assertIn("exit code: 2", diagnostic)
                self.assertIn("--list-dirs", diagnostic)
                self.assertIn(f"homedir:{home}", diagnostic)
                self.assertNotIn(secret.read_text(), diagnostic)


if __name__ == "__main__":
    unittest.main()
