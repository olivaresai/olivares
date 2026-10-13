#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Release asset removals need an explicit entry in the current CHANGELOG section."""
from contextlib import redirect_stdout, redirect_stderr
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / 'scripts/check-release-assets.py'


def release(tag, names):
    return {'tag_name': tag, 'assets': [{'name': n} for n in names]}


class AssetCheck(unittest.TestCase):
    def run_check(self, previous, current, notes='', *extra):
        with tempfile.TemporaryDirectory(dir=os.environ.get('TMPDIR')) as tmp:
            root = Path(tmp)
            for name, data in [('previous', previous), ('current', current)]:
                (root / (name + '.json')).write_text(json.dumps(data))
            (root / 'CHANGELOG.md').write_text('## [0.1] - 2026-10-10\n\n' + notes + '\n')
            return subprocess.run([sys.executable, '-B', str(SCRIPT), '--previous-json',
                                   str(root / 'previous.json'), '--current-json',
                                   str(root / 'current.json'), '--changelog',
                                   str(root / 'CHANGELOG.md'), *extra], text=True, capture_output=True)

    def test_unstated_removal_fails(self):
        result = self.run_check(release('26.10.1', ['olivares_26.10.1_fips_linux_amd64.tar.gz']),
                                release('0.1', ['olivares_0.1_linux_amd64.tar.gz']), '### Changed\n\nOther changes.')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('undocumented removal: olivares_{version}_fips_linux_amd64.tar.gz', result.stderr)

    def test_normalized_version_is_not_a_removal(self):
        result = self.run_check(release('26.10.1', ['olivares-install-26.10.1.sh', 'olivares_26.10.1_linux_amd64.deb']),
                                release('0.1', ['olivares-install-0.1.sh', 'olivares_0.1_linux_amd64.deb']))
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_stated_removal_passes(self):
        result = self.run_check(release('26.10.1', ['image.spdx.sbom.json']), release('0.1', ['checksums.txt']),
                                '### Removed\n\n- Release assets removed: `image.spdx.sbom.json`.')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('- image.spdx.sbom.json', result.stdout)

    def test_partial_statement_and_wrong_section_fail(self):
        for notes in ('### Removed\n\n- Release assets removed: `unrelated.json`.',
                      '### Added\n\n- Release assets removed: `image.spdx.sbom.json`.',
                      '### Removed\n\n```\n- Release assets removed: `image.spdx.sbom.json`.\n```',
                      'Text.\n\n## [0.0] - 2026-10-01\n\n### Removed\n- Release assets removed: `image.spdx.sbom.json`.'):
            with self.subTest(notes=notes):
                result = self.run_check(release('26.10.1', ['image.spdx.sbom.json']), release('0.1', ['checksums.txt']), notes)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('undocumented removal:', result.stderr)

    def test_malformed_metadata_fails_closed(self):
        for previous in ({}, release('26.10.1', []), release('26.10.1', ['same', 'same'])):
            result = self.run_check(previous, release('0.1', ['checksums.txt']))
            self.assertNotEqual(result.returncode, 0)

    def test_phase_two_signature_is_only_pending_with_pipeline_pair(self):
        previous = release('26.10.1', ['stable-manifest.json', 'stable-manifest.json.sig'])
        names = ['stable-manifest.json', 'stable-manifest.json.pipeline.sig', 'stable-manifest.json.pipeline.pem']
        for pending, assets, ok in [(True, names, True), (False, names, False), (True, names[:-1], False)]:
            result = self.run_check(previous, release('0.1', assets), '', *(['--phase-one'] if pending else []))
            self.assertEqual(result.returncode == 0, ok, result.stderr)

    def test_previous_release_uses_publication_order(self):
        spec = importlib.util.spec_from_file_location('asset_check', SCRIPT)
        checker = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(checker)
        def published(tag, date, **extra):
            return dict(tag_name=tag, published_at=date, draft=False, prerelease=False, **extra)
        current = published('0.1', '2026-10-10T00:00:00Z')
        previous = published('26.10.1', '2026-10-03T00:00:00Z')
        releases = [published('0.2', '2026-10-13T00:00:00Z'), current,
                    dict(previous, tag_name='draft', draft=True),
                    dict(previous, tag_name='preview', prerelease=True), previous,
                    published('26.10.0', '2026-10-01T00:00:00Z')]
        self.assertEqual(checker.previous_release(releases, current), previous)
        draft = dict(current, draft=True, published_at=None, created_at='2026-10-01T00:00:00Z')
        self.assertEqual(checker.previous_release([previous], draft), previous)
        with self.assertRaises(ValueError):
            checker.previous_release([current], current)
        with patch.object(checker.subprocess, 'run', side_effect=subprocess.CalledProcessError(1, ['gh'])):
            with self.assertRaises(subprocess.CalledProcessError):
                checker.gh_json('repos/example/project/releases')

    def test_network_path_paginates_both_asset_inventories(self):
        spec = importlib.util.spec_from_file_location('asset_check_network', SCRIPT)
        checker = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(checker)
        current = dict(release('0.1', ['incomplete']), id=2, published_at=None, draft=True)
        previous = dict(release('26.10.1', ['incomplete']), id=1,
                        published_at='2026-10-03T00:00:00Z', draft=False, prerelease=False)
        prefix = 'repos/example/project/releases'
        responses = {prefix + '?per_page=100': [[previous], [current]],
                     prefix + '/1/assets?per_page=100': [[{'name': 'checksums.txt'}], [{'name': 'removed.json'}]],
                     prefix + '/2/assets?per_page=100': [[{'name': 'checksums.txt'}]]}
        def fetch(endpoint, paginate=False):
            if '/tags/' in endpoint:
                raise subprocess.CalledProcessError(1, ['gh', 'api', endpoint], stderr='HTTP 404')
            self.assertTrue(paginate)
            return responses[endpoint]
        with tempfile.TemporaryDirectory(dir=os.environ.get('TMPDIR')) as tmp:
            changelog = Path(tmp) / 'CHANGELOG.md'
            changelog.write_text('## [0.1] - 2026-10-10\n\n### Changed\nOther changes.\n')
            args = ['check-release-assets.py', '--repo', 'example/project', '--tag', '0.1', '--changelog', str(changelog)]
            with patch.object(sys, 'argv', args), patch.object(checker, 'gh_json', side_effect=fetch), \
                    redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()) as errors:
                self.assertEqual(checker.main(), 1)
            self.assertIn('undocumented removal: removed.json', errors.getvalue())

    def test_candidate_lookup_requires_exactly_one_visible_match(self):
        spec = importlib.util.spec_from_file_location('asset_check_lookup', SCRIPT)
        checker = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(checker)
        candidate = dict(release('0.1', ['checksums.txt']), id=2, draft=True, published_at=None)
        previous = dict(release('26.10.1', ['checksums.txt']), id=1, draft=False,
                        prerelease=False, published_at='2026-10-03T00:00:00Z')
        prefix = 'repos/example/project/releases'
        for matches, expected in [([], 1), ([candidate, candidate], 1), ([candidate], 0)]:
            with self.subTest(matches=len(matches)), tempfile.TemporaryDirectory(dir=os.environ.get('TMPDIR')) as tmp:
                changelog = Path(tmp) / 'CHANGELOG.md'
                changelog.write_text('## [0.1] - 2026-10-10\n\n### Changed\nOther changes.\n')
                calls = []
                def fetch(endpoint, paginate=False):
                    calls.append(endpoint)
                    if '/tags/' in endpoint:
                        raise subprocess.CalledProcessError(1, ['gh', 'api', endpoint], stderr='HTTP 404')
                    self.assertTrue(paginate)
                    if endpoint == prefix + '?per_page=100':
                        return [[previous], matches]
                    return [[{'name': 'checksums.txt'}]]
                args = ['check-release-assets.py', '--repo', 'example/project', '--tag', '0.1',
                        '--changelog', str(changelog), '--phase-one']
                with patch.object(sys, 'argv', args), patch.object(checker, 'gh_json', side_effect=fetch), \
                        redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()) as errors:
                    self.assertEqual(checker.main(), expected, errors.getvalue())
                if expected:
                    self.assertEqual(calls, [prefix + '?per_page=100'])
                    self.assertIn('comparison failed', errors.getvalue())
                else:
                    self.assertEqual(calls, [prefix + '?per_page=100', prefix + '/1/assets?per_page=100',
                                             prefix + '/2/assets?per_page=100'])

    def test_published_26_10_1_to_0_1(self):
        fixtures = json.loads((ROOT / 'scripts/testdata/release-assets-26.10.1-to-0.1.json').read_text())
        previous, current = fixtures['26.10.1'], fixtures['0.1']
        result = self.run_check(previous, current, '### Changed\n\nOther changes.')
        self.assertEqual(result.returncode, 1, result.stderr)
        missing = [line.removeprefix('undocumented removal: ') for line in result.stderr.splitlines()]
        self.assertEqual(len(missing), 45)
        self.assertIn('image.spdx.sbom.json', missing)
        self.assertIn('olivares_{version}_fips_linux_arm64.tar.gz', missing)
        statement = '### Removed\n\n- Release assets removed: ' + ', '.join('`' + n + '`' for n in missing) + '.'
        stated = self.run_check(previous, current, statement)
        self.assertEqual(stated.returncode, 0, stated.stderr)

    def test_workflow_waits_for_uploads_and_is_read_only(self):
        source = (ROOT / '.github/workflows/release.yml').read_text()
        self.assertIn('  verify-assets:\n', source)
        block = source.split('  verify-assets:\n', 1)[1].split('\n  promote-latest:', 1)[0]
        self.assertIn('needs: [preflight, goreleaser, provenance-binaries, provenance-image]', block)
        self.assertIn('contents: write', block)
        self.assertIn('draft visibility', block)
        self.assertNotIn('gh release upload', block)
        self.assertNotIn('gh release edit', block)
        self.assertIn('!failure() && !cancelled()', block)
        self.assertIn('python3 -B scripts/check-release-assets.py', block)
        self.assertIn('--phase-one', block)


if __name__ == '__main__':
    unittest.main()
