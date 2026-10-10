# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Compare migration content hashes against Git, never a rewritable checksum list."""
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]


def git(*args):
    return subprocess.check_output(['git', *args], cwd=ROOT, stderr=subprocess.PIPE)


def relevant(name):
    parts = Path(name).parts
    return ('testdata' not in parts and 'vendor' not in parts and
            ((name.endswith('.sql') and 'migrations' in parts) or
             (name.endswith('.go') and not name.endswith('_test.go'))))


def sql_hashes(files):
    hashes = {}
    versions = {}
    for name, body in files.items():
        if not name.endswith('.sql'):
            continue
        path = Path(name)
        match = re.match(r'(\d+)_', path.name)
        if not match:
            raise ValueError('migration has no version: ' + name)
        key = (str(path.parent), int(match[1]), 'down' if name.endswith('.down.sql') else 'up')
        if key in versions:
            raise ValueError(f'duplicate migration version: {versions[key]} and {name}')
        versions[key] = name
        hashes[name] = hashlib.sha256(body).hexdigest()
    return hashes


def baseline_files(sha):
    # Read Git blobs directly: git archive honors export-ignore/export-subst,
    # which must never remove or rewrite the comparison's source of truth.
    entries = {}
    for entry in git('ls-tree', '-rz', '--full-tree', sha).split(b'\0'):
        if not entry:
            continue
        header, name = entry.split(b'\t', 1)
        name = name.decode()
        if not relevant(name):
            continue
        mode, kind, oid = header.split()
        if kind != b'blob' or mode == b'120000':
            raise ValueError('baseline migration source is not a regular file: ' + name)
        entries[name] = oid
    ids = sorted(set(entries.values()))
    result = subprocess.run(['git', 'cat-file', '--batch'], cwd=ROOT,
                            input=b'\n'.join(ids) + b'\n', capture_output=True, check=True)
    stream = io.BytesIO(result.stdout)
    blobs = {}
    for oid in ids:
        got, kind, size = stream.readline().split()
        if got != oid or kind != b'blob':
            raise ValueError('could not read baseline blob')
        blobs[oid] = stream.read(int(size))
        if len(blobs[oid]) != int(size) or stream.read(1) != b'\n':
            raise ValueError('truncated baseline blob')
    return {name: blobs[oid] for name, oid in entries.items()}


def main():
    baseline = os.environ.get('OLIVARES_MIGRATION_BASE')
    if not baseline:
        # Local branches compare with their mainline ancestor. Minimal fixture
        # repositories have no remote and compare working files with HEAD.
        refs = git('for-each-ref', '--format=%(refname)', 'refs/remotes/origin/main')
        baseline = git('merge-base', 'HEAD', 'origin/main').decode().strip() if refs.strip() else 'HEAD'
        if refs.strip() and baseline == git('rev-parse', 'HEAD').decode().strip():
            baseline = 'HEAD^'
    sha = git('rev-parse', '--verify', baseline + '^{commit}').decode().strip()
    # A release that starts a fresh-install line (0.1: no upgrade from 26.10.x) declares the epoch in
    # core/migrate/EPOCH.txt. A baseline without that file predates it, so history restarts once; every
    # baseline that carries the file is compared in full again.
    epoch = ROOT / 'core/migrate/EPOCH.txt'
    if epoch.is_file() and subprocess.run(['git', 'cat-file', '-e', f'{sha}:core/migrate/EPOCH.txt'],
                                          cwd=ROOT, capture_output=True).returncode != 0:
        print(f'✓ migration epoch {epoch.read_text().strip()}: baseline {sha[:12]} predates it; history restarts here')
        return 0
    before = baseline_files(sha)
    paths = git('ls-files', '-z', '--cached', '--others', '--exclude-standard').decode().split('\0')
    after = {}
    for name in sorted(set(paths)):
        if name and relevant(name):
            target = ROOT / name
            if not target.exists():
                continue  # A tracked deletion must be compared against the baseline.
            if target.is_symlink():
                raise ValueError('migration source is a symlink: ' + name)
            after[name] = target.read_bytes()
    old, new = sql_hashes(before), sql_hashes(after)
    sources = [{name: body.decode() for name, body in files.items() if name.endswith('.go')}
               for files in (before, after)]
    result = subprocess.run(['go', 'run', str(ROOT / 'scripts/migrationhash/main.go')],
                            input=json.dumps(sources), text=True, capture_output=True, check=True,
                            env={**os.environ, 'GOWORK': 'off', 'GO111MODULE': 'off', 'GOTOOLCHAIN': 'local'})
    old_go, new_go = json.loads(result.stdout)
    old.update(old_go)
    new.update(new_go)
    if not old:
        raise ValueError('baseline contains no migrations')
    changed = [key for key, digest in old.items() if new.get(key) != digest]
    for key in sorted(changed):
        print('migration immutable: ' + key + ' changed or disappeared; add a new version', file=sys.stderr)
    if changed:
        return 1
    print(f'✓ {len(old)} migration content hashes immutable against {sha[:12]}')
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        detail = error.stderr if isinstance(error, subprocess.CalledProcessError) else str(error)
        if isinstance(detail, bytes):
            detail = detail.decode(errors='replace')
        print('migration hashes: UNVERIFIED: ' + detail, file=sys.stderr)
        sys.exit(2)
