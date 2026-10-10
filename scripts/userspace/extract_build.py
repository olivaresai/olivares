#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Bind the existing development artifact to the exact Actions candidate."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import tarfile


def extract(archive, destination, expected_sha, expected_head, expected_run, expected_attempt, expected_repository):
    names = {'olivares', 'manifest.json', 'version.json', 'toolchain.txt', 'buildinfo.txt', 'SHA256SUMS'}
    destination.mkdir(mode=0o700, parents=True, exist_ok=False)
    with tarfile.open(archive) as bundle:
        members = bundle.getmembers()
        if len(members) != len(names) or {m.name for m in members} != names or not all(m.isfile() for m in members):
            raise ValueError('candidate archive is not the existing fixed-file build bundle')
        for member in members:
            with bundle.extractfile(member) as source, (destination / member.name).open('xb') as target:
                shutil.copyfileobj(source, target)
    manifest = json.loads((destination / 'manifest.json').read_text())
    if not (manifest['schema'] == 'olivares.cli-development-verification/v1' and
            manifest['repository'] == expected_repository and manifest['checkout_sha'] == expected_sha and
            manifest['pr_head_sha'] == expected_head and manifest['run_id'] == expected_run and
            manifest['run_attempt'] == expected_attempt and
            manifest['binary']['path'] == 'olivares'):
        raise ValueError('candidate artifact identity does not match this PR checkout/build')
    binary = destination / 'olivares'
    if hashlib.sha256(binary.read_bytes()).hexdigest() != manifest['binary']['sha256']:
        raise ValueError('candidate binary digest differs from the build artifact')
    binary.chmod(0o755)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('archive', type=Path)
    parser.add_argument('destination', type=Path)
    args = parser.parse_args()
    event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
    head = event['pull_request']['head']['sha']
    extract(args.archive, args.destination, head, head,
            os.environ['GITHUB_RUN_ID'], os.environ['GITHUB_RUN_ATTEMPT'], os.environ['GITHUB_REPOSITORY'])
