#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Both release paths must pin the privileged binfmt image, not only the action."""
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[1]


class QemuImagePin(unittest.TestCase):
    def test_release_paths_pin_binfmt(self):
        for name in ('release.yml', 'release-rehearsal.yml'):
            with self.subTest(workflow=name):
                workflow = ROOT / '.github/workflows' / name
                if name == 'release-rehearsal.yml' and not workflow.exists():
                    continue  # The rehearsal workflow is not exported.
                text = workflow.read_text()
                steps = re.findall(r'^      - uses: docker/setup-qemu-action@[^\n]+\n((?:(?!      - ).*\n)*)', text, re.M)
                self.assertTrue(steps, 'QEMU setup step missing')
                for step in steps:
                    self.assertRegex(step, r'(?m)^        with:\n          image: docker.io/tonistiigi/binfmt@sha256:[0-9a-f]{64}$')


if __name__ == '__main__':
    unittest.main()
