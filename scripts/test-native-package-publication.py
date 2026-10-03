#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Exercise draft admission and the complete signed checksum upload, without writes."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class NativePublication(unittest.TestCase):
    def run_publication(self, scenario):
        with tempfile.TemporaryDirectory(prefix='native-publication-', dir=os.environ.get('TMPDIR')) as tmp:
            root = Path(tmp)
            (root / 'scripts').mkdir()
            (root / 'bin').mkdir()
            (root / 'dist').mkdir()
            (root / 'scripts/build-native-release-packages.py').write_text('from pathlib import Path\nPath("built").touch()\n')
            (root / 'scripts/cosign-verified.sh').write_text('touch signed\n')
            gh = root / 'bin/gh'
            gh.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
root=pathlib.Path(os.environ['OLIVARES_ROOT'])
if sys.argv[1] == 'api':
    route=sys.argv[-1]
    if '/releases/tags/' in route:
        sys.exit(1)  # GitHub does not resolve a draft through the published-tag route.
    reads=int((root/'reads').read_text()) if (root/'reads').exists() else 0
    (root/'reads').write_text(str(reads+1))
    scenario=os.environ['SCENARIO']
    release={'id': 2 if scenario == 'replaced' and reads else 1,
        'tag_name': '26.11', 'draft': scenario != 'published' and not (scenario == 'published-later' and reads), 'prerelease': False}
    if '?per_page=' in route:
        print(json.dumps([dict(release, id=9, tag_name='26.10.0', draft=False)]))
        print(json.dumps([release, release] if scenario == 'duplicated' else [release]))
    else:
        print(json.dumps(release))
else:
    (root/'upload.json').write_text(json.dumps(sys.argv[1:]))
''')
            gh.chmod(0o755)
            env = dict(os.environ, OLIVARES_ROOT=str(root), PATH=str(root / 'bin') + os.pathsep + os.environ['PATH'],
                       RELEASE_GITHUB_OWNER='example', RELEASE_GITHUB_NAME='fixture', RELEASE_TAG='26.11', RELEASE_VERSION='26.11', SCENARIO=scenario)
            result = subprocess.run(['bash', str(ROOT / 'scripts/publish-native-release-packages.sh')], env=env, text=True, capture_output=True)
            return result, (root / 'built').exists(), (root / 'signed').exists(), json.loads((root / 'upload.json').read_text()) if (root / 'upload.json').exists() else None

    def test_complete_draft_upload_after_checksum_signature(self):
        result, built, signed, upload = self.run_publication('healthy')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(built and signed)
        self.assertEqual(upload[:6], ['release', 'upload', '26.11', '--repo', 'example/fixture', '--clobber'])
        self.assertEqual(len(upload[6:]), 10)
        self.assertIn('dist/checksums.txt.pem', upload)
        self.assertIn('dist/olivares_26.11_linux_amd64.pkg.tar.zst', upload)

    def test_published_release_refuses_before_build(self):
        result, built, signed, upload = self.run_publication('published')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(built or signed)
        self.assertIsNone(upload)

    def test_draft_replaced_or_published_before_upload_refuses(self):
        for scenario in ('replaced', 'published-later', 'duplicated'):
            with self.subTest(scenario=scenario):
                result, _, _, upload = self.run_publication(scenario)
                self.assertNotEqual(result.returncode, 0)
                self.assertIsNone(upload)


if __name__ == '__main__':
    unittest.main(verbosity=2)
