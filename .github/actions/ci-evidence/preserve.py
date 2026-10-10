#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Copy only the upload's visible files; never follow links to other runner data."""

import os
from pathlib import Path
import re
import shutil


def slug(value: str) -> str:
    return re.sub(r'[^A-Za-z0-9_-]+', '-', value).strip('-') or 'evidence'


def preserve(paths: str, destination: Path) -> int:
    files = set()
    roots = []
    for pattern in paths.splitlines():
        pattern = pattern.strip()
        if not pattern:
            continue
        prefix = re.split(r'[*?\[]', pattern, maxsplit=1)[0]
        root = Path(prefix).absolute()
        roots.append(root if root.is_dir() else root.parent)
        absolute = Path(pattern).absolute()
        for source in Path(absolute.anchor).glob(str(absolute.relative_to(absolute.anchor))):
            candidates = source.rglob('*') if source.is_dir() else [source]
            for file in candidates:
                # Match upload-artifact's default include-hidden-files: false.
                if (file.is_file() and not any(part.startswith('.') for part in file.parts) and
                        not any(p.is_symlink() for p in (file, *file.parents))):
                    files.add(file)
    destination.mkdir(parents=True, exist_ok=True, mode=0o700)
    if not files:
        print('::warning::No evidence files found for the persistent runner copy')
        return 0
    base = Path(os.path.commonpath([str(root) for root in roots]))
    for file in sorted(files):
        target = destination / file.relative_to(base)
        target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        shutil.copyfile(file, target)
        target.chmod(0o600)
    return len(files)


def main() -> None:
    os.umask(0o077)
    # workflow_ref supplies the filename, even when the display name has spaces.
    workflow = os.environ['GITHUB_WORKFLOW_REF'].split('@', 1)[0].rsplit('/', 1)[-1]
    destination = (Path.home() / 'ci-evidence' / slug(Path(workflow).stem) /
                   f"{os.environ['GITHUB_RUN_ID']}-{os.environ['GITHUB_RUN_ATTEMPT']}" /
                   slug(os.environ['GITHUB_JOB']) / slug(os.environ['EVIDENCE_NAME']))
    print(f'Persistent CI evidence: {destination}', flush=True)
    with open(os.environ['GITHUB_STEP_SUMMARY'], 'a', encoding='utf-8') as summary:
        summary.write(f'\nPersistent CI evidence on this runner: `{destination}`\n')
    count = preserve(os.environ['EVIDENCE_PATHS'], destination)
    print(f'Preserved {count} evidence files')


if __name__ == '__main__':
    main()
