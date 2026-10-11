#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Exercise the actual CI setup shell without installing packages or browsers."""
import os
from pathlib import Path
import re
import subprocess
import tempfile
import textwrap
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from upgrade import Upgrade


class BrowserSetup(unittest.TestCase):
    def test_runner_setup_and_install_failure(self) -> None:
        for workflow, job in [('pr-ci.yml', 'userspace-compat'), ('integration-check.yml', 'integration-check')]:
            with self.subTest(workflow=workflow):
                self.check_setup(workflow, job)

    def check_setup(self, workflow: str, job: str) -> None:
        text = (Path(__file__).resolve().parents[2] / '.github/workflows' / workflow).read_text()
        step = text.split(f'  {job}:\n', 1)[1].split('      - name: install the existing browser test tools\n', 1)[1]
        script = textwrap.dedent(re.split(r'\n      - ', step, maxsplit=1)[0].split('        run: |\n', 1)[1])
        for runner, install_rc in [('self-hosted', 0), ('github-hosted', 0),
                                   ('self-hosted', 19), ('github-hosted', 19)]:
            with self.subTest(runner=runner, install_rc=install_rc), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                pnpm = root / 'pnpm'
                pnpm.write_text(r'''#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >> "$CALLS"
case "$*" in
  '--dir web install --frozen-lockfile') exit 0 ;;
  '--dir web exec playwright install'*'chromium')
    if [[ "$RUNNER_ENVIRONMENT" == self-hosted && "$*" == *--with-deps* ]]; then
      echo 'sudo: a password is required' >&2
      exit 1
    fi
    printf '%s\n' "${PLAYWRIGHT_BROWSERS_PATH:-$HOME/.cache/ms-playwright}" > "$BROWSER_CACHE"
    exit "$INSTALL_RC" ;;
  *) exit 99 ;;
esac
''')
                pnpm.chmod(0o755)
                env_file = root / 'env'
                calls = root / 'calls'
                browser_cache = root / 'browser-cache'
                env = dict(os.environ, PATH=f'{root}:{os.environ["PATH"]}',
                           RUNNER_ENVIRONMENT=runner, INSTALL_RC=str(install_rc),
                           GITHUB_ENV=str(env_file), CALLS=str(calls),
                           RUNNER_TEMP=str(root / 'runner-temp'), HOME=str(root / 'installer-home'),
                           BROWSER_CACHE=str(browser_cache))
                # A developer's shared cache must not conceal the CI HOME mismatch.
                env.pop('PLAYWRIGHT_BROWSERS_PATH', None)
                result = subprocess.run(['bash', '-e', '-o', 'pipefail', '-c', script],
                                        cwd=root, env=env, capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, install_rc, result.stderr)
                deps = ' --with-deps' if runner == 'github-hosted' else ''
                self.assertEqual(calls.read_text().splitlines(), [
                    '--dir web install --frozen-lockfile',
                    f'--dir web exec playwright install{deps} chromium'])
                if install_rc == 0:
                    exported = dict(line.split('=', 1) for line in env_file.read_text().splitlines())
                    self.assertEqual(exported['NODE_PATH'], f'{root}/web/node_modules')
                    with patch.dict(os.environ, env | exported, clear=True):
                        fixture = Upgrade(SimpleNamespace(output=root / 'upgrade', database='sqlite',
                                                          published=pnpm, candidate=pnpm))
                    self.assertNotEqual(fixture.env['HOME'], env['HOME'])
                    lookup = fixture.env.get('PLAYWRIGHT_BROWSERS_PATH',
                                             fixture.env['HOME'] + '/.cache/ms-playwright')
                    self.assertEqual(lookup, browser_cache.read_text().strip(),
                                     'upgrade must find the browser installed before HOME changed')
                else:
                    self.assertFalse(env_file.exists(), 'failed setup must not publish NODE_PATH')


if __name__ == '__main__':
    unittest.main()
