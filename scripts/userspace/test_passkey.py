#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Passkey failures identify their stage without retaining browser error payloads."""
import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from upgrade import Upgrade


class PasskeyFailures(unittest.TestCase):
    def test_browser_errors_report_only_the_stage(self):
        script = Path(__file__).with_name('passkey.cjs').resolve()
        harness = r'''
const Module = require('node:module');
const load = Module._load;
const fail = stage => {
  if (stage === process.env.FAIL_STAGE) throw new Error('private-browser-error');
};
Module._load = function(name, ...args) {
  if (name !== '@playwright/test') return load.call(this, name, ...args);
  return { chromium: { launch: async () => {
    fail('browser-launch');
    return {
      newPage: async () => {
        fail('browser-setup');
        return {
          goto: async () => {},
          context: () => ({ newCDPSession: async () => ({ send: async () => {} }) }),
          evaluate: async () => { fail('webauthn'); return { aal: 3 }; },
        };
      },
      close: async () => {},
    };
  } } };
};
require(process.argv[1]);
'''
        for stage in ('browser-launch', 'browser-setup', 'webauthn', ''):
            with self.subTest(stage=stage):
                result = subprocess.run(['node', '-e', harness, str(script)],
                                        env=dict(os.environ, FAIL_STAGE=stage),
                                        input='{"origin":"http://localhost","token":"private-token"}',
                                        capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 1 if stage else 0)
                self.assertEqual(result.stderr, f'WebAuthn upgrade fixture failed at {stage}\n' if stage else '')
                self.assertEqual(result.stdout, '' if stage else '{"aal":3}\n')

    def test_upgrade_retains_only_allowlisted_failure_stages(self):
        for detail, expected in [
            ('WebAuthn upgrade fixture failed at browser-launch\n', ' at browser-launch'),
            ('WebAuthn upgrade fixture failed at webauthn\n', ' at webauthn'),
            ('WebAuthn upgrade fixture failed at private-token\n', ''),
            ('private-browser-error', ''),
        ]:
            with self.subTest(detail=detail), tempfile.TemporaryDirectory() as directory:
                fixture = Upgrade(SimpleNamespace(output=Path(directory) / 'upgrade', database='sqlite',
                                                  published=Path(__file__), candidate=Path(__file__)))
                ceremony = Mock(returncode=1, pid=12345)
                ceremony.communicate.return_value = ('', detail)
                with patch.object(fixture, 'start'), patch.object(fixture, 'login'), \
                        patch('upgrade.subprocess.Popen', return_value=ceremony), \
                        patch('upgrade.os.killpg'), contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(fixture.run(), 1)
                result = json.loads((fixture.out / 'result.json').read_text())
                self.assertFalse(result['complete'])
                self.assertEqual(result['failures'], ['published passkey ceremony failed' + expected])


if __name__ == '__main__':
    unittest.main()
