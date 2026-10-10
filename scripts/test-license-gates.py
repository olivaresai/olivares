#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Behavior tests for contributor and distribution license gates."""
import importlib.util
import pathlib
import subprocess
import sys
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]


class CLATests(unittest.TestCase):
    def check(self, author: str, association: str, records: str) -> subprocess.CompletedProcess[str]:
        with tempfile.TemporaryDirectory() as tmp:
            record = pathlib.Path(tmp) / 'signatures'
            record.write_text(records)
            return subprocess.run(
                [sys.executable, str(ROOT / 'scripts/check-cla.py'), author, association, str(record)],
                capture_output=True, text=True,
            )

    def test_maintainer_pull_request_passes_from_github_association(self) -> None:
        for association in ('OWNER', 'MEMBER', 'COLLABORATOR'):
            self.assertEqual(self.check('maintainer', association, '').returncode, 0)

    def test_unknown_outside_author_is_rejected_with_signing_steps(self) -> None:
        # A name in a comment header grants nothing: only GitHub's association or a signer record does.
        for association in ('CONTRIBUTOR', 'FIRST_TIME_CONTRIBUTOR', 'FIRST_TIMER', 'NONE'):
            result = self.check('outside', association, '# Maintainers: outside\n')
            self.assertEqual(result.returncode, 1)
            self.assertIn('CONTRIBUTING.md#dco-sign-off-and-cla', result.stderr)

    def test_recorded_signer_passes_case_insensitively(self) -> None:
        self.assertEqual(self.check('SIGNER', 'CONTRIBUTOR', 'signer 2026-10-05 HA-CLA-I-1.0\n').returncode, 0)

    def test_malformed_record_does_not_grant_access(self) -> None:
        for record in ('outside\n', 'outside 2026-02-30 HA-CLA-I-1.0\n',
                       'outside 2026-10-05 unknown\n'):
            self.assertNotEqual(self.check('outside', 'NONE', record).returncode, 0)

    def test_signature_record_names_no_maintainer(self) -> None:
        # The export leak gate refuses a maintainer handle in a published file.
        self.assertNotIn('# Maintainers:', (ROOT / '.github/CLA-SIGNATURES').read_text())

    def test_only_public_non_bot_pull_requests_run_the_check(self) -> None:
        # Dependabot cannot sign, and the record format cannot hold a [bot] login.
        self.assertEqual(self.check('dependabot[bot]', 'NONE', '').returncode, 1)
        workflow = (ROOT / '.github/workflows/cla.yml').read_text()
        self.assertRegex(
            workflow,
            r"\n  cla:\n(?:    #.*\n)*    if: github\.repository == 'olivaresai/olivares'"
            r" && github\.event\.pull_request\.user\.type != 'Bot'\n",
        )
        self.assertIn('github.event.pull_request.author_association', workflow)


class LicenseTests(unittest.TestCase):
    def setUp(self) -> None:
        spec = importlib.util.spec_from_file_location('license_gate', ROOT / 'scripts/license-gate.py')
        self.gate = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.gate)

    def test_only_approved_licenses_pass(self) -> None:
        for name in ('MIT', 'BSD-2-Clause', 'BSD-3-Clause', 'ISC', 'Apache-2.0', 'MPL-2.0'):
            self.gate.check_license('example.test/dep', name)
        for name in ('GPL-3.0', 'AGPL-3.0', 'LGPL-2.1', 'SSPL-1.0', 'BSL-1.1',
                     'Elastic-2.0', 'Unknown', '', 'MIT,GPL-3.0'):
            with self.assertRaises(ValueError, msg=name):
                self.gate.check_license('example.test/dep', name)

    def test_notice_includes_license_and_apache_notice(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            (root / 'LICENSE').write_text('Apache license text\n')
            (root / 'NOTICE').write_text('Upstream attribution must survive\n')
            (root / 'sub').mkdir()
            (root / 'sub/NOTICE').write_text('Package-specific attribution\n')
            text = self.gate.dependency_notice('example.test/dep', 'v1.0.0', root / 'LICENSE', root)
            self.assertIn('Apache license text', text)
            self.assertIn('Upstream attribution must survive', text)
            self.assertIn('example.test/dep v1.0.0', text)
            self.assertIn('Package-specific attribution', text)
            (root / 'sub/NOTICE').unlink()
            (root / 'sub/NOTICE').symlink_to('/etc/hosts')
            with self.assertRaises(ValueError):
                self.gate.dependency_notice('example.test/dep', 'v1.0.0', root / 'LICENSE', root)

    def test_mpl_replacement_or_vendor_is_rejected(self) -> None:
        for module in ({'Path': 'example.test/dep', 'Replace': {'Dir': '/tmp/patch'}},
                       {'Path': 'example.test/dep', 'Main': True},
                       {'Path': 'example.test/dep', 'Dir': '/src/vendor/example.test/dep'}):
            with self.assertRaises(ValueError):
                self.gate.check_mpl(module)

    def test_empty_report_fails_closed(self) -> None:
        with self.assertRaises(ValueError):
            self.gate.parse_report('')


if __name__ == '__main__':
    unittest.main()
