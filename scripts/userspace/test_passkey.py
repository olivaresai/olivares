#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Passkey failures and timeouts identify their stage without retaining browser error payloads."""
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

import upgrade
from upgrade import Upgrade

SCRIPT = Path(__file__).with_name('passkey.cjs').resolve()
# Playwright replaced by a stand-in that fails (FAIL_STAGE) or hangs (HANG_STAGE) at one named stage.
HARNESS = r'''
const Module = require('node:module');
const load = Module._load;
const trip = async stage => {
  if (stage === process.env.FAIL_STAGE) throw new Error('private-browser-error');
  if (stage === process.env.HANG_STAGE) await new Promise(() => setInterval(() => {}, 1000));
};
Module._load = function(name, ...args) {
  if (name !== '@playwright/test') return load.call(this, name, ...args);
  return { chromium: { launch: async () => {
    await trip('launch');
    return {
      newPage: async () => {
        await trip('new-page');
        let report;
        return {
          exposeFunction: async (_, fn) => { report = fn; },
          goto: async () => { await trip('login'); },
          context: () => ({ newCDPSession: async () => ({ send: async () => { await trip('cdp'); } }) }),
          evaluate: async () => {
            await report('private-token');
            for (const stage of ['register-options', 'credential-create', 'authenticate-options', 'assertion']) {
              await report(stage);
              await trip(stage);
            }
            return { aal: 3 };
          },
        };
      },
      close: async () => { await trip('browser-close'); },
    };
  } } };
};
require(process.argv[1]);
'''


def fixture_in(directory):
    return Upgrade(SimpleNamespace(output=Path(directory) / 'upgrade', database='sqlite',
                                   published=Path(__file__), candidate=Path(__file__)))


def failures_of(fixture):
    with contextlib.redirect_stdout(io.StringIO()):
        assert fixture.run() == 1
    result = json.loads((fixture.out / 'result.json').read_text())
    assert not result['complete']
    return result['failures']


class PasskeyFailures(unittest.TestCase):
    def test_browser_errors_report_only_the_stage(self):
        for stage in ('launch', 'new-page', 'login', 'cdp', 'register-options', 'assertion', ''):
            with self.subTest(stage=stage), tempfile.TemporaryDirectory() as directory:
                stage_file = Path(directory) / 'stage'
                result = subprocess.run(['node', '-e', HARNESS, str(SCRIPT)],
                                        env=dict(os.environ, FAIL_STAGE=stage),
                                        input=json.dumps({'origin': 'http://localhost', 'token': 'private-token',
                                                          'stageFile': str(stage_file)}),
                                        capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 1 if stage else 0)
                self.assertEqual(result.stderr, '')
                self.assertEqual(result.stdout, '' if stage else '{"aal":3}\n')
                self.assertEqual(stage_file.read_text(), stage or 'browser-close')

    def test_a_hung_ceremony_names_the_stage_it_stopped_at(self):
        popen = subprocess.Popen
        for stage in ('cdp', 'credential-create'):
            with self.subTest(stage=stage), tempfile.TemporaryDirectory() as directory:
                fixture = fixture_in(directory)

                def harnessed(argv, **options):
                    return popen(['node', '-e', HARNESS, argv[1]], **dict(options, env=dict(os.environ, HANG_STAGE=stage)))
                with patch.object(fixture, 'start'), patch.object(fixture, 'login'), \
                        patch('upgrade.subprocess.Popen', harnessed), patch.object(upgrade, 'PASSKEY_SECONDS', 3):
                    self.assertEqual(failures_of(fixture), ['published passkey ceremony timed out at ' + stage])

    def test_upgrade_retains_only_allowlisted_failure_stages(self):
        for stage, expected in [
            ('launch', ' at launch'),
            ('credential-create', ' at credential-create'),
            ('olst_secret', ''),
            ('private token\n', ''),
            ('x' * 200, ''),
            ('', ''),
        ]:
            with self.subTest(stage=stage), tempfile.TemporaryDirectory() as directory:
                fixture = fixture_in(directory)
                ceremony = Mock(returncode=1, pid=12345)

                def communicate(*args, **kwargs):
                    (fixture.home / 'passkey.stage').write_text(stage)
                    return '', ''
                ceremony.communicate.side_effect = communicate
                with patch.object(fixture, 'start'), patch.object(fixture, 'login'), \
                        patch('upgrade.subprocess.Popen', return_value=ceremony), patch('upgrade.os.killpg'):
                    self.assertEqual(failures_of(fixture), ['published passkey ceremony failed' + expected])


if __name__ == '__main__':
    unittest.main()
