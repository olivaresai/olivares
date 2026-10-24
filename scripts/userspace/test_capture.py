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
            # capture's first go env runs in this home: telemetry off, so no child outlives the test.
            (original_home / '.config/go/telemetry').mkdir(parents=True)
            (original_home / '.config/go/telemetry/mode').write_text('off')
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

                    def execute(argv, cwd, child_env, timeout=600):
                        if argv[:2] == ['go', 'test']:
                            homes.append(child_env['HOME'])
                            actual = subprocess.check_output(
                                ['go', 'env', 'GOCACHE'], cwd=root, env=child_env, text=True).strip()
                            self.assertEqual(actual, expected)
                            self.assertNotEqual(child_env['HOME'], str(original_home))
                            self.assertEqual(child_env['XDG_CONFIG_HOME'],
                                             str(Path(child_env['HOME']) / '.config'))
                            raise ProbeComplete
                        return original_execute(argv, cwd, child_env, timeout=timeout)

                    with patch.dict(os.environ, env, clear=True), patch.object(capture, 'execute', execute):
                        for _ in range(2):
                            with self.assertRaises(ProbeComplete):
                                capture.capture(root, root / 'binary', root / 'output.json')
                    self.assertNotEqual(*homes)


    def test_fresh_homes_turn_go_telemetry_off(self) -> None:
        # With telemetry on (mode local), the first go command in a fresh home writes
        # .config/go/telemetry/local, and a telemetry child can still write there while the home
        # is removed (ENOTEMPTY). GOTELEMETRY in the environment does not turn it off.
        class ProbeComplete(Exception):
            pass

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            runner_home = root / 'runner-home'
            (runner_home / '.config/go/telemetry').mkdir(parents=True)
            (runner_home / '.config/go/telemetry/mode').write_text('off')
            env = dict(os.environ, HOME=str(runner_home), GOTOOLCHAIN='local')
            env.pop('XDG_CONFIG_HOME', None)
            modes = []
            original_execute = capture.execute

            def execute(argv, cwd, child_env, timeout=600):
                if argv[:2] == ['go', 'test']:
                    modes.append(subprocess.check_output(
                        ['go', 'env', 'GOTELEMETRY'], cwd=root, env=child_env, text=True).strip())
                    self.assertFalse((Path(child_env['XDG_CONFIG_HOME']) / 'go/telemetry/local').exists())
                    raise ProbeComplete
                return original_execute(argv, cwd, child_env, timeout=timeout)

            with patch.dict(os.environ, env, clear=True), patch.object(capture, 'execute', execute):
                with self.assertRaises(ProbeComplete):
                    capture.capture(root, root / 'binary', root / 'output.json')
            self.assertEqual(modes, ['off'])


class CaptureTimeouts(unittest.TestCase):
    def test_the_cli_ref_compile_gets_a_wider_bound_than_quick_commands(self) -> None:
        class ProbeComplete(Exception):
            pass

        go_env = subprocess.CompletedProcess(
            args=[], returncode=0,
            stdout='{"GOCACHE": "/cache", "GOPATH": "/gopath", "GOMODCACHE": "/gomodcache"}')
        seen = {}

        def execute(argv, cwd, child_env, timeout=None):
            if argv[:2] == ['go', 'env']:
                seen['go_env'] = timeout
                return go_env
            if argv[:2] == ['go', 'test']:
                seen['compile'] = timeout
                raise ProbeComplete
            raise AssertionError('unexpected capture command: ' + ' '.join(argv))

        with tempfile.TemporaryDirectory() as directory, patch.object(capture, 'execute', execute):
            with self.assertRaises(ProbeComplete):
                capture.capture(Path(directory), Path(directory) / 'binary',
                                Path(directory) / 'output.json')
        self.assertEqual(seen.get('compile'), 1800)
        self.assertEqual(seen.get('go_env'), 600)


if __name__ == '__main__':
    unittest.main()
