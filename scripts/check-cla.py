#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Pass a maintainer PR by GitHub's author association, otherwise check the trusted base signature record."""
import datetime
import pathlib
import re
import sys


# GitHub sets pull_request.author_association; a contributor cannot choose it.
MAINTAINER_ASSOCIATIONS = {'OWNER', 'MEMBER', 'COLLABORATOR'}


def check(author: str, association: str, path: pathlib.Path) -> int:
    approved = set()
    for number, line in enumerate(path.read_text().splitlines(), 1):
        if line.strip() and not line.startswith('#'):
            fields = line.split()
            if (len(fields) != 3 or not re.fullmatch(r'[A-Za-z0-9-]+', fields[0])
                    or not re.fullmatch(r'\d{4}-\d{2}-\d{2}', fields[1])
                    or fields[2] not in ('HA-CLA-I-1.0', 'HA-CLA-E-1.0')):
                raise ValueError(f'invalid CLA record at line {number}')
            datetime.date.fromisoformat(fields[1])
            approved.add(fields[0].lower())
    if association not in MAINTAINER_ASSOCIATIONS and author.lower() not in approved:
        print(f'No recorded CLA for {author}. Follow CONTRIBUTING.md#dco-sign-off-and-cla; '
              'a maintainer records the signed PDF before this check can pass.', file=sys.stderr)
        return 1
    print(f'CLA: {author} is a maintainer or recorded signer')
    return 0


if __name__ == '__main__':
    try:
        if len(sys.argv) != 4:
            raise ValueError('usage: check-cla.py GITHUB_LOGIN AUTHOR_ASSOCIATION TRUSTED_SIGNATURE_FILE')
        sys.exit(check(sys.argv[1], sys.argv[2], pathlib.Path(sys.argv[3])))
    except (OSError, ValueError) as error:
        print(f'CLA unverified: {error}', file=sys.stderr)
        sys.exit(2)
