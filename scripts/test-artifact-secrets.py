#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Publication-boundary tests with synthetic canaries and the real scanner."""
import hashlib
import gzip
import io
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
import zipfile

ROOT = Path(__file__).resolve().parents[1]
CHECK = ROOT / 'scripts/check-artifact-secrets.py'
CANARY = 'ghp_' + hashlib.sha256(b'Olivares synthetic artifact canary').hexdigest()[:36]


def tar_bytes(files):
    stream = io.BytesIO()
    with tarfile.open(fileobj=stream, mode='w') as archive:
        for name, value in files.items():
            value = value.encode() if isinstance(value, str) else value
            member = tarfile.TarInfo(name)
            member.size = len(value)
            archive.addfile(member, io.BytesIO(value))
    return stream.getvalue()


class ArtifactSecrets(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='artifact-canary-')
        self.addCleanup(self.temporary.cleanup)
        self.scratch = Path(self.temporary.name)

    def check(self, *args, env=None):
        result = subprocess.run([sys.executable, str(CHECK), *map(str, args)],
                                capture_output=True, text=True, env=env)
        self.assertNotIn(CANARY, result.stdout + result.stderr)
        return result

    def test_clean_archive_passes(self):
        path = self.scratch / 'clean.tar.gz'
        with tarfile.open(path, 'w:gz') as archive:
            archive.addfile(tarfile.TarInfo('LICENSE'), io.BytesIO())
        self.assertEqual(self.check(path).returncode, 0)

    def test_nested_compressed_canary_blocks_without_logging_value(self):
        path = self.scratch / 'release.tar.gz'
        with tarfile.open(path, 'w:gz') as archive:
            data = tar_bytes({'nested/.env': 'token=' + CANARY})
            member = tarfile.TarInfo('inner.tar')
            member.size = len(data)
            archive.addfile(member, io.BytesIO(data))
        self.assertEqual(self.check(path).returncode, 1)

    def test_bundle_canary_blocks(self):
        path = self.scratch / 'console.zip'
        with zipfile.ZipFile(path, 'w', zipfile.ZIP_DEFLATED) as archive:
            archive.writestr('assets/main.js', 'const token="' + CANARY + '";')
        self.assertEqual(self.check(path).returncode, 1)

    def test_binary_canary_blocks(self):
        path = self.scratch / 'olivares'
        path.write_bytes(b'\x7fELF\x00\xff\x00token=' + CANARY.encode() + b'\x00\xff')
        self.assertEqual(self.check(path).returncode, 1)

    def test_current_german_locale_has_exact_provenance(self):
        spec = importlib.util.spec_from_file_location('artifact_gate', CHECK)
        gate = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(gate)
        finding = {'RuleID': 'generic-api-key', 'FileSHA256': 'a' * 64,
                   'MatchSHA256': 'bb8d6b1587c15b8b30aecb426e47a772f90f034bffc1686bf4104877b9c726cb',
                   'Tags': []}
        rows = [row for row in gate.pinned_exemptions()
                if row['match_sha256'] == finding['MatchSHA256']]
        expected = {'kind': 'repository', 'path': 'web/src/features/nis2/i18n/de.json',
                    'line': 6,
                    'sha256': '2aec98618d03358bf02f54fb1f248ff44d4e33cadd0387c194e0204d7acb058a'}
        self.assertTrue(rows)
        for row in rows:
            self.assertEqual(row['sources'], [expected])
            self.assertEqual(row['max_occurrences_per_member'], 1)
        # The positive Business replay uses the actual assembled locale; the
        # release canary suite must also run in a Community checkout without it.

    def test_announced_binary_matches_have_exact_provenance(self):
        spec = importlib.util.spec_from_file_location('artifact_gate', CHECK)
        gate = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(gate)
        matches = [
            '254d512a3baf6a7f8634c94128b16363e2180fc78f313d35c3f86f11fb3912e5',
            '88ab51444d7b2bdd1797891876872e71de5c4d368b3a03a6b52f14b7848890b5',
            '1baeac79a6986396ba786d67029f862b71f0a15956527bcb2ae41a99906d806e',
            '79fc498090d9d2ebf56dc324f5d1ad9b84c4d813b7943dccec8b65cc1c8df2a0',
            'c7541c0f92af95c6194910426ad7544c2d578085b05e30d029148fbce6a55c09',
        ]
        for digest in matches:
            rule = 'private-key' if digest == matches[2] else 'generic-api-key'
            with self.subTest(rule=rule, digest=digest):
                rows = [row for row in gate.pinned_exemptions()
                        if row['rule'] == rule and row['match_sha256'] == digest]
                self.assertEqual(len(rows), 1)
                self.assertEqual(rows[0]['member_role'], 'engine binary olivares')
                self.assertEqual(rows[0]['max_occurrences_per_member'], 1)
                self.assertEqual(rows[0]['class'], 'third-party public test key'
                                 if rule == 'private-key' else 'random-looking non-secret')
                # Eligibility additionally requires these real source files.
                # Native replays verify them; this metadata regression must not
                # require a populated Go cache before release modules download.
                self.assertTrue(rows[0]['sources'])

    def pinned_fixture(self):
        checkout = self.scratch / 'checkout'
        scripts = checkout / 'scripts'
        scripts.mkdir(parents=True)
        for name in ('check-artifact-secrets.py', 'artifact-secrets.toml',
                     'package_repository_lib.py'):
            shutil.copyfile(ROOT / 'scripts' / name, scripts / name)
        source = checkout / 'fixture-source.txt'
        source.write_text('Public synthetic test fixture.\n')
        row = {'rule': 'github-pat', 'member_role': 'engine binary olivares',
               'max_occurrences_per_member': 1,
               'match_sha256': hashlib.sha256(CANARY.encode()).hexdigest(),
               'sources': [{'kind': 'repository', 'path': source.name, 'line': 1,
                            'sha256': hashlib.sha256(source.read_bytes()).hexdigest()}],
               'class': 'test fixture', 'reason': 'Synthetic regression test fixture.'}
        pins = scripts / 'artifact-secrets-exemptions.json'
        pins.write_text(json.dumps({'version': 1, 'scanned_tree': 'a' * 40, 'findings': [row]}))
        second = 'ghp_' + hashlib.sha256(b'Another synthetic artifact canary').hexdigest()[:36]

        def attempt(values, member='olivares', archive_name='fixture.tar.gz'):
            artifact = self.scratch / archive_name
            # The detector recognizes ELF only above its minimum header length.
            # Keep one finding in the printable representation of this fixture.
            artifact.write_bytes(gzip.compress(tar_bytes({member: b'\x7fELF' + b'\x00' * 64 +
                                  b'\x00'.join(value.encode() for value in values) + b'\x00\xff'})))
            result = subprocess.run([sys.executable, str(scripts / CHECK.name), str(artifact)],
                                    capture_output=True, text=True)
            for value in (CANARY, second):
                self.assertNotIn(value, result.stdout + result.stderr)
            return result.returncode

        return source, attempt, second

    def test_pinned_fixture_exempts_only_one_exact_finding(self):
        # A path-wide waiver or a missing hash/source/count check would let
        # one of these real-scanner publication attempts through.
        source, attempt, second = self.pinned_fixture()
        self.assertEqual(attempt([CANARY]), 0, 'the exact pinned fixture must pass')
        self.assertEqual(attempt([CANARY, second]), 1, 'a new finding must block')
        self.assertEqual(attempt([CANARY, CANARY]), 1, 'an extra identical finding must block')
        self.assertEqual(attempt([second]), 1, 'a changed match hash must block')
        self.assertEqual(attempt([CANARY], 'renamed'), 1, 'a different member must block')
        source.write_text('\nPublic synthetic test fixture.\n')
        self.assertEqual(attempt([CANARY]), 1, 'a moved source line must block')
        source.unlink()
        self.assertEqual(attempt([CANARY]), 1, 'a missing source must block')

    def test_renamed_release_archive_keeps_pinned_member(self):
        _, attempt, _ = self.pinned_fixture()
        self.assertEqual(attempt([CANARY], archive_name='olivares_26.10.0_linux_amd64.tar.gz'), 0)
        self.assertEqual(attempt([CANARY], archive_name='olivares_26.10.1_linux_amd64.tar.gz'), 0)

    def test_same_hash_at_moved_source_path_blocks(self):
        source, attempt, _ = self.pinned_fixture()
        self.assertEqual(attempt([CANARY]), 0)
        original_sha = hashlib.sha256(source.read_bytes()).hexdigest()
        moved = source.with_name('moved-source.txt')
        source.rename(moved)
        self.assertEqual(hashlib.sha256(moved.read_bytes()).hexdigest(), original_sha)
        self.assertEqual(attempt([CANARY]), 1, 'the same hash at another source path must block')

    def test_source_and_inline_exemptions_do_not_apply(self):
        path = self.scratch / 'fixture.txt'
        path.write_text('token=' + CANARY + ' # gitleaks:allow\n')
        (self.scratch / '.gitleaksignore').write_text('*\n')
        env = dict(os.environ, GITLEAKS_CONFIG_TOML='[allowlist]\npaths=[".*"]')
        self.assertEqual(self.check(path, env=env).returncode, 1)

    def test_default_file_exemptions_do_not_hide_printable_secrets(self):
        for name in ('go.sum', 'payload.exe', 'package-lock.json'):
            with self.subTest(name=name):
                path = self.scratch / name
                path.write_text('token=' + CANARY)
                self.assertEqual(self.check(path).returncode, 1)

    def test_finding_locations_do_not_disclose_secret_file_names(self):
        path = self.scratch / (CANARY + '.txt')
        path.write_text('token=' + CANARY)
        self.assertEqual(self.check(path).returncode, 1)

    def test_missing_empty_and_malformed_input_blocks(self):
        self.assertEqual(self.check().returncode, 2)
        self.assertEqual(self.check(self.scratch / 'absent').returncode, 2)
        path = self.scratch / 'broken.tar.gz'
        path.write_bytes(b'\x1f\x8b\x08broken')
        self.assertEqual(self.check(path).returncode, 2)
        path = self.scratch / 'broken.tar'
        path.write_bytes(tar_bytes({'clean.txt': 'clean'}) + b'garbage'.ljust(512, b'\0'))
        self.assertEqual(self.check(path).returncode, 2)
        path.write_bytes(tar_bytes({'clean.txt': 'clean'})[:513])
        self.assertEqual(self.check(path).returncode, 2)

    def test_checksum_mutation_blocks(self):
        path = self.scratch / 'LICENSE'
        path.write_text('clean')
        checksum = self.scratch / 'checksums.txt'
        checksum.write_text(hashlib.sha256(b'clean').hexdigest() + '  LICENSE\n')
        self.assertEqual(self.check('--checksums', checksum).returncode, 0)
        path.write_text('changed')
        self.assertEqual(self.check('--checksums', checksum).returncode, 2)

    def test_missing_scanner_blocks(self):
        path = self.scratch / 'plain.txt'
        path.write_text('clean')
        self.assertEqual(self.check(path, env=dict(os.environ, PATH=str(self.scratch))).returncode, 2)

    def test_native_packages_are_unpacked_and_scanned(self):
        nfpm = shutil.which('nfpm')
        self.assertIsNotNone(nfpm, 'native-package canary requires the pinned nFPM')
        payload = self.scratch / 'canary.txt'
        payload.write_text('token=' + CANARY)
        config = self.scratch / 'package.json'
        config.write_text(json.dumps({'name': 'artifact-canary', 'arch': 'amd64', 'version': '1.0.0',
                                     'maintainer': 'Olivares <test@example.invalid>',
                                     'description': 'Synthetic canary',
                                     'contents': [{'src': str(payload), 'dst': '/usr/share/canary.txt'}]}))
        for fmt, suffix in [('deb', '.deb'), ('rpm', '.rpm'), ('apk', '.apk'),
                            ('archlinux', '.pkg.tar.zst')]:
            with self.subTest(format=fmt):
                package = self.scratch / ('canary' + suffix)
                subprocess.run([nfpm, 'package', '--config', str(config), '--packager', fmt,
                                '--target', str(package)], check=True, capture_output=True)
                if fmt == 'archlinux' and sys.version_info < (3, 14):
                    self.assertEqual(self.check(package).returncode, 2,
                                     'an unavailable zstd reader must block publication')
                else:
                    self.assertEqual(self.check(package).returncode, 1)

    def test_saved_image_includes_deleted_layer_and_blocks_push(self):
        saved = self.scratch / 'saved.tar'
        saved.write_bytes(tar_bytes({'blobs/sha256/old': gzip.compress(tar_bytes({'root/.codex/auth.json': CANARY})),
                                    'new/layer.tar': tar_bytes({'root/.codex/.wh.auth.json': ''}),
                                    'manifest.json': '[{"Layers":["blobs/sha256/old","new/layer.tar"]}]'}))
        self.assertEqual(self.check(saved).returncode, 1)
        transport = self.scratch / 'docker'
        marker = self.scratch / 'pushed'
        transport.write_text('#!' + sys.executable + '\n' +
                             'import os, pathlib, shutil, sys\n' +
                             'if sys.argv[1:3] == ["image", "inspect"]:\n' +
                             ' print("sha256:" + "a" * 64)\n' +
                             'elif sys.argv[1:3] == ["image", "save"]:\n' +
                             ' shutil.copyfile(os.environ["TEST_SAVED"], sys.argv[4])\n' +
                             'elif sys.argv[1] == "push":\n' +
                             ' pathlib.Path(os.environ["TEST_PUSHED"]).touch()\n')
        transport.chmod(0o755)
        launcher = ROOT / 'scripts/artifact-scan-bin/docker'
        env = dict(os.environ, OLIVARES_REAL_DOCKER=str(transport), TEST_SAVED=str(saved),
                   TEST_PUSHED=str(marker))
        result = subprocess.run([str(launcher), 'push', 'example.invalid/canary:1'],
                                env=env, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(marker.exists(), 'secret-bearing image reached Docker push')
        self.assertNotIn(CANARY, result.stdout + result.stderr)
        saved.write_bytes(tar_bytes({'layer.tar': tar_bytes({'LICENSE': 'clean'}), 'config.json': '{}'}))
        result = subprocess.run([str(launcher), 'push', 'example.invalid/canary:1'],
                                env=env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(marker.exists(), 'clean image did not reach Docker push')
        marker.unlink()
        result = subprocess.run([str(launcher), 'buildx', 'build', '--push', '.'],
                                env=env, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(marker.exists())


if __name__ == '__main__':
    unittest.main(verbosity=2)
