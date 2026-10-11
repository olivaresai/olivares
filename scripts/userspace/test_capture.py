#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Capture isolates product settings without discarding Go's build cache."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import capture


class CaptureEnvironment(unittest.TestCase):
    def test_fresh_homes_reuse_the_resolved_go_cache(self) -> None:
        class ProbeComplete(Exception):
            pass

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            original_home = root / 'runner-home'
            original_home.mkdir()
            for explicit in (False, True):
                with self.subTest(explicit=explicit):
                    env = dict(os.environ, HOME=str(original_home), GOTOOLCHAIN='local')
                    env.pop('GOCACHE', None)
                    if explicit:
                        env['GOCACHE'] = str(root / 'assigned-cache')
                    expected = subprocess.check_output(
                        ['go', 'env', 'GOCACHE'], cwd=root, env=env, text=True).strip()
                    homes = []
                    original_execute = capture.execute

                    def execute(argv, cwd, child_env):
                        if argv[:2] == ['go', 'test']:
                            homes.append(child_env['HOME'])
                            actual = subprocess.check_output(
                                ['go', 'env', 'GOCACHE'], cwd=root, env=child_env, text=True).strip()
                            self.assertEqual(actual, expected)
                            self.assertNotEqual(child_env['HOME'], str(original_home))
                            self.assertEqual(child_env['XDG_CONFIG_HOME'],
                                             str(Path(child_env['HOME']) / '.config'))
                            raise ProbeComplete
                        return original_execute(argv, cwd, child_env)

                    with patch.dict(os.environ, env, clear=True), patch.object(capture, 'execute', execute):
                        for _ in range(2):
                            with self.assertRaises(ProbeComplete):
                                capture.capture(root, root / 'binary', root / 'output.json')
                    self.assertNotEqual(*homes)


if __name__ == '__main__':
    unittest.main()
