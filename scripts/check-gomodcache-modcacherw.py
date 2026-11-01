#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Fail when a workflow puts GOMODCACHE under RUNNER_TEMP without -modcacherw.

Go writes the module cache read-only (directories 0555, files 0444). A runner that
keeps its _temp directory cannot delete such a cache, so the next job reads half-deleted
modules (issue #3655). Every GOMODCACHE assignment under RUNNER_TEMP must therefore
append -modcacherw to GOFLAGS in the lines right after it.

Usage: check-gomodcache-modcacherw.py [WORKFLOW_DIR]
Exit 0: every such assignment carries the flag. Exit 1: the offending file:line list.
"""

import pathlib
import re
import sys

ASSIGNMENT = re.compile(r'echo "GOMODCACHE=[^"]*RUNNER_TEMP[^"]*"')
FLAG = re.compile(r'GOFLAGS=.*-modcacherw')
# The GOFLAGS line sits next to the GOMODCACHE line inside the same GITHUB_ENV block.
WINDOW_LINES = 2


def offenders(workflow_dir):
    found = []
    for path in sorted(pathlib.Path(workflow_dir).glob('*.yml')):
        lines = path.read_text(encoding='utf-8').splitlines()
        for index, line in enumerate(lines):
            if not ASSIGNMENT.search(line):
                continue
            window = lines[index + 1:index + 1 + WINDOW_LINES]
            if not any(FLAG.search(near) for near in window):
                found.append(f'{path}:{index + 1}')
    return found


def main(argv):
    workflow_dir = argv[1] if len(argv) > 1 else '.github/workflows'
    found = offenders(workflow_dir)
    for location in found:
        print(f'{location}: GOMODCACHE under RUNNER_TEMP lacks GOFLAGS -modcacherw', file=sys.stderr)
    return 1 if found else 0


if __name__ == '__main__':
    sys.exit(main(sys.argv))
