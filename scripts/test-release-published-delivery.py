#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Exercise the finalizer's actual delivery postcondition after a draft is published."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class PublishedDelivery(unittest.TestCase):
    def run_delivery(self, published_tag='26.1100'):
        source = (ROOT / 'scripts/release-finalize-stable.sh').read_text()
        start = source.index('check_published_state() {')
        function = source[start:source.index('\n}\n', start) + 3]
        with tempfile.TemporaryDirectory(prefix='published-delivery-', dir=os.environ.get('TMPDIR')) as tmp:
            work = Path(tmp)
            (work / 'published.json').write_text(json.dumps({'tag_name': published_tag, 'draft': False, 'id': 1}))
            for name in ('stable-manifest.json', 'stable-manifest.json.sig'):
                (work / name).write_text('verified fixture bytes\n')
            curl = work / 'curl'
            curl.write_text('''#!/usr/bin/env bash
set -eu
output=''
while [ "$#" -gt 0 ]; do
    case "$1" in --output) output="$2"; shift ;; https:*) url="$1" ;; esac
    shift
done
printf '%s\n' "$url" >> "$WORK/requests"
case "$url" in https://github.com/example/fixture/releases/download/26.1100/*) ;; *) exit 22 ;; esac
cp "$WORK/${url##*/}" "$output"
''')
            curl.chmod(0o755)
            script = '''set -eu
DL="$WORK"; CURL_BIN="$WORK/curl"; JQ_BIN=jq; MAKE_LATEST=false
REPOSITORY=example/fixture; RELEASE_TAG=26.1100
_say() { :; }
asset_url_of() { printf 'https://github.com/example/fixture/releases/download/untagged-draft/%s' "$1"; }
sha_of() { sha256sum "$1" | cut -d' ' -f1; }
postcondition_failed() { printf '%s\n' "$*" >&2; exit 4; }
''' + function + '\ncheck_published_state "$WORK/published.json"\n'
            result = subprocess.run(['bash', '-c', script], env=dict(os.environ, WORK=str(work)), text=True, capture_output=True)
            requests = (work / 'requests').read_text().splitlines() if (work / 'requests').exists() else []
            return result, requests

    def test_published_tag_replaces_draft_asset_url(self):
        result, requests = self.run_delivery()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(requests, [f'https://github.com/example/fixture/releases/download/26.1100/{name}'
                                   for name in ('stable-manifest.json', 'stable-manifest.json.sig')])

    def test_changed_published_tag_refuses_before_delivery(self):
        result, requests = self.run_delivery('26.1200')
        self.assertEqual(result.returncode, 4)
        self.assertEqual(requests, [])


if __name__ == '__main__':
    unittest.main(verbosity=2)
