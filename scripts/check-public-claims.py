#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Verify source provenance; editorial claims still require source review.

Exit 0: verified; 1: mismatched source; 2: source unavailable or malformed.
No fetch, publication admission, or capability acceptance is performed.
"""
import argparse
import hashlib
import json
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = 'docs/claims/public-claims.v1.json'


class Unavailable(ValueError):
    pass


def git(repo: Path, *args: str) -> bytes:
    return subprocess.check_output(
        ['bash', '-c', '. "$1" || exit 2; shift; exec git -c core.useReplaceRefs=false "$@"',
         'claims', str(ROOT / 'scripts/lib/git-env.sh'), '-C', str(repo), *args],
        stderr=subprocess.PIPE,
    )


def commit(repo: Path, value: str) -> None:
    if not isinstance(value, str) or not re.fullmatch(r'[0-9a-f]{40}', value):
        raise Unavailable('source must name an exact 40-hex commit')
    try:
        resolved = git(repo, 'rev-parse', '--verify', value + '^{commit}').decode().strip()
        if resolved != value:
            raise Unavailable('source object is not a commit')
    except subprocess.CalledProcessError as error:
        raise Unavailable(f'producer/source commit {value} is absent from this repository') from error


def check(repo: Path, producer: str, manifest: dict) -> list[str]:
    commit(repo, producer)
    if manifest.get('schemaVersion') != 'public-claims/v1':
        raise Unavailable('unknown public claims schema')
    measured = manifest['measurement']['refs']['hub']
    commit(repo, measured)
    try:
        git(repo, 'merge-base', '--is-ancestor', measured, producer)
    except subprocess.CalledProcessError as error:
        if error.returncode == 1:
            return ['measured source is not an ancestor of the producer']
        raise Unavailable(f'ancestry query failed (exit {error.returncode})') from error
    errors = []
    for claim in manifest['claims']:
        for anchor in claim['controlRoutes']:
            path, blob = anchor.rsplit('@', 1)
            if not re.fullmatch(r'[0-9a-f]{12,40}', blob):
                raise Unavailable(f'{claim["id"]}: malformed blob anchor')
            entry = git(repo, 'ls-tree', measured, '--', path).decode().strip()
            if not entry or not entry.startswith('100644 blob '):
                errors.append(f'{claim["id"]}: {path} is not a regular source blob')
            elif not entry.split()[2].startswith(blob):
                errors.append(f'{claim["id"]}: {path} does not match the measured blob')
        for test in claim['acceptance']['tests']:
            path = test.split(':', 1)[0]
            if not git(repo, 'ls-tree', measured, '--', path).strip():
                errors.append(f'{claim["id"]}: test source {path} is absent')
    date = manifest['measuredAt']
    if not re.fullmatch(r'\d{4}-\d{2}-\d{2}', date):
        raise Unavailable('invalid measurement date')
    audit = f'docs/claims/measurements/{date}/AUDIT.md'
    audit_bytes = git(repo, 'show', f'{producer}:{audit}')
    if hashlib.sha256(audit_bytes).hexdigest() != manifest['measurement']['auditReportSha256']:
        errors.append('measurement audit digest does not match the producer audit')
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--producer', help='exact committed producer (defaults to HEAD)')
    args = parser.parse_args()
    try:
        producer = args.producer or git(ROOT, 'rev-parse', 'HEAD').decode().strip()
        commit(ROOT, producer)
        manifest = json.loads(git(ROOT, 'show', f'{producer}:{MANIFEST}'))
        errors = check(ROOT, producer, manifest)
    except (Unavailable, KeyError, ValueError, TypeError, AttributeError, OSError, subprocess.CalledProcessError) as error:
        print(f'check-public-claims: UNAVAILABLE: {error}', file=sys.stderr)
        return 2
    if errors:
        print('\n'.join(['check-public-claims: MISMATCH:'] + errors), file=sys.stderr)
        return 1
    print(f'check-public-claims: source verified at producer {producer}')
    return 0


if __name__ == '__main__':
    sys.exit(main())
