#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Protect the current published release, or an older release at a release cut."""
import argparse
from collections.abc import Iterable
import json
import re
import subprocess
import sys
import urllib.error
import urllib.request

API = 'https://api.github.com/repos/olivaresai/olivares/'
FIRST_STABLE = '1.0'


def version(tag):
    match = re.fullmatch(r'([0-9]+)\.([0-9]+)', tag)
    if not match:
        raise ValueError('not a stable version: ' + tag)
    return tuple(map(int, match.groups()))


def older_tags(tags: Iterable[str], candidate: str, include_current: bool = False) -> list[str]:
    ceiling = version(candidate)
    valid = []
    for tag in tags:
        try:
            parsed = version(tag)
            if parsed < ceiling or (include_current and parsed == ceiling):
                valid.append((parsed, tag))
        except ValueError:
            continue
    return [tag for _, tag in sorted(set(valid), reverse=True)]


def get(path):
    request = urllib.request.Request(API + path, headers={'Accept': 'application/vnd.github+json',
                                                        'X-GitHub-Api-Version': '2022-11-28'})
    with urllib.request.urlopen(request, timeout=30) as response:
        return json.load(response)


def published(release):
    return bool(release.get('published_at') and not release['draft'] and not release['prerelease'])


def resolve(candidate: str, release_cut: bool = False) -> dict | None:
    version(candidate)  # Validate before reading any release metadata.
    if candidate == FIRST_STABLE and release_cut:
        return None  # The first stable release has no predecessor: 26.x has no upgrade path.
    tags = subprocess.run(['git', 'tag', '--list'], capture_output=True, text=True, timeout=30)
    if tags.returncode == 0:
        available = tags.stdout.splitlines()
        if not release_cut:
            # RELEASE-VERSION remains authoritative when local tags are incomplete.
            available.append(candidate)
        for tag in older_tags(available, candidate, include_current=not release_cut):
            try:
                release = get('releases/tags/' + tag)
            except urllib.error.HTTPError as error:
                if error.code == 404:
                    continue
                raise
            if published(release):
                return dict(release, selection='release version and git tags; public release publication verified')
    # Tags unavailable or no eligible published tag: use release metadata, never /latest.
    releases = []
    for page in range(1, 11):
        batch = get('releases?per_page=100&page=' + str(page))
        releases += [r for r in batch if published(r)]
        if len(batch) < 100:
            break
    by_tag = {r['tag_name']: r for r in releases}
    for tag in older_tags(by_tag, candidate, include_current=not release_cut):
        return dict(by_tag[tag], selection='public release API fallback')
    if candidate == FIRST_STABLE:
        return None  # 1.0 is not published yet, so development has no release to protect.
    raise ValueError('no eligible published baseline for version ' + candidate)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--candidate-version', required=True)
    parser.add_argument('--release-cut', action='store_true',
                        help='exclude the candidate version when qualifying its release tag')
    args = parser.parse_args()
    try:
        release = resolve(args.candidate_version, release_cut=args.release_cut)
        if release is None:
            print('userspace baseline: first stable release; no published baseline', file=sys.stderr)
        else:
            print('userspace baseline: ' + release['tag_name'] + ' (' + release['selection'] + ')', file=sys.stderr)
        print(json.dumps(release))
    except (OSError, ValueError, urllib.error.URLError) as error:
        print('baseline unavailable: ' + str(error), file=sys.stderr)
        sys.exit(2)
