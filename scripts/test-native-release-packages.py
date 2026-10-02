#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Named, offline native-version qualification with the real pinned release tools."""
import ctypes
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest

import package_repository_lib as packages
from importlib.util import spec_from_file_location, module_from_spec
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
_spec = spec_from_file_location('native_builder', ROOT / 'scripts/build-native-release-packages.py')
builder = module_from_spec(_spec)
_spec.loader.exec_module(builder)
VERSIONS = ('26.9.0', '26.10.0', '26.10.1', '26.11', '26.11.1', '26.12', '27.1')


def numeric_segments(version):
    # Restricted to these numeric CalVer releases. RPM compares numeric segments
    # numerically, then segment count (rpm-version(7)); APK compares successive
    # numeric tokens (apk-tools src/version.c). No general SemVer emulation here.
    return tuple(int(part) for part in version.split('.'))


def apt_upgrade_offer(package, version, parent):
    # An isolated APT state models the already-published 26.10.0 installation.
    # No host package state, configured source or network is used.
    state = parent / ('apt-' + version)
    for directory in ('lists/partial', 'archives/partial', 'sourceparts', 'preferencesparts', 'configparts'):
        (state / directory).mkdir(parents=True)
    (state / 'apt.conf').write_text(f'Dir::Etc::parts {json.dumps(str(state / "configparts"))};\nDir::Etc::main {json.dumps(str(state / "apt.conf"))};\n')
    env = dict(os.environ, APT_CONFIG=str(state / 'apt.conf'), LC_ALL='C')
    shutil.copyfile(package, state / package.name)
    control = subprocess.check_output(['dpkg-deb', '-f', str(package)], text=True)
    (state / 'Packages').write_text(control + f'Filename: {package.name}\nSize: {package.stat().st_size}\nSHA256: {packages.digest_file(package)}\n\n')
    (state / 'status').write_text('Package: olivares\nStatus: install ok installed\nArchitecture: amd64\nVersion: 26.10.0\nDescription: installed release fixture\n\n')
    (state / 'sources.list').write_text(f'deb [trusted=yes] file:{state} ./\n')
    options = ['-o', f'Dir::State={state}', '-o', f'Dir::State::status={state / "status"}',
               '-o', f'Dir::Cache={state}', '-o', f'Dir::Etc::sourcelist={state / "sources.list"}',
               '-o', f'Dir::Etc::sourceparts={state / "sourceparts"}', '-o', f'Dir::Etc::preferencesparts={state / "preferencesparts"}',
               '-o', f'Dir::Etc::parts={state / "configparts"}', '-o', f'Dir::Etc::main={state / "apt.conf"}',
               '-o', 'APT::Architecture=amd64', '-o', 'APT::Sandbox::User=root', '-o', 'Debug::NoLocking=true']
    config = subprocess.check_output(['apt-config', *options, 'dump'], env=env, text=True)
    if 'Post-Invoke' in config:
        raise AssertionError('isolated APT fixture loaded host hooks')
    subprocess.run(['apt-get', *options, 'update', '-qq'], env=env, check=True)
    policy = subprocess.check_output(['apt-cache', *options, 'policy', 'olivares'], env=env, text=True)
    if 'Installed: 26.10.0' not in policy or f'Candidate: {version}' not in policy:
        raise AssertionError(policy)
    upgrade = subprocess.check_output(['apt-get', *options, '-s', '--no-install-recommends', 'upgrade'], env=env, text=True)
    if f'Inst olivares [26.10.0] ({version} ' not in upgrade:
        raise AssertionError(upgrade)
    print(f'APT isolated repository: installed 26.10.0 -> candidate {version}; upgrade simulation offers the actual package')


def arch_version(path):
    zstd = ctypes.CDLL('libzstd.so.1')
    zstd.ZSTD_decompress.argtypes = [ctypes.c_void_p, ctypes.c_size_t, ctypes.c_void_p, ctypes.c_size_t]
    zstd.ZSTD_decompress.restype = ctypes.c_size_t
    zstd.ZSTD_isError.argtypes = [ctypes.c_size_t]
    zstd.ZSTD_isError.restype = ctypes.c_uint
    data = path.read_bytes()
    output = ctypes.create_string_buffer(16 * 1024 * 1024)
    size = zstd.ZSTD_decompress(output, len(output), data, len(data))
    if zstd.ZSTD_isError(size):
        raise ValueError('could not inspect the real Arch package')
    with tarfile.open(fileobj=io.BytesIO(output.raw[:size])) as bundle:
        text = bundle.extractfile('.PKGINFO').read().decode()
    return next(line.split(' = ', 1)[1] for line in text.splitlines() if line.startswith('pkgver = ')).rsplit('-', 1)[0]


class NativeReleaseVersions(unittest.TestCase):
    def test_producer_shapes_and_historical_zero(self):
        for version in ('26.11', '26.11.1', '26.12', '27.1', '26.10.0', '26.9.0'):
            self.assertTrue(builder.valid_version(version, False), version)
        for version in ('26.11.0', '27.1.0', 'v26.11', '26.01', '26.13', '26.11.01', '26.11.1.1'):
            self.assertFalse(builder.valid_version(version, False), version)

    def test_native_upgrade_order(self):
        for left, right in zip(VERSIONS, VERSIONS[1:]):
            self.assertLess(numeric_segments(left), numeric_segments(right))
            subprocess.run(['dpkg', '--compare-versions', left, 'lt', right], check=True)
            if shutil.which('rpm'):
                result = subprocess.check_output(['rpm', '--eval', f'%{{lua:print(rpm.vercmp("{left}", "{right}"))}}'], text=True)
                self.assertLess(int(result.strip()), 0)
            if shutil.which('apk'):
                self.assertEqual(subprocess.check_output(['apk', 'version', '-t', left, right], text=True).strip(), '<')
        print('native comparators: dpkg real; RPM/APK real when installed, otherwise documented numeric-token domain')

    def test_real_monthly_and_patch_package_metadata_and_reproducibility(self):
        with tempfile.TemporaryDirectory(prefix='native-version-', dir=os.environ.get('TMPDIR')) as tmp:
            project = Path(tmp)
            (project / 'go.mod').write_text('module example.invalid/nativefixture\ngo 1.26\n')
            (project / 'main.go').write_text('package main\nimport "fmt"\nvar version="dev"\nfunc main(){fmt.Println("olivares "+version)}\n')
            for command in (['git', 'init', '-q'], ['git', 'add', '.'],
                ['git', '-c', 'core.hooksPath=', '-c', 'commit.gpgsign=false', '-c', 'user.name=fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-q', '--no-verify', '-m', 'test: native fixture'],
                ['git', 'remote', 'add', 'origin', 'https://example.invalid/olivares/olivares.git']):
                subprocess.run(command, cwd=project, check=True)
            goreleaser = subprocess.check_output(['bash', str(ROOT / 'scripts/ensure-goreleaser.sh'), '--dest', str(project / 'tools')], text=True).strip()
            for version in ('26.11', '26.11.1'):
                config = {'version': 2, 'project_name': 'olivares', 'snapshot': {'version_template': version},
                    'checksum': {'name_template': 'checksums.txt', 'algorithm': 'sha256'},
                    'builds': [{'id': 'base', 'main': '.', 'binary': 'olivares', 'env': ['CGO_ENABLED=0'],
                        'ldflags': ['-X main.version={{ .Version }}'], 'goos': ['linux'], 'goarch': ['amd64', 'arm64']}],
                    'archives': [{'ids': ['base'], 'formats': ['tar.gz'], 'name_template': 'olivares_{{ .Version }}_{{ .Os }}_{{ .Arch }}'}]}
                (project / '.goreleaser.yaml').write_text(json.dumps(config))
                subprocess.run([goreleaser, 'check'], cwd=project, check=True)
                env = dict(os.environ, GORELEASER_CURRENT_TAG=version, GORELEASER_PREVIOUS_TAG='26.10.0')
                subprocess.run([goreleaser, 'release', '--snapshot', '--clean', '--parallelism', '2',
                    '--skip=before,publish,sign,sbom,docker,validate,homebrew,ko,nix,scoop,snapcraft,winget,aur,announce,notarize,chocolatey,flatpak,makeself,mcp,srpm'], cwd=project, env=env, check=True)
                dist = project / 'dist'
                nfpm = subprocess.check_output(['bash', str(ROOT / 'scripts/ensure-nfpm.sh'), '--dest', str(project / 'tools')], text=True).strip()
                with patch.dict(os.environ, OLIVARES_NFPM=nfpm):
                    paths = builder.build(ROOT, dist, version, 1790812800, ROOT / 'packaging/nfpm/packages.json', False)
                self.assertEqual(len(paths), 7)
                for path in paths:
                    if path.name.endswith('.pkg.tar.zst'):
                        got = arch_version(path)
                    else:
                        fmt = path.suffix[1:]
                        obj = getattr(packages, 'parse_' + fmt)(path, {'source_name': path.name, 'source_path': path,
                            'format': fmt, 'asset_arch': 'arm64' if '_arm64.' in path.name else 'amd64',
                            'sha256': packages.digest_file(path), 'size': path.stat().st_size})
                        got = packages._base_version(obj)
                    self.assertEqual(got, version, path.name)
                self.assertEqual(len(packages.load_release_packages(dist / 'checksums.txt', dist, version)), 6)
                apt_upgrade_offer(dist / f'olivares_{version}_linux_amd64.deb', version, project)
                for name in ('base', 'portal'):
                    raw = (ROOT / f'packaging/nfpm/olivares-appliance-{name}.yaml').read_text()
                    config = json.loads('\n'.join(line for line in raw.splitlines() if not line.startswith('#')))
                    config.update(arch='amd64', version=version, mtime='2026-10-01T00:00:00Z')
                    for content in config['contents']:
                        if 'src' in content:
                            content['src'] = str(project / 'main.go' if content['src'].startswith('bin/') else ROOT / content['src'])
                    for event, source in config['scripts'].items():
                        config['scripts'][event] = str(ROOT / source)
                    config_file = project / f'appliance-{name}.json'
                    config_file.write_text(json.dumps(config))
                    for fmt in ('deb', 'rpm'):
                        path = project / f'appliance-{name}.{fmt}'
                        subprocess.run([nfpm, 'package', '--config', str(config_file), '--packager', fmt, '--target', str(path)], check=True)
                        obj = getattr(packages, 'parse_' + fmt)(path, {'source_name': path.name, 'source_path': path, 'format': fmt,
                            'asset_arch': 'amd64', 'sha256': packages.digest_file(path), 'size': path.stat().st_size})
                        self.assertEqual(packages._base_version(obj), version, path.name)

                hashes = {p.name: packages.digest_file(p) for p in paths}
                with patch.dict(os.environ, OLIVARES_NFPM=nfpm):
                    repeated = builder.build(ROOT, dist, version, 1790812800, ROOT / 'packaging/nfpm/packages.json', False)
                self.assertEqual(hashes, {p.name: packages.digest_file(p) for p in repeated})
                print(f'{version}: all seven native metadata values exact; strict repository reader accepts six; reproducible bytes')
                for left in ('26.10.0', '26.10.1'):
                    subprocess.run(['dpkg', '--compare-versions', left, 'lt', version], check=True)


if __name__ == '__main__':
    unittest.main(verbosity=2)
