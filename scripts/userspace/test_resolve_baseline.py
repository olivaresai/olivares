#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Development protects the current release; release cuts cannot qualify themselves."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace
from urllib.error import HTTPError

from resolve_baseline import older_tags, resolve


class BaselineSelection(unittest.TestCase):
    def test_development_uses_current_published_release_from_git_tags(self) -> None:
        releases = {tag: {'tag_name': tag, 'published_at': 'date', 'draft': False, 'prerelease': False}
                    for tag in ('26.1000', '26.1001', '26.1002')}
        for local_tags in ('26.1000\n26.1001\n26.1002\n', '26.1000\n'):
            with self.subTest(local_tags=local_tags), \
                    patch('resolve_baseline.subprocess.run', return_value=SimpleNamespace(
                        returncode=0, stdout=local_tags)), \
                    patch('resolve_baseline.get', side_effect=lambda path: releases[path.rsplit('/', 1)[1]]):
                self.assertEqual(resolve('26.1001')['tag_name'], '26.1001')

    def test_development_uses_current_published_release_from_api_fallback(self) -> None:
        releases = [{'tag_name': tag, 'published_at': 'date', 'draft': False, 'prerelease': False}
                    for tag in ('26.1002', '26.1001', '26.1000')]
        with patch('resolve_baseline.subprocess.run', return_value=SimpleNamespace(returncode=1)), \
                patch('resolve_baseline.get', return_value=releases):
            self.assertEqual(resolve('26.1001')['tag_name'], '26.1001')

    def test_current_release_must_be_published_on_both_paths(self) -> None:
        previous = {'tag_name': '26.1000', 'published_at': 'date', 'draft': False, 'prerelease': False}
        current = dict(previous, tag_name='26.1001')
        for invalid in ({'draft': True}, {'prerelease': True}, {'published_at': None}):
            releases = {'26.1000': previous, '26.1001': dict(current, **invalid)}
            for use_tags in (True, False):
                with self.subTest(invalid=invalid, use_tags=use_tags), \
                        patch('resolve_baseline.subprocess.run', return_value=SimpleNamespace(
                            returncode=0 if use_tags else 1, stdout='26.1000\n26.1001\n')), \
                        patch('resolve_baseline.get', side_effect=lambda path: (
                            releases[path.rsplit('/', 1)[1]] if '/tags/' in path else
                            list(releases.values()))):
                    self.assertEqual(resolve('26.1001')['tag_name'], '26.1000')

    def test_publication_check_errors_do_not_select_an_older_release(self) -> None:
        previous = {'tag_name': '26.1000', 'published_at': 'date', 'draft': False, 'prerelease': False}

        def publication(path: str) -> dict | list[dict]:
            if path.endswith('/26.1001'):
                raise HTTPError('url', 403, 'forbidden', {}, None)
            return previous if '/tags/' in path else [previous]

        with patch('resolve_baseline.subprocess.run', return_value=SimpleNamespace(
                returncode=0, stdout='26.1000\n26.1001\n')), \
                patch('resolve_baseline.get', side_effect=publication):
            with self.assertRaises(HTTPError):
                resolve('26.1001')

    def test_release_tag_never_compares_with_itself(self):
        self.assertEqual(older_tags(['26.1000', '26.1001', '26.1002'], '26.1002'),
                         ['26.1001', '26.1000'])

    def test_newer_and_prerelease_tags_are_excluded(self):
        self.assertEqual(older_tags(['26.1003', '26.1002-rc1', '26.900'], '26.1002'),
                         ['26.900'])

    def test_versions_sort_numerically(self):
        self.assertEqual(older_tags(['26.909', '26.1000', '26.1001'], '26.1002'),
                         ['26.1001', '26.1000', '26.909'])

    def test_first_stable_release_does_not_read_retired_baselines(self):
        with patch('resolve_baseline.subprocess.run', side_effect=AssertionError('must not read retired tags')), \
                patch('resolve_baseline.get', side_effect=AssertionError('must not read retired releases')):
            self.assertIsNone(resolve('1.0', release_cut=True))
        self.assertEqual(older_tags(['26.10.2', 'v26.9.0', '1.0'], '1.1'), ['1.0'])

    def test_first_stable_development_protects_1_0_only_once_published(self) -> None:
        retired = [{'tag_name': tag, 'published_at': 'date', 'draft': False, 'prerelease': False}
                   for tag in ('26.10.1', '26.10.0', 'v26.9.0')]
        first = {'tag_name': '1.0', 'published_at': 'date', 'draft': False, 'prerelease': False}
        for published_1_0, expected in ((False, None), (True, '1.0')):
            def publication(path: str) -> dict | list[dict]:
                if path == 'releases/tags/1.0':
                    if published_1_0:
                        return first
                    raise HTTPError('url', 404, 'not published', {}, None)
                if path.startswith('releases?'):
                    return retired + ([first] if published_1_0 else [])
                raise AssertionError('must not read a retired release: ' + path)
            with self.subTest(published_1_0=published_1_0), \
                    patch('resolve_baseline.subprocess.run', return_value=SimpleNamespace(
                        returncode=0, stdout='v26.9.0\n26.10.0\n26.10.1\n')), \
                    patch('resolve_baseline.get', side_effect=publication):
                result = resolve('1.0')
                self.assertEqual(result and result['tag_name'], expected)

    def test_first_stable_development_reads_the_api_fallback(self) -> None:
        first = {'tag_name': '1.0', 'published_at': 'date', 'draft': False, 'prerelease': False}
        retired = {'tag_name': '26.10.1', 'published_at': 'date', 'draft': False, 'prerelease': False}
        for listed, expected in (([retired, first], '1.0'), ([retired], None)):
            with self.subTest(expected=expected), \
                    patch('resolve_baseline.subprocess.run', return_value=SimpleNamespace(returncode=1)), \
                    patch('resolve_baseline.get', return_value=listed):
                result = resolve('1.0')
                self.assertEqual(result and result['tag_name'], expected)

    def test_a_later_release_without_a_published_baseline_fails_closed(self) -> None:
        # Only 1.0 may have no baseline: from 1.1 on, a missing one stops compat.sh.
        def unpublished(path: str) -> list:
            if '/tags/' in path:
                raise HTTPError('url', 404, 'not published', {}, None)
            return []
        for release_cut in (True, False):
            with self.subTest(release_cut=release_cut), \
                    patch('resolve_baseline.subprocess.run', return_value=SimpleNamespace(
                        returncode=0, stdout='1.0\n1.1\n')), \
                    patch('resolve_baseline.get', side_effect=unpublished):
                with self.assertRaises(ValueError):
                    resolve('1.1', release_cut=release_cut)

    def test_first_release_has_no_baseline(self):
        self.assertEqual(older_tags(['26.1000'], '26.1000'), [])

    def test_unpublished_tag_does_not_replace_the_published_baseline(self):
        published = {'tag_name': '26.1000', 'published_at': 'date', 'draft': False, 'prerelease': False}
        with patch('resolve_baseline.subprocess.run', return_value=SimpleNamespace(returncode=0, stdout='26.1000\n26.1001\n26.1002\n')), \
                patch('resolve_baseline.get', side_effect=[HTTPError('url', 404, 'not published', {}, None), published]) as api:
            result = resolve('26.1002', release_cut=True)
        self.assertEqual(result['tag_name'], '26.1000')
        self.assertEqual([call.args[0] for call in api.call_args_list], ['releases/tags/26.1001', 'releases/tags/26.1000'])

    def test_api_fallback_excludes_candidate_and_newer_releases(self):
        releases = [{'tag_name': tag, 'published_at': 'date', 'draft': False, 'prerelease': False}
                    for tag in ('26.1003', '26.1002', '26.1001')]
        with patch('resolve_baseline.subprocess.run', return_value=SimpleNamespace(returncode=1)), \
                patch('resolve_baseline.get', return_value=releases):
            result = resolve('26.1002', release_cut=True)
        self.assertEqual(result['tag_name'], '26.1001')
        self.assertEqual(result['selection'], 'public release API fallback')


class CompatibilityBaseline(unittest.TestCase):
    def test_first_stable_release_captures_initial_contracts(self) -> None:
        source = Path(__file__).resolve().parent
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            scripts = root / 'scripts/userspace'
            scripts.mkdir(parents=True)
            shutil.copy(source / 'resolve_baseline.py', scripts)
            # compat.sh's real selection and first-release block, stopping before asset downloads;
            # the tools that block runs are stubs that record they ran.
            prefix = (source / 'compat.sh').read_text().split('assets=', 1)[0]
            (scripts / 'compat.sh').write_text(prefix)
            (scripts / 'test_stub.py').write_text('import unittest\n\n\nclass Stub(unittest.TestCase):\n'
                                                   '    def test_runs(self):\n        pass\n')
            (scripts / 'capture.py').write_text("import sys\nopen(sys.argv[sys.argv.index('--output') + 1], 'w')"
                                                ".write('{}')\n")
            (root / 'scripts/check-migrations.sh').write_text('exit 0\n')
            (root / 'RELEASE-VERSION').write_text('1.0\n')
            out = root / 'out'
            out.mkdir()
            # 1.0 is not published: the tag lookup answers 404 and the release list is empty.
            (root / 'sitecustomize.py').write_text('''
import io, json, urllib.error, urllib.request
def urlopen(request, timeout):
    if '/releases?' in request.full_url:
        return io.BytesIO(b'[]')
    raise urllib.error.HTTPError(request.full_url, 404, 'not published', {}, None)
urllib.request.urlopen = urlopen
''')
            for args in (['init', '-q'], ['-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                                          '-c', 'commit.gpgsign=false', 'commit', '--allow-empty', '-qm', 'tree']):
                subprocess.run(['git', *args], cwd=root, check=True, capture_output=True, timeout=10)
            result = subprocess.run(['bash', str(scripts / 'compat.sh'), '/bin/true'],
                                    cwd=root, capture_output=True, text=True, timeout=60,
                                    env={**os.environ, 'USERSPACE_OUT': str(out), 'PYTHONPATH': str(root),
                                         'GITHUB_REF': 'refs/heads/main',
                                         # `go vet` is stubbed as an exported bash function, so the
                                         # test runs where the temporary directory is noexec too.
                                         'BASH_FUNC_go%%': '() { return 0; }'})
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIsNone(json.loads((out / 'baseline.json').read_text()))
            self.assertIsNone(json.loads((out / 'findings.json').read_text())['baseline'])
            self.assertTrue((out / 'candidate.json').exists())
            self.assertIn('Initial stable contracts captured', result.stdout)
            self.assertNotIn('published baseline', result.stdout)

    def test_checkout_distinguishes_release_cut_from_development(self) -> None:
        source = Path(__file__).resolve().parent
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            scripts = root / 'scripts/userspace'
            scripts.mkdir(parents=True)
            shutil.copy(source / 'resolve_baseline.py', scripts)
            # Execute compat.sh's actual selection block, stopping before asset downloads.
            prefix = (source / 'compat.sh').read_text().split('assets=', 1)[0]
            (scripts / 'compat.sh').write_text(prefix)
            (root / 'RELEASE-VERSION').write_text('26.1001\n')
            out = root / 'out'
            out.mkdir()
            # Only the public API transport is replaced; Git and both selectors are real.
            (root / 'sitecustomize.py').write_text('''
import io, json, urllib.request
def urlopen(request, timeout):
    def release(tag):
        return dict(tag_name=tag, published_at='date', draft=False, prerelease=False)
    if '/releases?' in request.full_url:
        data = [release('26.1001'), release('26.1000')]
    else:
        tag = request.full_url.rsplit('/', 1)[1]
        assert tag in ('26.1000', '26.1001')
        data = release(tag)
    return io.BytesIO(json.dumps(data).encode())
urllib.request.urlopen = urlopen
''')
            def git(*args: str) -> None:
                subprocess.run(['git', *args], cwd=root, check=True, capture_output=True, timeout=10)
            git('init', '-q')
            git('add', 'RELEASE-VERSION')
            git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                '-c', 'commit.gpgsign=false', 'commit', '-qm', 'published')
            git('tag', '26.1000')
            git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                '-c', 'commit.gpgsign=false', 'commit', '--allow-empty', '-qm', 'current release')
            git('tag', '26.1001')
            for state, expected, ref in [('cut', '26.1000', 'refs/heads/main'),
                                         ('development', '26.1001', 'refs/heads/main'),
                                         ('partial-tags', '26.1001', 'refs/heads/main'),
                                         ('no-tags', '26.1001', 'refs/heads/main'),
                                         ('tag-ref', '26.1000', 'refs/tags/26.1001')]:
                if state == 'development':
                    git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                        '-c', 'commit.gpgsign=false', 'commit', '--allow-empty', '-qm', 'development')
                if state == 'partial-tags':
                    git('tag', '-d', '26.1001')
                if state == 'no-tags':
                    git('tag', '-d', '26.1000')
                result = subprocess.run(['bash', str(scripts / 'compat.sh'), '/bin/true'],
                                        cwd=root, capture_output=True, text=True, timeout=10,
                                        env=dict(os.environ, USERSPACE_OUT=str(out),
                                                 PYTHONPATH=str(root), GITHUB_REF=ref))
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(json.loads((out / 'baseline.json').read_text())['tag_name'], expected)
                self.assertIn('published baseline: ' + expected, result.stdout)
