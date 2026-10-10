#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Redact credentials from the first-hour engine log before upload."""
import argparse
import json
from pathlib import Path
import re
from urllib.parse import quote


def redact(text, secrets):
    for secret in secrets:
        if secret:
            for value in (secret, quote(secret, safe=''), json.dumps(secret)[1:-1]):
                text = text.replace(value, '[REDACTED]')
    text = re.sub(r'-----BEGIN [^-]*PRIVATE KEY-----.*?(?:-----END [^-]*PRIVATE KEY-----|\Z)',
                  '[REDACTED PRIVATE KEY]', text, flags=re.S)
    text = re.sub(r'\b(?:olst_[\w-]+|sk-[\w-]+|eyJ[\w-]+\.[\w-]+\.[\w-]+)', '[REDACTED]', text)
    text = re.sub(r'(?i)\b(?:Bearer|Basic)\s+[^\s"\',;]+', 'Bearer [REDACTED]', text)
    # A Cookie header has several opaque values separated by semicolons; none
    # can be safely retained. Log lines remain separate diagnostic records.
    text = re.sub(r'''(?im)(\b(?:set-)?cookie["']?\s*[:=]\s*)[^\r\n]*''',
                  r'\1[REDACTED]', text)
    return re.sub(
        r'''(?ix)((?:[\w-]*(?:token|password|secret|credential|key)|csrf|authorization|(?:set-)?cookie)["']?\s*[:=]\s*)
        (?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\s,;]+)''',
        r'\1[REDACTED]', text)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('source', type=Path)
    parser.add_argument('destination', type=Path)
    parser.add_argument('secrets', type=Path)
    args = parser.parse_args()
    secrets = json.loads(args.secrets.read_text())
    result = redact(args.source.read_text(), secrets).encode()
    # Do not publish partial output when parsing or sanitization fails.
    args.destination.write_bytes(result)


if __name__ == '__main__':
    try:
        main()
    except Exception:
        raise SystemExit('first-hour diagnostic sanitization failed; raw evidence was not published') from None
