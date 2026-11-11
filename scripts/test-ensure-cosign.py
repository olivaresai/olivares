#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Manual network test: release-front failure with the real pinned upstream binary.

Run explicitly with python3 scripts/test-ensure-cosign.py; not part of CI gates.
Requires GitHub release API access (unauthenticated API rate limits apply).
"""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent


class CosignDownloadTest(unittest.TestCase):
    def test_release_api_fallback_and_corrupt_bytes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            shim = root / "bin"
            shim.mkdir()
            curl = shutil.which("curl")
            (shim / "curl").write_text('''#!/usr/bin/env bash
set -euo pipefail
for arg in "$@"; do
  case "$arg" in
    https://github.com/*/releases/download/*) echo 'curl: (22) HTTP 504' >&2; exit 22 ;;
  esac
done
"$REAL_CURL" "$@"
if [[ "${CORRUPT:-}" == 1 ]]; then
  while [[ $# -gt 0 ]]; do
    if [[ "$1" == -o ]]; then printf corrupt > "$2"; break; fi
    shift
  done
fi
''')
            (shim / "curl").chmod(0o755)
            env = dict(os.environ, PATH=f"{shim}:{os.environ['PATH']}", REAL_CURL=curl, input_cosign_release="v2.6.4", input_install_dir=str(root / "good"), input_use_sudo="false", runner_os="Linux", runner_arch="X64", NO_COLOR="1")
            command = ["bash", os.environ.get("TEST_INSTALLER", str(ROOT / "scripts/ensure-cosign.sh")), "--dest", str(root / "good")]
            installed = subprocess.run(command, env=env, text=True, capture_output=True)
            self.assertEqual(installed.returncode, 0, installed.stdout + installed.stderr)
            binary = installed.stdout.strip()
            verified = subprocess.run([binary, "version"], text=True, capture_output=True)
            self.assertEqual(verified.returncode, 0, verified.stderr)
            self.assertIn("v2.6.4", verified.stdout)
            github_env = root / "github-env"
            github_env.touch()
            assertion = subprocess.run(["bash", str(ROOT / "scripts/assert-cosign-binary.sh"), "--isolate"],
                env=dict(env, PATH=f"{root / 'good'}:{env['PATH']}", RUNNER_TEMP=tmp, GITHUB_ENV=str(github_env)),
                text=True, capture_output=True)
            self.assertEqual(assertion.returncode, 0, assertion.stdout + assertion.stderr)
            bad = subprocess.run(command[:-1] + [str(root / "bad")], env=dict(env, CORRUPT="1"), text=True, capture_output=True)
            self.assertNotEqual(bad.returncode, 0)
            self.assertIn("digest", bad.stderr)
            self.assertFalse((root / "bad/cosign").exists())
            print("HTTP 504 -> release API -> pinned cosign v2.6.4; corrupt download refused")


if __name__ == "__main__":
    unittest.main()
