#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Release notes must come from the exact dated CHANGELOG section."""
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class ReleaseNotes(unittest.TestCase):
    def test_release_workflows_use_curated_notes(self) -> None:
        for name in ('release.yml', 'release-rehearsal.yml'):
            path = ROOT / '.github/workflows' / name
            if not path.exists() and name == 'release-rehearsal.yml':
                continue  # The public export excludes the internal rehearsal.
            source = path.read_text()
            with self.subTest(workflow=name):
                args = next(line.strip().removeprefix('args: ') for line in source.splitlines()
                            if line.strip().startswith('args: release '))
                self.assertTrue('--release-notes' in args, 'GoReleaser does not receive curated notes')
                step = re.search(r'      - name: render release notes from CHANGELOG\n'
                                 r'        run: \|\n((?:          .+\n)+)', source)
                self.assertIsNotNone(step, 'No CHANGELOG extractor is wired')
                self.assertLess(step.start(), source.index('      - name: goreleaser release ('))
                command = '\n'.join(line[10:] for line in step[1].splitlines())
                with tempfile.TemporaryDirectory(dir=os.environ.get('TMPDIR')) as tmp:
                    work = Path(tmp) / 'checkout'
                    (work / 'scripts').mkdir(parents=True)
                    (work / 'scripts/render-release-notes.py').write_bytes(
                        (ROOT / 'scripts/render-release-notes.py').read_bytes())
                    (work / 'RELEASE-VERSION').write_text('# Published release\n26.1001\n')
                    (work / 'CHANGELOG.md').write_text(
                        '## [Unreleased]\n\nNot shipped.\n\n'
                        '## [26.1001] - 2026-10-04\n\n### Before you upgrade\n\nBack up.\n\n'
                        '### Known limits\n\nA limit.\n\n## [26.1000] - 2026-10-01\n\nOld.\n')
                    result = subprocess.run(['bash', '-e', '-o', 'pipefail', '-c', command],
                                            cwd=work, capture_output=True, text=True,
                                            env=dict(os.environ, RUNNER_TEMP=tmp, RELEASE_TAG='26.1001'))
                    self.assertEqual(result.returncode, 0, result.stderr)
                    parsed = shlex.split(args.replace('${{ runner.temp }}', tmp))
                    notes = Path(parsed[parsed.index('--release-notes') + 1]).read_text()
                    self.assertTrue(notes.startswith('## [26.1001] - 2026-10-04\n'))
                    self.assertIn('### Before you upgrade', notes)
                    self.assertIn('### Known limits', notes)
                    self.assertNotIn('## [Unreleased]', notes)
                    self.assertNotIn('## [26.1000]', notes)
                    if name == 'release.yml':
                        missing = subprocess.run(['bash', '-e', '-o', 'pipefail', '-c', command],
                                                 cwd=work, capture_output=True, text=True,
                                                 env=dict(os.environ, RUNNER_TEMP=tmp, RELEASE_TAG='99.100'))
                        self.assertNotEqual(missing.returncode, 0)

    def render(self, source: str, tag: str = '26.1001', *args: str) -> subprocess.CompletedProcess[str]:
        with tempfile.TemporaryDirectory(dir=os.environ.get('TMPDIR')) as tmp:
            changelog = Path(tmp) / 'CHANGELOG.md'
            changelog.write_text(source)
            return subprocess.run(
                [sys.executable, str(ROOT / 'scripts/render-release-notes.py'), tag,
                 '--changelog', str(changelog), *args], capture_output=True, text=True,
            )

    def test_markdown_and_web_json_preserve_the_same_section(self) -> None:
        section = ('## [26.1001] - 2026-10-04\n\n### Known limits\n\n'
                   '- Keep **all** limits and [links](https://example.com).\n'
                   '  This continuation is not another change.\n\n'
                   '```markdown\n## [26.900] - 2026-09-01\n```\n')
        source = ('# Changelog\n\n## [Unreleased]\n\nNot shipped.\n\n' + section
                  + '\n## [26.1000] - 2026-10-01\n\nOlder release.\n')
        markdown = self.render(source)
        self.assertEqual(markdown.returncode, 0, markdown.stderr)
        self.assertEqual(markdown.stdout, section)
        web = self.render(source, '26.1001', '--format', 'json')
        self.assertEqual(web.returncode, 0, web.stderr)
        self.assertEqual(json.loads(web.stdout), {
            'version': '26.1001', 'date': '2026-10-04', 'body': section,
        })

    def test_refuses_patch_and_v_tags_even_with_a_matching_section(self) -> None:
        for tag in ('1.0.1', '26.10.2', 'v1.0'):
            source = '## [' + tag + '] - 2026-10-07\n\nPublished changes.\n'
            self.assertNotEqual(self.render(source, tag).returncode, 0)

    def test_refuses_missing_duplicate_undated_invalid_or_empty_release(self) -> None:
        cases = [
            ('## [Unreleased]\n\nNot shipped.\n', '26.1001'),
            ('## [Unreleased]\n\nNot shipped.\n', 'Unreleased'),
            ('## [26.1001] - 2026-10-04\n\nA\n' * 2, '26.1001'),
            ('## [26.1001]\n\nA\n', '26.1001'),
            ('## [26.1001] - 2026-02-30\n\nA\n', '26.1001'),
            ('## [26.1001] - 2026-10-04\n\n', '26.1001'),
        ]
        for source, tag in cases:
            with self.subTest(source=source, tag=tag):
                result = self.render(source, tag)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, '')
                self.assertIn('release notes:', result.stderr)


if __name__ == '__main__':
    unittest.main(verbosity=2)
