#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Release derivation preserves records while updating install coordinates."""
import json
import os
import re
import shutil
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parent.parent
# Exercise the checker's own classifiers without running its repository census.
SOURCE = (ROOT / 'scripts/check-release-version.sh').read_text().split("python3 - <<'PY'\n", 1)[1]
CHECKER = {}
# The checker discovers paths and reads the retired tag list relative to the repository root it
# runs from. Tests change directory: load it from the root so no test depends on the caller's
# directory or on which test ran first.
_before = Path.cwd()
os.chdir(ROOT)
exec(compile(SOURCE.split('if SELFTEST:', 1)[0], 'check-release-version.sh', 'exec'), CHECKER)
CHECKER['retired_tags']()
os.chdir(_before)


class ReleaseGeneration(unittest.TestCase):
    def test_published_openapi_descriptions_use_current_release_vocabulary(self):
        def descriptions(value):
            if isinstance(value, dict):
                for key, child in value.items():
                    if key == 'description' and isinstance(child, str):
                        yield child
                    else:
                        yield from descriptions(child)
            elif isinstance(value, list):
                for child in value:
                    yield from descriptions(child)

        paths = sorted((ROOT / 'web/openapi').glob('*.json'))
        self.assertTrue(paths, 'published OpenAPI snapshots must exist')
        for path in paths:
            with self.subTest(path=path.name):
                text = '\n'.join(descriptions(json.loads(path.read_text())))
                self.assertTrue(text, 'published descriptions must exist')
                name = path.relative_to(ROOT).as_posix()
                hits = CHECKER['scan']([name], lambda _: text)
                retired = [hit.token for hit in hits if hit.token.lstrip('vV').startswith('26.')]
                self.assertEqual(retired, [])

    def test_active_development_build_callers_use_canonical_stamps(self):
        helper = ROOT / 'scripts/build-ldflags.sh'
        canon = subprocess.check_output(['sh', str(helper), '--version'], text=True).strip()
        # Community source builds. The operator and hardened images moved to Business;
        # qualify-container-release.sh now consumes the source-built binary unchanged.
        callers = (
            '.github/workflows/compose-ready.yml',
            '.github/workflows/managed-tools.yml',
            '.github/workflows/patch-velocity.yml',
        )
        for name in callers:
            source = (ROOT / name).read_text()
            expression = re.search(r'--build-arg VERSION=(.+?)\s*\\\n', source)[1]
            with self.subTest(caller=name):
                value = subprocess.check_output(['bash', '-eu', '-c', 'printf "%s" ' + expression],
                                                cwd=ROOT, text=True)
                result = subprocess.run(['sh', str(helper), value, '123abcd', '2026-10-07T00:00:00Z'],
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertTrue(value == canon or value.startswith(canon + '-'), value)
                self.assertIn('main.version=' + value, result.stdout)

    def test_future_and_nonexistent_calver_references_are_refused(self):
        for path, text, judge in (
            ('operator/README.md', 'must serve /pod-readyz (olivares >= 26.11.0) and clients', 'judge_artifacts'),
            ('operator/README.md', 'must serve /pod-readyz (olivares >= 99.1.0) and clients', 'judge_artifacts'),
            ('docs/PSIRT-RUNBOOK.md', 'Requires >= 26.11.0', 'judge'),
            ('README.md', '99.1.0<!-- release-fixed -->', 'judge'),
            ('README.md', '26.11.0<!-- release-fixed -->', 'judge'),
            ('README.md', 'See CHANGELOG.md [26.11.0]', 'judge'),
            ('docs/releases/26.11.0-install-surfaces.json', '{"version":"26.11.0"}', 'judge'),
            ('CHANGELOG.md', '## [26.11.0] - 2026-10-08\nReleased 26.11.0', 'judge'),
            # export-closure: absent-by-design docs/RELEASE-GO-LIVE-RUNBOOK.md — a file name written into a temporary fixture tree, never read from this checkout
            ('docs/RELEASE-GO-LIVE-RUNBOOK.md', 'release 1.1', 'judge'),
            ('README.md', '1.0.0<!-- release-fixed -->', 'judge'),
            ('docs/PSIRT-RUNBOOK.md', 'Requires >= 1.0.0', 'judge'),
        ):
            with self.subTest(path=path, text=text):
                hits = CHECKER['scan']([path], lambda _: text)
                self.assertTrue(hits, 'the actual scanner must see the refused token')
                failures = CHECKER[judge]('1.0', hits) if judge == 'judge_artifacts' else CHECKER[judge]('1.0', hits, set(), set())[0]
                self.assertTrue(failures, 'unpublished reference was accepted')

    def test_named_calver_records_keep_only_their_existing_allowance(self):
        for path, text in (
            ('CHANGELOG.md', '## [26.10.1] - 2026-10-04\nReleased 26.10.1'),
            ('docs/releases/26.10.1-install-surfaces.json', '{"version":"26.10.1"}'),
            ('docs/DR-RUNBOOK.md', '26.10.1<!-- release-fixed -->'),
            ('docs/RELEASE-VERIFICATION.md', 'Requires >= v26.8.0'),
            ('README.md', 'See CHANGELOG.md [26.9.0]'),
            ('docs/RELEASE-GO-LIVE-RUNBOOK.md', 'release v26.8.1'),
            ('docs/integrations/paperclip-guide.md', 'Measured with Olivares AI 26.10.2<!-- release-fixed -->'),
        ):
            with self.subTest(path=path, text=text):
                hits = CHECKER['scan']([path], lambda _: text)
                self.assertTrue(hits)
                self.assertEqual(CHECKER['judge']('1.0', hits, {'docs/launch'}, set())[0], [])
        for path, text in (
            ('README.md', '26.10.2<!-- release-fixed -->'),
            ('docs/integrations/paperclip-guide.md', 'install olivares:26.10.2'),
            ('README.md', 'install olivares:26.10.1'),
            ('docs/releases/26.10.1-install-surfaces.json', '{"version":"26.10.0"}'),
            ('README.md', '0.1.0<!-- release-fixed -->'),
            # export-closure: absent-by-design docs/launch/recap-draft-launch-month.md — a file name written into a temporary fixture tree, never read from this checkout
            ('docs/launch/recap-draft-launch-month.md', 'release v1.0.1'),
        ):
            with self.subTest(path=path, text=text):
                hits = CHECKER['scan']([path], lambda _: text)
                self.assertTrue(CHECKER['judge']('1.0', hits, set(), set())[0])

    def test_image_task_uses_canon_and_preserves_explicit_container_override(self):
        # Execute the real task recipe and Dockerfile build instruction, replacing
        # only Docker and the compiler with argument recorders (no image build).
        task = (ROOT / 'Taskfile.yml').read_text().split('\n  image:\n', 1)[1]
        recipe = textwrap.dedent(task.split('\n  image:ebpf:', 1)[0].split('      - |\n', 1)[1])
        dockerfile = (ROOT / 'Dockerfile').read_text().replace('\\\n', '').splitlines()
        build = next(line.removeprefix('RUN ') for line in dockerfile if line.startswith('RUN VERSION_LDFLAGS='))
        defaults = dict(line[4:].partition('=')[::2] for line in dockerfile if line.startswith('ARG '))
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / 'scripts/lib').mkdir(parents=True)
            for file in ('scripts/build-ldflags.sh', 'scripts/lib/git-env.sh'):
                shutil.copyfile(ROOT / file, root / file)
            for tool in ('docker', 'go'):
                (root / tool).write_text('#!/bin/sh\nprintf "%s\\n" "$@"\n')
                (root / tool).chmod(0o755)
            env = dict(os.environ, PATH=str(root) + ':' + os.environ['PATH'])
            args = subprocess.check_output(['sh', '-eu', '-c', recipe], cwd=ROOT, env=env, text=True).splitlines()
            self.assertEqual(args[:2], ['build', '--build-arg'])
            self.assertEqual(args[-3:], ['-t', 'olivares:dev', '.'])
            supplied = dict(args[i + 1].split('=', 1) for i, arg in enumerate(args) if arg == '--build-arg')
            for canon in ('1.0', '1.1'):
                (root / 'RELEASE-VERSION').write_text(canon + '\n')
                for override in ('', '1.1-custom'):
                    with self.subTest(canon=canon, override=override):
                        build_env = {**env, **defaults, **supplied}
                        if override:
                            build_env['VERSION'] = override
                        compiled = subprocess.check_output(['sh', '-eu', '-c', build], cwd=root,
                                                           env=build_env, text=True).splitlines()
                        flags = compiled[compiled.index('-ldflags') + 1].split()
                        self.assertIn('main.version=' + (override or canon), flags)
                        self.assertIn('main.commit=' + supplied['COMMIT'], flags)

    def test_identical_live_and_historical_tokens_are_stamped_per_occurrence(self):
        mark = lambda text: '<!-- release -->' + text + '<!-- /release -->'
        for historical in ('Requires >= 26.10.1', 'See CHANGELOG.md [26.10.1]'):
            for live in ('install 26.10.1', 'install olivares:26.10.1',
                         'install olivares_26.10.1_linux_amd64.deb',
                         'install olivares-install-26.10.1.sh'):
                for parts in ((historical, mark(live)), (mark(live), historical)):
                    with self.subTest(parts=parts), tempfile.TemporaryDirectory() as folder:
                        before = Path.cwd()
                        try:
                            os.chdir(folder)
                            path = Path('docs/PSIRT-RUNBOOK.md')
                            path.parent.mkdir()
                            original = '  Step: ' + '; '.join(parts) + '\n'
                            expected = original.replace(mark(live), mark(live.replace('26.10.1', '1.1')))
                            path.write_text(original)
                            hits = CHECKER['scan']([str(path)], lambda p: Path(p).read_text())
                            with self.subTest(stage='reject unstamped download'):
                                self.assertTrue(CHECKER['judge']('1.1', hits, set(), set())[0])
                            for _ in range(2):
                                hits = CHECKER['scan']([str(path)], lambda p: Path(p).read_text())
                                CHECKER['stamp_surfaces']('1.1', hits)
                                self.assertEqual(path.read_text(), expected)
                            hits = CHECKER['scan']([str(path)], lambda p: Path(p).read_text())
                            self.assertEqual(CHECKER['judge']('1.1', hits, set(), set())[0], [])
                        finally:
                            os.chdir(before)

    def test_live_surfaces_follow_canon_without_rewriting_history(self):
        files = {
            'README.md': 'See <!-- release -->Install olivares:26.10.1 and 26.10.1-fips<!-- /release -->\nSee CHANGELOG.md [26.10.1]\n',
            'deploy/helm/olivares/Chart.yaml': '# release\nappVersion: "26.10.1"\n# /release\n',
            'deploy/manifests/install.yaml': '# release\nimage: docker.io/olivaresai/olivares:26.10.1\n# /release\n',
            'CHANGELOG.md': '# Changelog\n## [26.10.1] - 2026-10-01\nReleased 26.10.1\n',
            'docs/releases/26.10.1-install-surfaces.json': '{"version":"26.10.1"}\n',
            'operator/README.md': 'Requires olivares >= 26.8.0\n',
            'SECURITY.md': 'Latest supported release: <!-- release -->26.10.1<!-- /release -->\n',
            'docs/deprecation.md': 'Deprecated in 26.10.1<!-- release-fixed -->; install <!-- release -->26.10.1<!-- /release -->\n',
            'INSTALL.md': 'Requires >= 26.10.0; install <!-- release -->26.10.1<!-- /release -->\n'
                          'See <!-- release -->Install olivares_26.10.1_linux_amd64.deb and olivares-install-26.10.1.sh. '
                          'Release 26.10.1.<!-- /release -->\n',
        }
        with tempfile.TemporaryDirectory() as folder:
            before = Path.cwd()
            try:
                os.chdir(folder)
                for name, content in files.items():
                    Path(name).parent.mkdir(parents=True, exist_ok=True)
                    Path(name).write_text(content)
                hits = CHECKER['scan'](files, lambda path: Path(path).read_text())
                CHECKER['stamp_surfaces']('1.1', hits)
                self.assertEqual(Path('deploy/helm/olivares/Chart.yaml').read_text(),
                                 '# release\nappVersion: "1.1"\n# /release\n')
                self.assertIn('olivares:1.1', Path('deploy/manifests/install.yaml').read_text())
                self.assertEqual(Path('SECURITY.md').read_text(),
                                 'Latest supported release: <!-- release -->1.1<!-- /release -->\n')
                self.assertEqual(Path('docs/deprecation.md').read_text(),
                                 'Deprecated in 26.10.1<!-- release-fixed -->; install <!-- release -->1.1<!-- /release -->\n')
                self.assertEqual(Path('INSTALL.md').read_text(), files['INSTALL.md'].replace(
                    'install <!-- release -->26.10.1', 'install <!-- release -->1.1').replace(
                    'olivares_26.10.1_linux_amd64.deb and olivares-install-26.10.1.sh. Release 26.10.1.',
                    'olivares_1.1_linux_amd64.deb and olivares-install-1.1.sh. Release 1.1.'))
                self.assertEqual(Path('README.md').read_text(),
                                 'See <!-- release -->Install olivares:1.1 and 1.1-fips<!-- /release -->\nSee CHANGELOG.md [26.10.1]\n')
                for name in ('CHANGELOG.md', 'docs/releases/26.10.1-install-surfaces.json', 'operator/README.md'):
                    self.assertEqual(Path(name).read_text(), files[name])
                stamped = {name: Path(name).read_bytes() for name in files}
                CHECKER['stamp_surfaces']('1.1', CHECKER['scan'](files, lambda p: Path(p).read_text()))
                self.assertEqual(stamped, {name: Path(name).read_bytes() for name in files})
            finally:
                os.chdir(before)

    def test_fixed_versions_are_checked_and_never_exempt_other_tokens(self):
        # An annotation binds the version right before it, never another occurrence on the line.
        for canon, text, failures in (
            ('1.0', 'release 1.1<!-- release-fixed -->', 1),
            ('1.1', '26.10.1<!-- release-fixed -->', 0),
            ('1.1', '26.10.1<!-- release-fixed -->; install 26.10.0', 1),
            ('1.1', '26.10.1<!-- release-fixed -->; install 26.10.1', 1),
            ('1.1', 'install 26.10.1; 26.10.1<!-- release-fixed -->', 1),
        ):
            hits = CHECKER['scan'](['README.md'], lambda p: text)
            self.assertEqual(len(CHECKER['judge'](canon, hits, set(), set())[0]), failures, text)

    def test_dependency_and_standard_versions_are_not_product_coordinates(self):
        text = ('Requires Go 1.26.6 and PostgreSQL 15.1; RFC 302.1; April 2026.05; 40.32 minutes; '
                'until 2554-07-21T23:34:33.709551615Z.\n')
        self.assertEqual(CHECKER['judge']('1.0', CHECKER['scan'](['README.md'], lambda _: text), set(), set())[0], [])
        for coordinate in ('olivares:1.0.1', 'olivares_1.0.1_linux_amd64.deb',
                           'release 1.0.1', 'olivares 1.0.1', 'chi v5.3.1', 'release 1.2.x of OSCAL'):
            hits = CHECKER['scan'](['README.md'], lambda _: coordinate)
            self.assertTrue(CHECKER['judge']('1.0', hits, set(), set())[0], coordinate)

    # Measured 2026-10-09: main refused each of these in its 26.x spelling, and this
    # checker passed them: a release above the canon in prose, a prerelease after a boundary word,
    # a 1.M.x series of a line that has no patch number, and a version behind a dependency word
    # that names no published version.
    # Spanish plants are fixture data in a quoted assignment each, which the source-language gate reads
    # as data (check-source-language.py, language-data).
    SPANISH_PRERELEASE_PLANT = 'Actualiza desde 1.0-rc.2 a la versión estable.'  # language-data: fixture
    SPANISH_ANY_WORDING = 'La versión {v} de Olivares es la actual.'  # language-data: fixture
    ABOVE_THE_CANON_AND_BOUNDARY_PLANTS = (
        'Olivares v2.0 is the current release.',
        'Olivares 2.0 adds clustering.',
        'Olivares 99.1.0 is the current release.',
        'Use >= 99.1.0 of Olivares.',
        'Upgrade from 1.0-rc.1 to the stable release.',
        'Ab 1.0-beta ist Olivares stabil.',
        SPANISH_PRERELEASE_PLANT,
        'Upgrade from 1.0-fips to the stable release.',
        'Olivares 1.1.x packages are signed.',
        'Olivares 1.0.x packages are signed.',
        'Olivares Compose 1.0.1 bundle.',
        'The Helm 1.0.1 chart installs Olivares.',
        # Measured 2026-10-10: the Go row reached 60 characters past an English "go" or "Go".
        'Olivares is a go-to tool; 1.21 ships next month.',
        'Go get 1.21.0 of Olivares today.',
        'Olivares go 1.21 ships next month.',
        'Ready to go 1.21 ships next month.',
        # Measured 2026-10-09: a lookalike dash before a prerelease word, a variant word no
        # list named, a version after a word only a sentence uses, a list or table cell that names
        # a release of ours, and a CVSS label before one.
        'Upgrade from 1.0\u2011rc to the stable release.',
        'Ab 1.0\u2014beta ist Olivares stabil.',
        'Upgrade from 1.0\u2011fips to the stable release.',
        'Upgrade from 1.0-lts to the stable release.',
        'Upgrade from 1.0-rc to the stable release.',
        'Upgrade from 1.0-stig to the stable release.',
        'Upgrade from 1.0-hotfix.2 to the stable release.',
        'Upgrade from 1.0\u7248-rc.2 to the stable release.',
        'Olivares will go 2.0 when the API breaks.',
        'Python 3.11 and 1.0.1 are the supported releases.',
        '| Go | Olivares release | 2.0 |',
        'Critical (1.0.1) fixes ship first.',
        'Olivares Linux 1.0.1 packages are signed.',
        '| Linux | amd64 | 1.0.1 |',
        'We go with Olivares 1.21 as the next release.',
        'Critical (1.1) was patched.',
        'Olivares 1.0/GA is out.',
        'Requires Olivares Compose >= 2.0.',
        'Olivares OpenAPI 2.0 adds webhooks.',
        'Olivares 2.0 分',
        'Olivares 2.0 GB of logs fit.',
        'Version 1.0/GA is out today.',
        'go 2.0 when the API breaks.',
        'Olivares@2.0 ships.',
        'OLIVARES TLS 1.1 support.',
        'Olivares  2.0x faster.',
        'It lands in 1.41.',
        'Olivares AI 1.0 以降 で利用可能',
        '**Olivares** 1.0 以降',
        'Olivares AI 2.0 GB of logs fit.',
        'Olivares AI Python 1.0 ships.',
        'The Compose 1.0.1 bundle ships.',
        '| TLS | amd64 | 1.0.1 |',
        'Releases use **MAJOR.MINOR** only; 2.0 adds clustering.',
        'The next release will go out as 1.21.',
    )
    PLANTS_IN_A_ROWS_DOCUMENT = (
        ('CHANGELOG.md', '- DOMPurify fixed in Olivares 2.0.'),
        ('CHANGELOG.md', '- undici is in Olivares 1.0.1.'),
        ('docs/trust/dod-zero-trust-mapping.md', 'Enforced in 1.0.'),
        ('docs/trust/dod-zero-trust-mapping.md', '| 3.0 | Olivares |'),
        ('docs-site/src/content/docs/how-to/connectors/tak.md', 'The connector (arrives in 2.0) adds TAK mTLS.'),
        ('docs/accessibility/VPAT-olivares-admin.md', 'Planned for 2.0/2.1 (keyboard).'),
        ('docs/accessibility/VPAT-olivares-admin.md', 'Keyboard navigation will be removed in 2.0.'),
        ('docs/accessibility/VPAT-olivares-admin.md', 'Re-issued with the 2.1 baseline.'),
        ('docs/trust/dod-zero-trust-mapping.md', 'Fixed in 1.1/1.2.'),
        ('docs/trust/dod-zero-trust-mapping.md', '| v1.2 | Planned |'),
        ('docs/trust/dod-zero-trust-mapping.md', 'The fix is listed in 1.1.'),
        ('docs/trust/evaluation-guide.md', '| 2.0 | Olivares clustering |'),
        ('docs/trust/evaluation-guide.md', '| 1.7 | Community release |'),
        ('docs/trust/csa-star-ai-readiness.md', 'Community v1.0 released 2026-10-14.'),
        ('docs/trust/csa-star-ai-readiness.md', 'The console shipped with the v1.1 release.'),
        ('docs-site/src/content/docs/how-to/connectors/tak.md', 'The connector (Version 2.0) adds mTLS.'),
        ('docs-site/src/content/docs/how-to/first-hour.md', 'Upgrade to 2.1.90 for the wizard.'),
        ('docs-site/src/content/docs/reference/siem-telemetry-egress.md', 'The 1.3 release adds the OCSF downgrade emitter.'),
        ('docs-site/src/content/docs/ja/reference/siem-telemetry-egress.md', 'OCSF エクスポートは 1.3 で提供されます。'),
        ('docs-site/src/content/docs/reference/siem-telemetry-egress.md', '| OCSF | Community | 1.2 |'),
        # Measured 2026-10-10: a bare preposition or full stop before 1.3 named an OCSF version.
        ('docs-site/src/content/docs/ru/reference/siem-telemetry-egress.md', 'Обновитесь до 1.3.'),
        ('docs-site/src/content/docs/zh/reference/siem-telemetry-egress.md', '在 1.3 中提供。'),
        ('docs-site/src/content/docs/ja/reference/siem-telemetry-egress.md', '対応しました。1.3 で提供されます。'),
        ('docs-site/src/content/docs/de/reference/siem-telemetry-egress.md', 'Es gibt ein 1.3 Update.'),
        ('docs-site/src/content/docs/de/reference/siem-telemetry-egress.md', 'Bis ein 1.3 Release erscheint.'),
        ('docs-site/src/content/docs/zh/reference/siem-telemetry-egress.md', '1.3 降级版本已发布。'),
        ('docs-site/src/content/docs/ja/reference/siem-telemetry-egress.md', '1.3 へのダウンロード版です。'),
        ('docs-site/src/content/docs/how-to/connectors/otel-genai.md', 'The 1.41 release adds GenAI spans.'),
        ('CHANGELOG.md', '- DOMPurify fixed in 2.0.'),
        ('docs-site/src/content/docs/reference/connectors.md', 'A2A support lands in v1.0.'),
        ('docs/accessibility/VPAT-olivares-admin.md', 'Olivares AI release 1.2.1 ships.'),
        ('docs/trust/README.md', 'Legacy API tokens are removed in 2.0.'),
        ('docs/accessibility/VPAT-olivares-admin.md', 'Tested on the 2.0 baseline.'),
        ('docs-site/src/content/docs/how-to/connectors/tak.md', '`Event-PUBLIC.xsd` support arrives in version 2.0) soon.'),
        ('docs-site/src/content/docs/reference/siem-telemetry-egress.md', '| ECS | amd64 | 1.0.1 |'),
        ('docs-site/src/content/docs/reference/connectors.md', 'The OpenTelemetry connector gains spans in v1.37.'),
        ('docs-site/src/content/docs/reference/connectors.md', 'The spec lands in v1.28.'),
    )
    BOUNDARY_WORDS = (
        'Releases before 1.0 used CalVer.',
        'Dies ist ein pre-1.0-Projekt.',
        'Olivares AI находится в pre-1.0-стадии.',
        '製品の多くは pre-1.0／設計段階にある。',
    )

    def test_a_release_above_the_canon_or_after_a_boundary_word_is_refused(self):
        plants = [('INSTALL.md', text) for text in self.ABOVE_THE_CANON_AND_BOUNDARY_PLANTS]
        for path, text in plants + list(self.PLANTS_IN_A_ROWS_DOCUMENT):
            with self.subTest(path=path, text=text):
                hits = CHECKER['scan']([path], lambda _: text + '\n')
                self.assertTrue(hits, 'the scanner must see the planted version')
                self.assertTrue(CHECKER['judge']('1.0', hits, set(), set())[0], 'a planted release was accepted')
        for text in self.BOUNDARY_WORDS:
            with self.subTest(text=text):
                hits = CHECKER['scan'](['INSTALL.md'], lambda _: text + '\n')
                self.assertEqual(CHECKER['judge']('1.0', hits, set(), set())[0], [])

    def test_private_rows_refuse_their_plants(self):
        # The rows file of the documents the export removes carries its own refusal plants
        # (`#plant<TAB>path<TAB>text`), so this shipped test names none of those documents. A public
        # tree has neither the file nor the documents.
        rows = ROOT / CHECKER['PRIVATE_ROWS']
        if not rows.exists():
            self.skipTest('no private rows in this tree')
        plants = [line.split('\t')[1:] for line in rows.read_text().splitlines() if line.startswith('#plant\t')]
        self.assertTrue(plants, 'the private rows carry no plant')
        before = Path.cwd()
        try:
            os.chdir(ROOT)
            for path, text in plants:
                with self.subTest(path=path, text=text):
                    hits = CHECKER['scan']([path], lambda _: text + '\n')
                    self.assertTrue(hits, 'the scanner must see the planted version')
                    self.assertTrue(CHECKER['judge']('1.0', hits, set(), set())[0], 'a planted release was accepted')
        finally:
            os.chdir(before)

    def test_every_dependency_word_and_row_is_needed(self):
        """Each DEPENDENCY word, and each row that reads DEPENDENCY, is the only one that names some
        version a published census file writes where no other row does: one that nothing needs, or
        that another word or row already covers, only opens a hole (`Olivares will go 2.0`)."""
        dependency = CHECKER['DEPENDENCY']
        rows = {name: pattern for name, _, pattern in CHECKER['NOT_RELEASES'] if dependency in pattern}
        self.assertTrue(rows, 'no row reads DEPENDENCY')
        alone = {w: {row: re.compile(p.replace(dependency, '(?:' + w + ')'), re.M) for row, p in rows.items()}
                 for w in CHECKER['DEPENDENCY_WORDS']}
        needed_words, needed_rows, skipped, before = set(), set(), 0, Path.cwd()
        os.chdir(ROOT)
        try:
            curated, kept = CHECKER['curated_out'](), CHECKER['curation_kept']()
            files = sorted(set(CHECKER['surface_files']()) | set(CHECKER['artifact_files']()))
            for path in files:
                if CHECKER['removed_by_export'](path, curated, kept):
                    skipped += 1
                    continue
                applicable = CHECKER['rows_at'](path, False)
                here = [name for name, _ in applicable if name in rows]
                text = CHECKER['read_surface'](path)
                spans = CHECKER['marks'](path, text)
                for offset, body in CHECKER['lines_at'](text):
                    for m in CHECKER['tokens'](body):
                        if (any(a <= offset + m.start() < b for a, b in spans)
                                or not CHECKER['product_line'](m.group())):
                            continue
                        window = CHECKER['row_window'](m)
                        if any(found.search(window) for name, found in applicable if name not in rows):
                            continue
                        by_row = [name for name, found in applicable if name in rows and found.search(window)]
                        by_word = [w for w, found in alone.items() if any(found[row].search(window) for row in here)]
                        needed_rows.update(by_row if len(by_row) == 1 else ())
                        needed_words.update(by_word if len(by_word) == 1 else ())
        finally:
            os.chdir(before)
        self.assertGreater(len(files), CHECKER['DOCS_LAST_GOOD']['floor'], 'the census walk shrank')
        if (ROOT / CHECKER['EXPORT_SCRIPT']).exists():
            self.assertGreater(skipped, 0, 'the curation was not read: removed documents counted as published')
        self.assertEqual(sorted(set(rows) - needed_rows), [], 'these rows admit no version only they name; drop them')
        self.assertEqual(sorted(set(alone) - needed_words), [], 'these words name no version only they name; drop them')

    def test_fleet_only_dependency_words_no_longer_waive_versions(self):
        # These words lost their exclusive published literals when fleet artifacts moved
        # to Business. A dependency name without a census owner must fail closed.
        for word in ('Kubernetes', 'XCCDF', 'nfpm'):
            with self.subTest(word=word):
                hits = CHECKER['scan'](['INSTALL.md'], lambda _: f'{word} 1.0.1 ships.\n')
                self.assertEqual(CHECKER['judge']('1.0', hits, set(), set())[0],
                                 [('INSTALL.md', 1, '1.0.1')])

    # Planted 2026-10-09: a patch release in a wording no context row named was neither refused nor
    # restamped (9 of 14 passed). Unmarked, every one is refused; marked, the stamp moves it.
    ANY_WORDING = (
        ('INSTALL.md', 'Olivares version {v} is the current release.'),
        ('INSTALL.md', 'Upgrade to {v} with `olivares upgrade`.'),
        ('INSTALL.md', 'The {v} release adds arm64 packages.'),
        ('INSTALL.md', 'export OLIVARES_VERSION={v}'),
        ('docs-site/src/content/docs/de/how-to/air-gap-install.md', 'Olivares-Version {v} ist die aktuelle Version.'),
        ('docs-site/src/content/docs/de/how-to/air-gap-install.md', 'Die nächste Version ist `{v}`.'),
        ('docs-site/src/content/docs/es/how-to/air-gap-install.md', SPANISH_ANY_WORDING),
        ('docs-site/src/content/docs/ja/how-to/air-gap-install.md', 'Olivares バージョン {v} が最新です。'),
        ('docs-site/src/content/docs/zh/how-to/air-gap-install.md', 'Olivares 版本 {v} 是当前版本。'),
        # Wordings a named row once hid.
        ('INSTALL.md', 'Olivares {v} Enterprise is the current release.'),
        ('INSTALL.md', 'The current release is {v} (stable).'),
        ('INSTALL.md', '| Latest | {v} | stable |'),
        ('INSTALL.md', 'TLS is required; the current release is {v}.'),
        ('INSTALL.md', 'brew install olivares@{v}'),
        ('INSTALL.md', 'Pull release:{v} first.'),
        ('INSTALL.md', 'Download https://example.com/olivares_{v}_linux_amd64.tar.gz'),
        ('docs-site/src/content/docs/fr/how-to/air-gap-install.md', 'La version {v} d’Olivares est la version actuelle.'),
        ('docs-site/src/content/docs/reference/connectors.md', 'Olivares {v} ships these connectors.'),
        ('docs/trust/evaluation-guide.md', 'Olivares {v} is the release under evaluation.'),
        ('INSTALL.md', '## {v} Release notes'),
        ('INSTALL.md', '### {v} (stable)'),
    )

    def test_a_patch_release_is_refused_in_any_wording(self):
        for path, line in self.ANY_WORDING:
            with self.subTest(path=path, line=line):
                text = line.format(v='1.0.1') + '\n'
                hits = CHECKER['scan']([path], lambda _: text)
                self.assertEqual(CHECKER['judge']('1.0', hits, set(), set())[0], [(path, 1, '1.0.1')])

    def test_a_release_in_any_wording_is_marked_and_stamped(self):
        for path, line in self.ANY_WORDING:
            with self.subTest(path=path, line=line), tempfile.TemporaryDirectory() as folder:
                before = Path.cwd()
                try:
                    os.chdir(folder)
                    Path(path).parent.mkdir(parents=True, exist_ok=True)
                    # Unmarked, the release would stay 1.0 at the next cut, so it is refused now.
                    Path(path).write_text(line.format(v='1.0') + '\n')
                    hits = CHECKER['scan']([path], CHECKER['read_surface'])
                    self.assertEqual(CHECKER['judge']('1.0', hits, set(), set())[0], [(path, 1, '1.0')])
                    Path(path).write_text(line.format(v='<!-- release -->1.0<!-- /release -->') + '\n')
                    hits = CHECKER['scan']([path], CHECKER['read_surface'])
                    self.assertEqual(CHECKER['judge']('1.0', hits, set(), set())[0], [])
                    CHECKER['stamp_surfaces']('1.1', hits)
                    self.assertEqual(Path(path).read_text(),
                                     line.format(v='<!-- release -->1.1<!-- /release -->') + '\n')
                    hits = CHECKER['scan']([path], CHECKER['read_surface'])
                    self.assertEqual(CHECKER['judge']('1.1', hits, set(), set())[0], [])
                finally:
                    os.chdir(before)

    def test_moby_policy_provenance_is_not_an_engine_release(self):
        for path in ('deploy/apparmor/olivares-sessions.conf', 'deploy/compose/README.md'):
            line = next(line for line in (ROOT / path).read_text().splitlines() if 'moby/moby' in line)
            with self.subTest(path=path):
                self.assertEqual(CHECKER['judge']('1.0', CHECKER['scan']([path], lambda _: line), set(), set())[0], [])
                # Dependency provenance must not waive another token on the line.
                hits = CHECKER['scan']([path], lambda _: line + '; install olivares:26.11.0')
                self.assertIn('26.11.0', [hit.token for hit in hits])
                self.assertTrue(CHECKER['judge']('1.0', hits, set(), set())[0])

    def test_prefixed_product_release_coordinates_are_rejected_and_never_stamped(self):
        # A prefix or a patch is no release the stamp can derive: it stays for the checker to
        # refuse. The export stamps before it checks, so a repair would launder the spelling.
        for coordinate in (
            '/releases/tag/v1.1',
            '/releases/download/v1.0.1/olivares.tar.gz',
            'https://github.com/olivaresai/olivares/releases/tag/v1.0',
            'https://github.com/olivaresai/olivares/releases/download/v1.0.1/olivares.tar.gz',
            'https://dl.example/olivares-v1.0.1_linux_amd64.tar.gz',
            'release version v1.1', 'release tag v1.0.1', 'release v0.1.0',
            'olivares v1.0.1', 'appVersion: "v1.0"',
        ):
            with self.subTest(coordinate=coordinate), tempfile.TemporaryDirectory() as folder:
                before = Path.cwd()
                try:
                    os.chdir(folder)
                    path = Path('docs/install.md')
                    path.parent.mkdir()
                    marked = 'Use <!-- release -->' + coordinate + '<!-- /release -->\n'
                    path.write_text(marked)
                    hits = CHECKER['scan']([str(path)], lambda p: Path(p).read_text())
                    self.assertEqual(len(hits), 1, coordinate)
                    self.assertEqual(len(CHECKER['judge']('1.0', hits, set(), set())[0]), 1)
                    CHECKER['stamp_surfaces']('1.0', hits)
                    self.assertEqual(path.read_text(), marked)
                    hits = CHECKER['scan']([str(path)], lambda p: Path(p).read_text())
                    self.assertEqual(len(CHECKER['judge']('1.0', hits, set(), set())[0]), 1)
                finally:
                    os.chdir(before)

    def test_prefixed_dependency_tags_stay_out_of_product_census_and_stamping(self):
        # A dependency a row names, or another project's URL, is no release of ours.
        dependencies = 'PostgreSQL v15.1; https://github.com/go-chi/chi/releases/tag/v1.1'
        hits = CHECKER['scan'](['docs/install.md'], lambda _: dependencies)
        self.assertEqual(CHECKER['judge']('1.0', hits, set(), set())[0], [])
        # One no row names is refused until a row names it or a mark claims it: the census fails closed.
        for unnamed in ('chi v1.1', 'install `v1.1`'):
            hits = CHECKER['scan'](['docs/install.md'], lambda _: unnamed)
            self.assertEqual(CHECKER['judge']('1.0', hits, set(), set())[0], [('docs/install.md', 1, 'v1.1')])
        with tempfile.TemporaryDirectory() as folder:
            before = Path.cwd()
            try:
                os.chdir(folder)
                path = Path('docs/install.md')
                path.parent.mkdir()
                original = dependencies + '; <!-- release -->release version 1.0<!-- /release -->\n'
                path.write_text(original)
                hits = CHECKER['scan']([str(path)], lambda p: Path(p).read_text())
                self.assertEqual([hit.token for hit in hits if hit.marked], ['1.0'])
                CHECKER['stamp_surfaces']('1.1', hits)
                self.assertEqual(path.read_text(), original.replace('version 1.0', 'version 1.1'))
            finally:
                os.chdir(before)

    def test_low_number_pins_and_variant_rows_are_stamped(self):
        files = {'packaging/aur/olivares-bin/.SRCINFO': 'pkgver = 1.0\n',
                 'packaging/docker/dockerhub-overview.md':
                     '| <!-- release -->`1.0`<!-- /release -->, `latest` | base |\n'
                     '| <!-- release -->`1.0-fips`<!-- /release --> | hardened |\n',
                 'CHANGELOG.md': 'The next release is <!-- release -->`1.0`<!-- /release -->.\n',
                 'deploy/manifests/install.yaml': '# release\nimage: olivaresai/olivares:1.0\n# /release\n'}
        before = Path.cwd()
        with tempfile.TemporaryDirectory() as folder:
            try:
                os.chdir(folder)
                for name, content in files.items():
                    Path(name).parent.mkdir(parents=True, exist_ok=True)
                    Path(name).write_text(content)
                hits = CHECKER['scan'](files, lambda p: Path(p).read_text())
                self.assertEqual({h.path for h in hits if h.marked}, set(files))
                CHECKER['stamp_surfaces']('1.1', hits)
                for name, content in files.items():
                    self.assertEqual(Path(name).read_text(), content.replace('1.0', '1.1'))
                self.assertTrue(CHECKER['scan_pins'](['deploy']))
            finally:
                os.chdir(before)

    def test_chart_package_and_dated_document_pins_are_not_release_coordinates(self):
        files = {'scripts/chart-fixture.sh': 'chart/olivares-1.0.0.tgz\n',
                 'docs-site/src/content/docs/2026-06/example.md': 'image: olivaresai/olivares:1.2.3\n'}
        before = Path.cwd()
        with tempfile.TemporaryDirectory() as folder:
            try:
                os.chdir(folder)
                for name, content in files.items():
                    Path(name).parent.mkdir(parents=True, exist_ok=True)
                    Path(name).write_text(content)
                self.assertEqual(CHECKER['scan_pins'](['scripts', 'docs-site']), [])
            finally:
                os.chdir(before)

    def test_chart_labels_preserve_independent_versions_without_exempting_engine_pins(self) -> None:
        for chart_version in ('0.2.4', '1.0', '26.10.2'):
            with self.subTest(chart_version=chart_version), tempfile.TemporaryDirectory() as folder:
                before = Path.cwd()
                try:
                    os.chdir(folder)
                    path = Path('deploy/manifests/install.yaml')
                    path.parent.mkdir(parents=True)
                    # The generator marks the whole render, the chart's own label included.
                    label = f'    helm.sh/chart: olivares-{chart_version}\n'
                    original = ('# release\n' + label + '    app.kubernetes.io/version: "1.0"\n'
                                '    image: olivaresai/olivares:1.0\n# /release\n')
                    path.write_text(original)
                    hits = CHECKER['scan']([str(path)], lambda p: Path(p).read_text())
                    pins = CHECKER['scan_pins'](['deploy'])
                    self.assertEqual([(hit.number, hit.token) for hit in pins], [(3, '1.0'), (4, '1.0')])
                    CHECKER['stamp_surfaces']('1.1', hits + pins)
                    self.assertIn(label, path.read_text())
                    self.assertIn('app.kubernetes.io/version: "1.1"', path.read_text())
                    self.assertIn('image: olivaresai/olivares:1.1', path.read_text())
                    # An unmarked engine pin beside the label is judged, and the stamp leaves it.
                    stale = label.rstrip() + ' # image: olivaresai/olivares:1.0\n'
                    path.write_text(stale)
                    pins = CHECKER['scan_pins'](['deploy'])
                    self.assertEqual([(hit.number, hit.token) for hit in pins], [(1, '1.0')])
                    self.assertTrue(CHECKER['judge_pins']('1.1', pins, set(), set()))
                    hits = CHECKER['scan']([str(path)], lambda p: Path(p).read_text())
                    CHECKER['stamp_surfaces']('1.1', hits + pins)
                    self.assertEqual(path.read_text(), stale)
                finally:
                    os.chdir(before)

    def test_flags_use_canon_and_commit_time_and_reject_flag_injection(self):
        helper = ROOT / 'scripts/build-ldflags.sh'
        result = subprocess.run(['sh', str(helper)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        canon = next(line for line in (ROOT / 'RELEASE-VERSION').read_text().splitlines() if line and not line.startswith('#'))
        self.assertIn('-X main.version=' + canon, result.stdout)
        git_commit = subprocess.run(['git', '-C', str(ROOT), 'rev-parse', '--short', 'HEAD'], capture_output=True, text=True)
        commit = git_commit.stdout.strip() if git_commit.returncode == 0 else 'none'
        self.assertIn('-X main.commit=' + commit, result.stdout)
        again = subprocess.check_output(['sh', str(helper)], text=True)
        self.assertEqual(result.stdout, again)
        git_epoch = subprocess.run(['git', '-C', str(ROOT), 'log', '-1', '--format=%ct'], capture_output=True, text=True)
        epoch = git_epoch.stdout.strip() if git_epoch.returncode == 0 else '0'
        pinned = subprocess.check_output(['sh', str(helper)], env=dict(os.environ, SOURCE_DATE_EPOCH=epoch), text=True)
        self.assertEqual(result.stdout, pinned)
        bad = subprocess.run(['sh', str(helper), '26.11 -X main.commit=wrong'], capture_output=True, text=True)
        self.assertNotEqual(bad.returncode, 0)
        self.assertEqual(bad.stdout, '')

    def test_canon_validation_and_source_archive(self):
        # A source archive must be outside the checkout: Git searches parent directories.
        with tempfile.TemporaryDirectory(dir='/tmp') as folder:
            root = Path(folder)
            (root / 'scripts/lib').mkdir(parents=True)
            for file in ('scripts/build-ldflags.sh', 'scripts/lib/git-env.sh'):
                shutil.copyfile(ROOT / file, root / file)
            helper = root / 'scripts/build-ldflags.sh'
            env = dict(os.environ, SOURCE_DATE_EPOCH='0')
            for invalid in ('UNDECIDED', '1.0.1', '26.11.0', 'v26.10.1', '26.9.0', '26.11\n26.12', ''):
                with self.subTest(invalid=invalid):
                    (root / 'RELEASE-VERSION').write_text(invalid + '\n')
                    result = subprocess.run(['sh', str(helper)], env=env, capture_output=True, text=True)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(result.stdout, '')
            for valid in ('1.0', '1.10', '1.299', '2.0', '26.13'):
                (root / 'RELEASE-VERSION').write_text('# Release\n' + valid + '\n')
                result = subprocess.check_output(['sh', str(helper)], env=env, text=True)
                self.assertEqual(result, f'-X main.version={valid} -X main.commit=none -X main.date=1970-01-01T00:00:00Z\n')

    def test_real_release_census_bump(self):
        # Read the production census, then render copies: never mutate the checkout.
        before = Path.cwd()
        try:
            os.chdir(ROOT)
            curated = CHECKER['curated_out']()
            kept = CHECKER['curation_kept']()
            docs = set(CHECKER['surface_files']())
            artifacts = set(CHECKER['artifact_files']())
            tops, _ = CHECKER['published_tops']()
            pin_hits = CHECKER['scan_pins'](tops)
            self.assertTrue(pin_hits, 'published pin census must not be empty')
            # The census also reads the private rows and the export curation they are checked against.
            files = sorted(docs | artifacts | {hit[0] for hit in pin_hits}
                           | {name for name in (CHECKER['PRIVATE_ROWS'], CHECKER['EXPORT_SCRIPT']) if Path(name).exists()})
            hits = CHECKER['scan'](sorted(docs | artifacts), CHECKER['read_surface'])
            content = {name: CHECKER['read_surface'](name) for name in files}
            with tempfile.TemporaryDirectory() as folder:
                os.chdir(folder)
                for name, value in content.items():
                    Path(name).parent.mkdir(parents=True, exist_ok=True)
                    Path(name).write_text(value)
                self.assertTrue(CHECKER['judge_pins']('1.1', pin_hits, curated, kept),
                                'the version bump must require published pins to change')
                CHECKER['stamp_surfaces']('1.1', hits + pin_hits)
                stamped_hits = CHECKER['scan'](files, lambda name: Path(name).read_text())
                self.assertEqual(CHECKER['judge']('1.1', [h for h in stamped_hits if h[0] in docs], curated, kept)[0], [])
                self.assertEqual(CHECKER['judge_artifacts']('1.1', [h for h in stamped_hits if h[0] in artifacts]), [])
                stamped_pins = CHECKER['scan_pins'](tops)
                self.assertEqual(len(stamped_pins), len(pin_hits))
                self.assertEqual(CHECKER['judge_pins']('1.1', stamped_pins, curated, kept), [])
                for name in ('README.md', 'README.es.md', 'SECURITY.md',
                             'deploy/compose/docker-compose.yml', 'packaging/docker/dockerhub-overview.md'):
                    self.assertIn('1.1', Path(name).read_text(), name)
                    self.assertNotIn('26.10.1', Path(name).read_text(), name)
                for name in ('CHANGELOG.md', 'docs/releases/26.10.1-install-surfaces.json'):
                    # The changelog masthead changes; its dated entries never do.
                    if name == 'CHANGELOG.md':
                        self.assertEqual(Path(name).read_text().split('## ', 1)[1], content[name].split('## ', 1)[1])
                    else:
                        self.assertEqual(Path(name).read_text(), content[name])
        finally:
            os.chdir(before)

    def test_every_row_names_what_it_versions_in_its_own_documents(self):
        # A row's example is the first literal it names in this tree, so no row is checked against an
        # invented sentence, and a row that names nothing is dead data. Inside a mark only the rows
        # naming a structure no release shares apply (the checker's INSIDE_MARKS, held here to its
        # contract); a row scoped to documents applies nowhere else, and never to a release claim
        # planted in one of them.
        inside_marks = {'an IPv4 address', 'a license identifier (SPDX)'}
        self.assertEqual(CHECKER['INSIDE_MARKS'], inside_marks)
        before = Path.cwd()
        try:
            os.chdir(ROOT)
            seen, published = {}, {}
            scoped = {name for name, paths, _ in CHECKER['NOT_RELEASES'] if paths is not None}
            curated, kept = CHECKER['curated_out'](), CHECKER['curation_kept']()
            for path in sorted(set(CHECKER['surface_files']()) | set(CHECKER['artifact_files']())):
                in_export = not CHECKER['removed_by_export'](path, curated, kept)
                for line in CHECKER['read_surface'](path).splitlines():
                    for match in CHECKER['tokens'](line):
                        name = CHECKER['not_release'](match, path)
                        if name:
                            seen.setdefault(name, (path, match))
                        if name in scoped and in_export and CHECKER['product_line'](match.group()):
                            published.setdefault(name, set()).add(match.group())
            for name, paths, _ in CHECKER['_NOT_RELEASES'] + CHECKER['private_rows']():
                with self.subTest(row=name):
                    self.assertIn(name, seen, 'a row that names no literal in this tree is dead data')
                    path, match = seen[name]
                    inside = CHECKER['not_release'](match, path, marked=True)
                    if name in inside_marks:
                        self.assertEqual(inside, name)
                    else:
                        self.assertNotEqual(inside, name)
                    if paths is None:
                        continue
                    self.assertNotEqual(CHECKER['not_release'](match, 'docs/elsewhere.md'), name)
                    planted = paths[0].replace('/??/', '/de/').replace('*', 'x.md')
                    hits = CHECKER['scan']([planted], lambda _: 'Olivares version 1.0.1 ships.\n')
                    self.assertEqual(CHECKER['judge']('1.0', hits, set(), set())[0], [(planted, 1, '1.0.1')])
            # A published document's row names each literal by its context, never the literal alone:
            # a claim of ours that reuses one of them in that document is refused, after the product's
            # name, and for a two-number literal (a release's shape) also after "lands in".
            for name, paths, _ in CHECKER['NOT_RELEASES']:
                planted = paths[0].replace('/??/', '/de/').replace('*', 'x.md') if paths else None
                for literal in sorted(published.get(name, ())):
                    claims = [f'Olivares {literal} ships.']
                    if re.fullmatch(r'v?[0-9]+\.[0-9]+', literal):
                        claims.append(f'It lands in {literal}.')
                    for claim in claims:
                        with self.subTest(row=name, claim=claim):
                            hits = CHECKER['scan']([planted], lambda _: claim + '\n')
                            self.assertTrue(CHECKER['judge']('1.0', hits, set(), set())[0], f'{planted}: {claim}')
        finally:
            os.chdir(before)

    def test_the_command_judges_and_stamps_every_census(self):
        # The command itself, on a copy of every file it reads: a red document, pin or artefact fails
        # the run, each planted where only its own census reads it, and --stamp moves all three
        # censuses, then checks the result.
        before = Path.cwd()
        try:
            os.chdir(ROOT)
            tops, _ = CHECKER['published_tops']()
            files = (set(CHECKER['surface_files']()) | set(CHECKER['artifact_files']())
                     | {hit.path for hit in CHECKER['scan_pins'](tops)} | set(CHECKER['crd_types_files']())
                     | set(CHECKER['COMPOSE_FILES'])
                     | {name for name in ('RELEASE-VERSION', 'PUBLIC-EXPORT.md', 'scripts/check-release-version.sh',
                                          CHECKER['HUB_LEG'], CHECKER['RETIRED_TAGS_FILE'], CHECKER['EXPORT_SCRIPT'],
                                          CHECKER['PRIVATE_ROWS'], CHECKER['WORDS_FILE']) if Path(name).exists()})
            with tempfile.TemporaryDirectory() as folder:
                for name in files:
                    Path(folder, name).parent.mkdir(parents=True, exist_ok=True)
                    shutil.copy2(name, Path(folder, name))

                def run(*args):
                    return subprocess.run(['sh', 'scripts/check-release-version.sh', *args], cwd=folder,
                                          capture_output=True, text=True)
                result = run()
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                # The plants below are prose, where only MAJOR 1 to 99 reads as a release (a bare
                # 0.N is a decimal), so a 0.x canon first stamps this copy forward to 1.0.
                version_file = Path(folder, 'RELEASE-VERSION')
                current = CHECKER['read_canon'](version_file.read_text())
                if current != '1.0':
                    version_file.write_text(version_file.read_text().replace('\n' + current + '\n', '\n1.0\n'))
                    result = run('--stamp')
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                for path, old, new in (('INSTALL.md', '', 'Olivares 1.0.1 is out.\n'),
                                       ('scripts/test-rpm-repository.sh', '', 'FIXTURE=olivares-1.0-1.x86_64.rpm\n'),
                                       ('deploy/compose/.env.example', '', '# olivares 1.0.1 is required\n')):
                    with self.subTest(red=path):
                        target = Path(folder, path)
                        original = target.read_text()
                        self.assertTrue(old in original)
                        target.write_text(original.replace(old, new, 1) if old else original + new)
                        result = run()
                        target.write_text(original)
                        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                        self.assertIn(path + ':', result.stdout)
                # The divergence note names the fix: a mark for a bare canon, a variants record only
                # for a hardened tag.
                target = Path(folder, 'INSTALL.md')
                original = target.read_text()
                for line, note in (('Olivares 1.0 is out.', 'the canon outside a release mark'),
                                   ('Pull olivaresai/olivares:1.0-fips.', "the canon's HARDENED tag")):
                    with self.subTest(note=line):
                        target.write_text(original + line + '\n')
                        result = run()
                        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                        self.assertEqual([row for row in result.stdout.splitlines() if 'INSTALL.md:' in row
                                          and note not in row], [])
                target.write_text(original)
                canon = Path(folder, 'RELEASE-VERSION')
                canon.write_text(canon.read_text().replace('\n1.0\n', '\n1.1\n'))
                result = run('--stamp')
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn('OK check-release-version', result.stdout.partition('Stamped')[2])
                for path, stamped in (('README.md', 'release-1.1-'),
                                      ('scripts/test-rpm-repository.sh', 'olivares-1.1-1.x86_64.rpm'),
                                      ('deploy/compose/docker-compose.yml', 'image: ${OLIVARES_IMAGE:-docker.io/olivaresai/olivares:1.1}')):
                    self.assertIn(stamped, Path(folder, path).read_text(), path)
        finally:
            os.chdir(before)

    def test_goreleaser_wrapper_preserves_policy_and_stamps_every_flag_form(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / 'scripts/lib').mkdir(parents=True)
            for file in ('scripts/build-ldflags.sh', 'scripts/lib/git-env.sh', 'scripts/goreleaser-build-wrapper.sh'):
                shutil.copyfile(ROOT / file, root / file)
            (root / 'RELEASE-VERSION').write_text('26.11\n')
            (root / 'scripts/build-connectors.sh').write_text('#!/bin/sh\nexit 0\n')
            (root / 'go').write_text('#!/bin/sh\nprintf "%s\\n" "$@"\n')
            (root / 'go').chmod(0o755)
            env = dict(os.environ, PATH=str(root) + ':' + os.environ['PATH'], GOOS='linux', GOARCH='amd64',
                       OLIVARES_BUILD_VERSION='26.11-snapshot', OLIVARES_BUILD_COMMIT='123abcd',
                       OLIVARES_BUILD_DATE='2026-10-01T00:00:00Z')
            for flags in (['-ldflags', '-s -w -X example.key=anchor'], ['-ldflags=-s -w'], []):
                args = subprocess.check_output(['bash', str(root / 'scripts/goreleaser-build-wrapper.sh'),
                                                'build', *flags, '-o', 'output with spaces', './cmd/olivares'],
                                               env=env, text=True).splitlines()
                self.assertIn('output with spaces', args)
                self.assertEqual(args[-1], './cmd/olivares')
                combined = ' '.join(args)
                self.assertIn('-X main.version=26.11-snapshot', combined)
                self.assertIn('-X main.commit=123abcd', combined)
                self.assertIn('-X main.date=2026-10-01T00:00:00Z', combined)
                if len(flags) == 2:
                    self.assertIn('-s -w -X example.key=anchor', combined)


if __name__ == '__main__':
    unittest.main()
