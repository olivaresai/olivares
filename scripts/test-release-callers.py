#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Exercise local release consumers with a source-built CLI, never a fake client.

Run with OLIVARES_TEST_BINARY=/absolute/path/to/olivares through heavy-job.
"""
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class ReleaseCallers(unittest.TestCase):
    def run_script(
        self, script: str, *args: str, uses_build_version: bool = False
    ) -> subprocess.CompletedProcess:
        binary = str(Path(os.environ['OLIVARES_TEST_BINARY']).resolve())
        with tempfile.TemporaryDirectory() as folder:
            # Record only public version arguments, then execute the real CLI.
            # Even the tampered-feed probe must supply a supported version, though
            # signature verification correctly refuses its bytes before parsing it.
            recorder = Path(folder) / 'versions'
            instrument = Path(folder) / 'olivares'
            instrument.write_text('''#!/bin/bash
previous=""
for argument in "$@"; do
    case "$previous" in
        --version|--current-version|--product-version) printf '%s\\n' "$argument" >> "$VERSION_RECORD" ;;
    esac
    previous="$argument"
done
exec "$REAL_BINARY" "$@"
''')
            instrument.chmod(0o755)
            result = subprocess.run(
                ['bash', str(ROOT / 'scripts' / script), *args, str(instrument)],
                cwd=ROOT, env=dict(os.environ, TMPDIR=folder, REAL_BINARY=binary,
                                   VERSION_RECORD=str(recorder)),
                capture_output=True, text=True, timeout=120,
            )
            versions = recorder.read_text().splitlines()
            if uses_build_version:
                # A stamped client uses its own identity, so the caller correctly
                # omits --current-version. Read that identity from the real CLI's
                # JSON instead of assuming every binary needs the dev override.
                reported = subprocess.run([binary, '-o', 'json', 'version'],
                                          capture_output=True, text=True, timeout=30)
                self.assertEqual(reported.returncode, 0, reported.stderr)
                build_version = json.loads(reported.stdout)['version']
                if build_version != 'dev':
                    versions.append(build_version)
            self.assertGreaterEqual(len(versions), 2, result.stdout + result.stderr)
            for version in versions:
                self.assertIsNotNone(re.fullmatch(r'[0-9]+\.[0-9]+', version), repr(version))
            return result

    def test_security_feed_packages_and_refuses_tamper(self):
        result = self.run_script('package-security-feed.sh', '--selftest', '--olivares')
        output = result.stdout + result.stderr
        self.assertEqual(result.returncode, 0, output)
        self.assertIn('no known advisory', output)
        self.assertIn('did not verify', output)
        self.assertIn('security-feed packaging selftest PASSED', output)

    def test_business_channel_verifies_and_refuses_bad_signatures(self):
        result = self.run_script('verify-business-chain.sh', '--only', 'channel-hermetic', '--binary',
                                 uses_build_version=True)
        output = result.stdout + result.stderr
        self.assertEqual(result.returncode, 0, output)
        for probe in ('valid', 'tampered', 'unsigned', 'wrong-key'):
            self.assertIn('ok   · channel-hermetic/' + probe, output)
        self.assertNotIn('FAIL ·', output)
        self.assertNotIn('SKIPPED', output)


if __name__ == '__main__':
    if not os.environ.get('OLIVARES_TEST_BINARY'):
        raise SystemExit('set OLIVARES_TEST_BINARY to the source-built CLI (no skipped real-binary tests)')
    unittest.main()
