#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Native Helm output and release stamping must agree on the flat install manifest."""
import os
import runpy
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
CHECKER = runpy.run_path(str(ROOT / 'scripts/test-release-version-generation.py'))['CHECKER']


class ReleaseManifestStamping(unittest.TestCase):
    def test_committed_manifest_matches_native_render(self) -> None:
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            shutil.copytree(ROOT / 'deploy/helm/olivares', root / 'deploy/helm/olivares')
            (root / 'scripts').mkdir()
            shutil.copyfile(ROOT / 'scripts/gen-install-manifest.sh', root / 'scripts/gen-install-manifest.sh')
            env = dict(os.environ, KUBE_VERSION='1.29.0', OLIVARES_NAMESPACE='olivares-system')
            subprocess.run(['sh', str(root / 'scripts/gen-install-manifest.sh')], env=env, check=True)
            self.assertEqual((root / 'deploy/manifests/install.yaml').read_bytes(),
                             (ROOT / 'deploy/manifests/install.yaml').read_bytes(),
                             'committed manifest must match native Helm output')

    def test_native_render_stamp_round_trip(self) -> None:
        self.assertIsNotNone(shutil.which('helm'), 'native Helm is required for this gate')
        before = Path.cwd()
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            shutil.copytree(ROOT / 'deploy/helm/olivares', root / 'deploy/helm/olivares')
            (root / 'scripts').mkdir()
            shutil.copyfile(ROOT / 'scripts/gen-install-manifest.sh', root / 'scripts/gen-install-manifest.sh')
            env = dict(os.environ, KUBE_VERSION='1.29.0', OLIVARES_NAMESPACE='olivares-system')
            try:
                os.chdir(root)
                subprocess.run(['sh', 'scripts/gen-install-manifest.sh'], env=env, check=True)
                manifest = Path('deploy/manifests/install.yaml')
                chart = Path('deploy/helm/olivares/Chart.yaml')
                chart_version = next(line for line in chart.read_text().splitlines() if line.startswith('version:'))
                labels = [line for line in manifest.read_text().splitlines() if 'helm.sh/chart:' in line]
                self.assertTrue(labels, 'native render must emit chart metadata')
                canon = CHECKER['read_canon']((ROOT / 'RELEASE-VERSION').read_text())
                for version in (canon, '1.1'):
                    with self.subTest(version=version):
                        files = [str(chart), str(manifest)]
                        hits = CHECKER['scan'](files, CHECKER['read_surface'])
                        pins = CHECKER['scan_pins'](['deploy'])
                        CHECKER['stamp_surfaces'](version, hits + pins)
                        self.assertIn(chart_version, chart.read_text().splitlines())
                        self.assertEqual(labels, [line for line in manifest.read_text().splitlines()
                                                  if 'helm.sh/chart:' in line])
                        self.assertEqual(CHECKER['judge_artifacts'](
                            version, CHECKER['scan'](files, CHECKER['read_surface'])), [])
                        self.assertEqual(CHECKER['judge_pins'](
                            version, CHECKER['scan_pins'](['deploy']), set(), set()), [])
                        stamped = manifest.read_bytes()
                        subprocess.run(['sh', 'scripts/gen-install-manifest.sh'], env=env, check=True)
                        self.assertEqual(stamped, manifest.read_bytes(),
                                         'release stamping must preserve the native Helm render')
            finally:
                os.chdir(before)


if __name__ == '__main__':
    unittest.main()
