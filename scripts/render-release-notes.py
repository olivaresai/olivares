#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Render one dated CHANGELOG section for release bodies and web consumers."""
import argparse
from datetime import date
import json
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[1]


def release_notes(source: str, tag: str) -> dict[str, str]:
    """Keep the curated Markdown intact; never fall back to commit history."""
    if not re.fullmatch(r'[0-9]+\.[0-9]+', tag):
        raise ValueError(f'expected a release tag, got {tag!r}')
    version = tag.removeprefix('v')
    lines = source.splitlines(keepends=True)
    headings = []
    fence = None
    for index, line in enumerate(lines):
        marker = re.match(r'^ {0,3}(`{3,}|~{3,})', line)
        if marker:
            run = marker[1]
            if fence is None:
                fence = run
            elif run[0] == fence[0] and len(run) >= len(fence) and not line[marker.end():].strip():
                fence = None
        elif fence is None and line.startswith('## '):
            headings.append(index)
    matches = []
    for position, start in enumerate(headings):
        heading = re.fullmatch(r'## \[([^\]]+)\] - (\d{4}-\d{2}-\d{2})\s*', lines[start])
        if heading and heading[1].removeprefix('v') == version:
            date.fromisoformat(heading[2])
            end = headings[position + 1] if position + 1 < len(headings) else len(lines)
            if not ''.join(lines[start + 1:end]).strip():
                raise ValueError(f'empty CHANGELOG section for {tag}')
            matches.append({'version': tag, 'date': heading[2],
                            'body': ''.join(lines[start:end]).rstrip('\n') + '\n'})
    if len(matches) != 1:
        raise ValueError(f'expected one dated CHANGELOG section for {tag}; found {len(matches)}')
    return matches[0]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('tag', help='exact MAJOR.MINOR release tag')
    parser.add_argument('--changelog', type=Path, default=ROOT / 'CHANGELOG.md')
    parser.add_argument('--format', choices=('markdown', 'json'), default='markdown',
                        help='JSON carries version, date and the same Markdown body for web consumers')
    args = parser.parse_args()
    try:
        notes = release_notes(args.changelog.read_text(encoding='utf-8'), args.tag)
    except (OSError, UnicodeError, ValueError) as error:
        print(f'release notes: {error}', file=sys.stderr)
        return 1
    print(json.dumps(notes, ensure_ascii=False) if args.format == 'json' else notes['body'],
          end='\n' if args.format == 'json' else '')
    return 0


if __name__ == '__main__':
    sys.exit(main())
