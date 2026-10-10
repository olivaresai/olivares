#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Exercise draft admission and the complete signed checksum upload, without writes."""
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class NativePublication(unittest.TestCase):
    def run_publication(self, scenario: str, profile: dict[str, str] | None = None) -> tuple:
        with tempfile.TemporaryDirectory(prefix='native-publication-', dir=os.environ.get('TMPDIR')) as tmp:
            root = Path(tmp)
            (root / 'scripts').mkdir()
            (root / 'bin').mkdir()
            (root / 'dist').mkdir()
            (root / 'scripts/build-native-release-packages.py').write_text(
                'import json, os, pathlib, sys\n'
                'pathlib.Path("built").write_text(json.dumps(sys.argv[1:]))\n'
                'if os.environ["SCENARIO"] == "boundary": sys.exit(19)\n')
            shutil.copyfile(ROOT / 'scripts/cosign-verified.sh', root / 'scripts/cosign-verified.sh')
            (root / 'scripts/check-artifact-secrets.py').write_text(
                'import os, pathlib, sys\npathlib.Path("scanned").touch()\n'
                'sys.exit(1 if os.environ["SCENARIO"] == "secret" else 0)\n')
            (root / 'scripts/assert-cosign-binary.sh').write_text('exit 0\n')
            cosign = root / 'bin/cosign'
            cosign.write_text('#!/bin/sh\ntest -f scanned || exit 1\ntouch signed\n')
            cosign.chmod(0o755)
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
        'tag_name': os.environ['RELEASE_TAG'], 'draft': scenario != 'published' and not (scenario == 'published-later' and reads), 'prerelease': False}
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
                       OLIVARES_COSIGN_BIN=str(cosign), COSIGN_MODE='keyless',
                       RELEASE_GITHUB_OWNER='example', RELEASE_GITHUB_NAME='fixture', RELEASE_TAG='26.11', RELEASE_VERSION='26.11', SCENARIO=scenario)
            env.update(profile or {})
            result = subprocess.run(['bash', str(ROOT / 'scripts/publish-native-release-packages.sh')], env=env, text=True, capture_output=True)
            if scenario == 'boundary':
                args = json.loads((root / 'built').read_text()) if (root / 'built').exists() else None
                return result, args, (root / 'signed').exists(), (root / 'upload.json').exists()
            return result, (root / 'built').exists(), (root / 'signed').exists(), json.loads((root / 'upload.json').read_text()) if (root / 'upload.json').exists() else None

    def rehearsal_profile(self, tag: str) -> dict[str, str]:
        # Match the workflow's reviewed mode and switches, but keep its private
        # destination out of this exported battery. The public preflight requires
        # the injected neutral tuple and exercises all containment checks.
        profile = dict(RELEASE_MODE='rehearsal', COSIGN_MODE='key', COSIGN_TLOG_UPLOAD='false',
                       PUBLISH_LATEST='false', PUBLISH_DOCKERHUB='false', PUBLISH_HOMEBREW='false',
                       PUBLISH_OTA_STABLE='false', RUN_SLSA='true')
        repo = 'fixture-org/rehearsal'
        profile.update(RELEASE_GITHUB_REPO=repo, OCI_IMAGE_REPO='ghcr.io/' + repo,
                       MIRROR_IMAGE_REPO='ghcr.io/' + repo + '/mirror',
                       HOMEBREW_TAP_REPO='fixture-org/homebrew-rehearsal',
                       SOURCE_REPOSITORY_URL='https://github.com/' + repo,
                       OLIVARES_REHEARSAL_EXPECTED_REPO=repo,
                       OLIVARES_REHEARSAL_EXPECTED_OCI='ghcr.io/' + repo,
                       OLIVARES_REHEARSAL_EXPECTED_SOURCE='https://github.com/' + repo,
                       RELEASE_TAG=tag, GITHUB_REPOSITORY=repo, GITHUB_REF='refs/tags/' + tag,
                       GITHUB_REF_NAME=tag, GITHUB_REF_TYPE='tag', GITHUB_SHA='a' * 40,
                       ACKNOWLEDGE_PUBLIC_SLSA_LOG='true')
        with tempfile.TemporaryDirectory(prefix='rehearsal-preflight-', dir=os.environ.get('TMPDIR')) as tmp:
            output = Path(tmp) / 'output'
            result = subprocess.run(['bash', str(ROOT / 'scripts/release-preflight.sh')],
                                    env=dict(profile, PATH=os.environ['PATH'], GITHUB_OUTPUT=str(output)),
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            return dict(line.split('=', 1) for line in output.read_text().splitlines())

    def test_actual_rehearsal_preflight_reaches_native_snapshot_boundary(self) -> None:
        spec = importlib.util.spec_from_file_location('native_builder', ROOT / 'scripts/build-native-release-packages.py')
        builder = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(builder)
        for tag in ('1.0', '1.1', '1.10'):
            with self.subTest(tag=tag):
                outputs = self.rehearsal_profile(tag)
                self.assertEqual(outputs['release_tag'], tag)
                self.assertEqual(outputs['release_version'], tag)
                profile = {key.upper(): value for key, value in outputs.items()}
                result, args, signed, uploaded = self.run_publication('boundary', profile)
                self.assertEqual(result.returncode, 19, result.stderr)
                self.assertEqual(args, ['--version', tag, '--dist', 'dist', '--snapshot'])
                self.assertTrue(builder.valid_version(args[1], True))
                self.assertFalse(signed or uploaded)

    def test_production_native_boundary_does_not_enable_snapshot(self) -> None:
        profile = dict(RELEASE_MODE='production', RELEASE_TAG='1.0', RELEASE_VERSION='1.0')
        result, args, signed, uploaded = self.run_publication('boundary', profile)
        self.assertEqual(result.returncode, 19, result.stderr)
        self.assertEqual(args, ['--version', '1.0', '--dist', 'dist'])
        self.assertFalse(signed or uploaded)

    def test_production_rejects_nonrelease_versions_before_build(self) -> None:
        for value in ('1.0.1', '26.10.2', 'v1.0', '1.0-rc.1', '1.0-SNAPSHOT-abc123'):
            with self.subTest(version=value):
                result, built, signed, uploaded = self.run_publication(
                    'healthy', dict(RELEASE_MODE='production', RELEASE_TAG=value, RELEASE_VERSION=value))
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('version must be MAJOR.MINOR', result.stderr)
                self.assertFalse(built or signed)
                self.assertIsNone(uploaded)

    def test_workflow_passes_validated_mode_to_native_publisher(self) -> None:
        # export-closure: absent-by-design .github/workflows/release-rehearsal.yml — internal rehearsal workflow; the public export removes it and this test skips there
        workflow = ROOT / '.github/workflows/release-rehearsal.yml'
        if not workflow.exists() and not workflow.is_symlink():
            result = subprocess.run(['bash', str(ROOT / 'scripts/hub-leg.sh'), '--classify', '--root', str(ROOT)],
                                    capture_output=True, text=True)
            self.assertEqual((result.returncode, result.stdout.strip()), (0, 'public'), result.stderr)
            self.skipTest('internal rehearsal workflow is absent by design in the classified public export')
        source = workflow.read_text()
        job_env = source.split('  goreleaser-rehearsal:\n', 1)[1].split('\n    env:\n', 1)[1].split('    steps:', 1)[0]
        self.assertIn('RELEASE_MODE: ${{ needs.preflight.outputs.release_mode }}', job_env)
        self.assertIn('Rehearsal tag, bare MAJOR.MINOR', source)

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

    def test_secret_finding_refuses_before_signing_and_upload(self):
        result, built, signed, upload = self.run_publication('secret')
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(built)
        self.assertFalse(signed)
        self.assertIsNone(upload)

    def test_draft_replaced_or_published_before_upload_refuses(self):
        for scenario in ('replaced', 'published-later', 'duplicated'):
            with self.subTest(scenario=scenario):
                result, _, _, upload = self.run_publication(scenario)
                self.assertNotEqual(result.returncode, 0)
                self.assertIsNone(upload)


if __name__ == '__main__':
    unittest.main(verbosity=2)
