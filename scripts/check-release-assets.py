#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Compare release assets; refuse removals absent from the dated CHANGELOG section."""
import argparse
from datetime import datetime, timezone
import importlib.util
import json
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
# Reuse the release body's exact section parser (the filename contains hyphens).
spec = importlib.util.spec_from_file_location('release_notes', ROOT / 'scripts/render-release-notes.py')
notes_module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(notes_module)


def asset_set(release):
    tag = release['tag_name']
    if not isinstance(tag, str) or not re.fullmatch(r'v?[0-9]+(?:\.[0-9]+){1,2}', tag):
        raise ValueError('invalid release tag')
    names = [asset['name'] for asset in release['assets']]
    if not names or any(not isinstance(n, str) or not n or any(ord(c) < 32 for c in n) for n in names):
        raise ValueError('empty or invalid asset inventory')
    if len(set(names)) != len(names):
        raise ValueError('duplicate asset names')
    version = tag.removeprefix('v')
    # Only a complete version token is normalized, not a number inside an architecture or digest.
    pattern = re.compile(r'(?<![A-Za-z0-9.])' + re.escape(version) + r'(?![A-Za-z0-9]|\.[0-9])')
    normalized = {pattern.sub('{version}', name) for name in names}
    if len(normalized) != len(names):
        raise ValueError('asset names collide after version normalization')
    return normalized


def stated_removals(body):
    names = set()
    section = ''
    fence = None
    for line in body.splitlines():
        marker = re.match(r'^ {0,3}(`{3,}|~{3,})', line)
        if marker:
            run = marker[1]
            if fence is None:
                fence = run
            elif run[0] == fence[0] and len(run) >= len(fence) and not line[marker.end():].strip():
                fence = None
            continue
        if fence is not None:
            continue
        if line.startswith('### '):
            section = line[4:].strip()
        if section in ('Changed', 'Removed') and line.startswith('- Release assets removed: '):
            names.update(re.findall(r'`([^`]+)`', line))
    return names


def compare(previous, current, changelog, phase_one=False):
    before, after = asset_set(previous), asset_set(current)
    if phase_one and {'stable-manifest.json', 'stable-manifest.json.pipeline.sig',
                      'stable-manifest.json.pipeline.pem'} <= after:
        # Phase 2 attaches this signature before its publication verifier runs.
        after.add('stable-manifest.json.sig')
        print('pending Phase-2 asset: stable-manifest.json.sig')
    print(f"asset diff: {previous['tag_name']} -> {current['tag_name']}")
    print('previous normalized assets:\n' + '\n'.join(sorted(before)))
    print('current normalized assets:\n' + '\n'.join(sorted(after)))
    removed = before - after
    for name in sorted(removed):
        print('- ' + name)
    for name in sorted(after - before):
        print('+ ' + name)
    if removed:
        body = notes_module.release_notes(changelog, current['tag_name'])['body']
        undocumented = removed - stated_removals(body)
        if undocumented:
            for name in sorted(undocumented):
                print('undocumented removal: ' + name, file=sys.stderr)
            return 1
    return 0


def gh_json(endpoint, paginate=False):
    command = ['gh', 'api', endpoint]
    if paginate:
        command += ['--paginate', '--slurp']
    result = subprocess.run(command, check=True, capture_output=True, text=True, timeout=120)
    return json.loads(result.stdout)


def previous_release(releases, current):
    # Publication chronology survives the CalVer -> MAJOR.MINOR transition. Skip drafts,
    # prereleases, the candidate itself, and releases published after a historical candidate.
    cutoff = (datetime.fromisoformat(current['published_at']) if current.get('published_at')
              else datetime.now(timezone.utc))
    previous = [r for r in releases if not r['draft'] and not r['prerelease']
                and r['tag_name'] != current['tag_name'] and r['published_at']
                and datetime.fromisoformat(r['published_at']) < cutoff]
    if not previous:
        raise ValueError('no previous published release found; comparison unavailable')
    return max(previous, key=lambda r: datetime.fromisoformat(r['published_at']))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--repo')
    parser.add_argument('--tag')
    parser.add_argument('--previous-json', type=Path)
    parser.add_argument('--current-json', type=Path)
    parser.add_argument('--changelog', type=Path, default=ROOT / 'CHANGELOG.md')
    parser.add_argument('--phase-one', action='store_true', help='account for the pending stable OTA signature')
    args = parser.parse_args()
    try:
        if args.previous_json and args.current_json and not (args.repo or args.tag):
            previous = json.loads(args.previous_json.read_text())
            current = json.loads(args.current_json.read_text())
        elif args.repo and args.tag and not (args.previous_json or args.current_json):
            if not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', args.repo):
                raise ValueError('invalid repository')
            if not re.fullmatch(r'[0-9]+\.[0-9]+', args.tag):
                raise ValueError('invalid candidate tag')
            prefix = f'repos/{args.repo}/releases'
            # The tag endpoint only returns published releases. Drafts require
            # the release list and a token with push access, as in the finalizer.
            pages = gh_json(prefix + '?per_page=100', paginate=True)
            releases = [r for page in pages for r in page]
            candidates = [r for r in releases if r['tag_name'] == args.tag]
            if len(candidates) != 1:
                print(f'release assets: expected one visible candidate, found {len(candidates)}', file=sys.stderr)
                raise ValueError('candidate release missing or ambiguous')
            current = candidates[0]
            previous = previous_release(releases, current)
            # Paginate assets separately; never infer removal from a truncated inventory.
            for release in (previous, current):
                pages = gh_json(f'{prefix}/{release["id"]}/assets?per_page=100', paginate=True)
                release['assets'] = [asset for page in pages for asset in page]
        else:
            raise ValueError('supply --repo and --tag, or both JSON files')
        return compare(previous, current, args.changelog.read_text(), args.phase_one)
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        print(f'release assets: comparison failed ({type(error).__name__})', file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
