#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Exercise the real runner's readiness gate without building the console."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class VisualRunnerTest(unittest.TestCase):
    def run_runner(self, mode, playwright_rc=0):
        with tempfile.TemporaryDirectory() as tmp:
            work = Path(tmp)
            commands = work / "bin"
            commands.mkdir()
            shims = {
                "pnpm": '''#!/bin/bash
case "$*" in
  *"vite build"*)
    while [ "$#" -gt 0 ]; do
      if [ "$1" = --outDir ]; then echo "$2" > "$TEST_WORK/out-dir"; break; fi
      shift
    done
    if [ "$TEST_MODE" = late-occupied ]; then touch "$TEST_WORK/foreign"; fi ;;
  *"vite preview"*)
    echo 'preview diagnostic: test server'
    echo $$ > "$TEST_WORK/preview.pid"
    case "$*" in *"--host 127.0.0.1"*) ;; *) exit 8 ;; esac
    if [ "$TEST_MODE" = dead ]; then exit 9; fi
    exec /bin/sleep 60 ;;
  *"playwright test"*)
    printf '%s\\n' "$@" > "$TEST_WORK/playwright.args"
    exit "$TEST_PLAYWRIGHT_RC" ;;
esac
''',
                "curl": '''#!/bin/bash
if [ "$TEST_MODE" = occupied ] || [ -f "$TEST_WORK/foreign" ]; then
  echo 'foreign build'
  exit 0
fi
if [ "$TEST_MODE" = ready ] && [ -f "$TEST_WORK/preview.pid" ]; then
  case "$*" in
    *"visual-e2e-ready.txt"*) cat "$(cat "$TEST_WORK/out-dir")/visual-e2e-ready.txt" ;;
    *) exit 0 ;;
  esac
else
  exit 22
fi
''',
                "sleep": '#!/bin/bash\n/bin/sleep 0.01\n',
            }
            for name, source in shims.items():
                path = commands / name
                path.write_text(source)
                path.chmod(0o755)
            env = dict(os.environ, PATH=f"{commands}:{os.environ['PATH']}",
                       TMPDIR=tmp, TEST_WORK=tmp, TEST_MODE=mode,
                       TEST_PLAYWRIGHT_RC=str(playwright_rc))
            result = subprocess.run(
                ["bash", str(ROOT / "scripts/web-visual-e2e.sh"),
                 "--grep", "source mode"], env=env,
                capture_output=True, text=True, timeout=10)
            args = work / "playwright.args"
            invoked = args.read_text() if args.exists() else None
            pid_file = work / "preview.pid"
            # The runner's EXIT trap must stop its preview, including on failure.
            if pid_file.exists():
                with self.assertRaises(ProcessLookupError):
                    os.kill(int(pid_file.read_text()), 0)
            expected = ["bin"]
            if (work / "foreign").exists():
                expected.append("foreign")
            if (work / "out-dir").exists():
                expected.append("out-dir")
            if invoked:
                expected.append("playwright.args")
            if pid_file.exists():
                expected.append("preview.pid")
            self.assertEqual(sorted(p.name for p in work.iterdir()), expected)
            if mode == "occupied":
                self.assertFalse(pid_file.exists(), "preview started on an occupied port")
            return result, invoked

    def test_existing_listener_refuses_preview_and_playwright(self):
        result, invoked = self.run_runner("occupied")
        self.assertNotEqual(result.returncode, 0)
        self.assertIsNone(invoked)
        self.assertIn("already has a listener", result.stderr)

    def test_listener_appearing_during_build_cannot_satisfy_readiness(self):
        result, invoked = self.run_runner("late-occupied")
        self.assertNotEqual(result.returncode, 0)
        self.assertIsNone(invoked)
        self.assertIn("preview diagnostic: test server", result.stderr)

    def test_dead_preview_refuses_playwright_and_prints_log(self):
        result, invoked = self.run_runner("dead")
        self.assertNotEqual(result.returncode, 0)
        self.assertIsNone(invoked)
        self.assertIn("preview diagnostic: test server", result.stderr)

    def test_unready_preview_refuses_playwright_and_prints_log(self):
        result, invoked = self.run_runner("unready")
        self.assertNotEqual(result.returncode, 0)
        self.assertIsNone(invoked)
        self.assertIn("preview diagnostic: test server", result.stderr)

    def test_ready_preview_forwards_arguments_and_playwright_status(self):
        for rc in [0, 7]:
            with self.subTest(playwright_rc=rc):
                result, invoked = self.run_runner("ready", rc)
                self.assertEqual(result.returncode, rc, result.stderr)
                self.assertIn("management-views.spec.ts", invoked)
                self.assertIn("--grep\nsource mode\n", invoked)


if __name__ == "__main__":
    unittest.main()
