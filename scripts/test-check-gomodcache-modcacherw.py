#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Prove check-gomodcache-modcacherw.py flags a GOMODCACHE under RUNNER_TEMP without -modcacherw."""

import importlib.util
from pathlib import Path
import tempfile
import unittest

CHECK = Path(__file__).resolve().parent / "check-gomodcache-modcacherw.py"
spec = importlib.util.spec_from_file_location("check_gomodcache_modcacherw", CHECK)
check = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check)

WITH_FLAG = """\
run: |
  {
    echo "GOMODCACHE=${GOMODCACHE:-$RUNNER_TEMP/gomodcache}"
    echo "GOFLAGS=${GOFLAGS:+$GOFLAGS }-modcacherw"
    echo "GOPATH=$RUNNER_TEMP/gopath"
  } >> "$GITHUB_ENV"
"""

WITHOUT_FLAG = """\
run: |
  {
    echo "GOMODCACHE=${GOMODCACHE:-$RUNNER_TEMP/gomodcache}"
    echo "GOPATH=$RUNNER_TEMP/gopath"
  } >> "$GITHUB_ENV"
"""

NOT_RUNNER_TEMP = """\
run: |
  echo "GOMODCACHE=$HOME/go/pkg/mod" >> "$GITHUB_ENV"
"""


class GomodcacheModcacherwTests(unittest.TestCase):
    def offenders_for(self, body):
        with tempfile.TemporaryDirectory() as directory:
            Path(directory, "job.yml").write_text(body, encoding="utf-8")
            return check.offenders(directory)

    def test_flag_next_to_cache_passes(self):
        self.assertEqual(self.offenders_for(WITH_FLAG), [])

    def test_cache_under_runner_temp_without_flag_is_reported_at_its_line(self):
        found = self.offenders_for(WITHOUT_FLAG)
        self.assertEqual(len(found), 1)
        self.assertTrue(found[0].endswith("job.yml:3"), found)

    def test_cache_outside_runner_temp_is_not_judged(self):
        self.assertEqual(self.offenders_for(NOT_RUNNER_TEMP), [])


if __name__ == "__main__":
    unittest.main()
