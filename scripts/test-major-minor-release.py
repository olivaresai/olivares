#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Exercise the release grammar at its build, install, package and signing boundaries."""
import fnmatch
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
VALID = ('1.0', '1.1', '1.10', '1.299', '2.0', '26.13')
INVALID = ('1.0.1', '26.10.2', 'v1.0', '1', '1.0-rc.1', '1.0+meta', '+1.0', '1.-1', '1.0\n2.0')


def load_checker():
    """-> the release checker's definitions, loaded from the repository root, where it reads its data files."""
    source = (ROOT / 'scripts/check-release-version.sh').read_text().split("python3 - <<'PY'\n", 1)[1]
    namespace, before = {'__name__': 'crv'}, Path.cwd()
    os.chdir(ROOT)
    try:
        exec(compile(source.split('if SELFTEST:', 1)[0], 'check-release-version.sh', 'exec'), namespace)
    finally:
        os.chdir(before)
    return namespace


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, ROOT / path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


class MajorMinorRelease(unittest.TestCase):
    def test_canon_and_build_flags(self):
        checker = load_checker()
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / 'scripts/lib').mkdir(parents=True)
            for name in ('scripts/build-ldflags.sh', 'scripts/lib/git-env.sh'):
                shutil.copyfile(ROOT / name, root / name)
            for value in VALID + INVALID:
                with self.subTest(version=value):
                    (root / 'RELEASE-VERSION').write_text(value + '\n')
                    result = subprocess.run(['sh', str(root / 'scripts/build-ldflags.sh'), '--version'],
                                            capture_output=True, text=True)
                    self.assertEqual(result.returncode == 0, value in VALID, result.stderr)
                    if value in VALID:
                        self.assertEqual(result.stdout, value + '\n')
                        self.assertEqual(checker['read_canon'](value), value)
                    else:
                        with self.assertRaises(SystemExit):
                            checker['read_canon'](value)

    def test_installers_and_renderer(self):
        with tempfile.TemporaryDirectory() as folder:
            for value in VALID + INVALID:
                for name in ('install.sh', 'install-bootstrap.sh'):
                    with self.subTest(script=name, version=value):
                        result = subprocess.run(['sh', str(ROOT / 'scripts' / name), '--version', value, '--dry-run'],
                                                capture_output=True, text=True, env=dict(os.environ, CI='true'))
                        self.assertEqual(result.returncode == 0, value in VALID, result.stderr)
                        if value in VALID:
                            self.assertIn('/releases/download/' + value + '/', result.stdout)
                result = subprocess.run(['bash', str(ROOT / 'scripts/render-release-installer.sh'), value,
                                         folder + '/installer.sh'], capture_output=True, text=True)
                with self.subTest(renderer=value):
                    self.assertEqual(result.returncode == 0, value in VALID, result.stderr)

    def test_release_tag_derivation_is_identity(self):
        for name in ('check-aur-olivares-bin.sh', 'installer-matrix-ci.sh'):
            source = (ROOT / 'scripts' / name).read_text()
            helper = re.search(r'^release_tag\(\) \{.*?^}', source, re.M | re.S)[0]
            for value in VALID:
                with self.subTest(script=name, version=value):
                    result = subprocess.check_output(['bash', '-c', helper + '\nrelease_tag "$1"', 'tag', value], text=True)
                    self.assertEqual(result, value)

    def test_documented_installer_recipe(self):
        recipe = (ROOT / 'docs/RELEASE-INSTALLER.md').read_text().split('```sh\n', 1)[1].split('```', 1)[0]
        setup = recipe.split('curl ', 1)[0]
        dry_run = next(line for line in recipe.splitlines() if line.startswith('sh ') and '--dry-run' in line)
        with tempfile.TemporaryDirectory() as folder:
            for value in ('1.0', '1.1', '1.10'):
                with self.subTest(version=value):
                    subprocess.run(['bash', str(ROOT / 'scripts/render-release-installer.sh'), value,
                                    folder + '/olivares-install-' + value + '.sh'],
                                   check=True, capture_output=True, text=True)
                    command = setup.replace('ver=MAJOR.MINOR', 'ver=' + value)
                    command += 'printf "%s\\n" "$base"\n' + dry_run
                    result = subprocess.run(['sh', '-eu', '-c', command], cwd=folder,
                                            env=dict(os.environ, CI='true'), capture_output=True, text=True)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(result.stdout.splitlines()[0],
                                     'https://github.com/olivaresai/olivares/releases/download/' + value)
                    self.assertIn('/releases/download/' + value + '/', result.stdout)

    def test_live_release_verification_examples(self):
        # Frozen site snapshots and independently versioned SDK/provider tags
        # keep their history. These are the current engine release instructions.
        commands = re.compile(r'(?:verify-release\.sh --source-tag|airgap-bundle\.sh --version|'
                              r'check-channel-parity\.sh --tag|release:channel-parity -- --tag|'
                              r'^git checkout) (\S+)', re.M)
        # export-closure: absent-by-design docs/RELEASE-CHANNEL-POLICY.md — internal policy page; the public export removes it and this loop skips it there
        for name in ('docs/RELEASE-VERIFICATION.md', 'docs/RELEASE-CHANNEL-POLICY.md',
                     'scripts/verify-release.sh', 'Taskfile.yml'):
            if name == 'docs/RELEASE-CHANNEL-POLICY.md' and not (ROOT / name).exists():
                result = subprocess.run(['bash', str(ROOT / 'scripts/hub-leg.sh'), '--classify', '--root', str(ROOT)],
                                        capture_output=True, text=True)
                self.assertEqual((result.returncode, result.stdout.strip()), (0, 'public'), result.stderr)
                continue
            examples = commands.findall((ROOT / name).read_text())
            self.assertTrue(examples, name + ' has no release command examples')
            for value in examples:
                with self.subTest(document=name, version=value):
                    self.assertTrue(value == 'MAJOR.MINOR' or re.fullmatch(r'[0-9]+\.[0-9]+', value), value)

    def test_release_index_schema_has_the_same_release_grammar(self):
        schema = json.loads((ROOT / 'docs/contracts/release-index.schema.json').read_text())
        for field in ('version', 'tag'):
            pattern = schema['properties'][field]['pattern']
            for value in VALID + INVALID:
                with self.subTest(field=field, version=value):
                    self.assertEqual(bool(re.fullmatch(pattern, value)), value in VALID)
        pattern = schema['properties']['artifacts']['items']['properties']['url']['pattern']
        for value in VALID + INVALID:
            with self.subTest(artifact_version=value):
                url = 'https://github.com/olivaresai/olivares/releases/download/' + value + '/olivares.tar.gz'
                self.assertEqual(bool(re.fullmatch(pattern, url)), value in VALID)

    def test_first_stable_package_journey_does_not_fetch_a_retired_release(self):
        source = (ROOT / '.github/workflows/pr-ci.yml').read_text()
        metadata = re.search(r'          remove_package\(\) \{.*?^          }\n(.*?)^          for fmt in deb rpm; do', source, re.M | re.S)[1]
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / 'bin').mkdir()
            (root / 'RELEASE-VERSION').write_text('1.0\n')
            for name in ('scripts/build-ldflags.sh', 'scripts/lib/git-env.sh', 'scripts/userspace/resolve_baseline.py'):
                target = root / name
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(ROOT / name, target)
            recorder = root / 'requests'
            for tool in ('curl', 'cosign'):
                script = root / 'bin' / tool
                script.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >>"$REQUESTS"\n')
                script.chmod(0o755)
            env = dict(os.environ, PATH=str(root / 'bin') + os.pathsep + os.environ['PATH'],
                       proof=str(root), REQUESTS=str(recorder), OLIVARES_COSIGN_BIN=str(root / 'bin/cosign'))
            result = subprocess.run(['bash', '-euo', 'pipefail', '-c', metadata], cwd=root, env=env,
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertFalse(recorder.exists(), recorder.read_text() if recorder.exists() else '')

    def test_native_package_versions_and_asset_names(self):
        builder = load('builder', 'scripts/build-native-release-packages.py')
        packages = load('packages', 'scripts/package_repository_lib.py')
        for value in VALID + INVALID:
            with self.subTest(version=value):
                self.assertEqual(builder.valid_version(value, False), value in VALID)
                self.assertEqual(bool(packages.ASSET_NAME.fullmatch('olivares_' + value + '_linux_amd64.deb')), value in VALID)
        for value in VALID + INVALID:
            with self.subTest(snapshot_version=value):
                self.assertEqual(builder.valid_version(value, True), value in VALID)
        self.assertTrue(builder.valid_version('1.0-SNAPSHOT-abc123', True))
        self.assertFalse(builder.valid_version('1.0.1-SNAPSHOT-abc123', True))

    def test_notes_refuse_patch_tags(self):
        notes = load('notes', 'scripts/render-release-notes.py')
        for value in VALID:
            self.assertEqual(notes.release_notes('## [' + value + '] - 2026-10-07\n\nChanges.\n', value)['version'], value)
        for value in INVALID:
            with self.subTest(version=value), self.assertRaises(ValueError):
                notes.release_notes('## [' + value + '] - 2026-10-07\n\nChanges.\n', value)

    def test_workflow_release_tag_triggers(self):
        for name in ('release.yml', 'compose-ready.yml'):
            source = (ROOT / '.github/workflows' / name).read_text()
            patterns = json.loads(re.search(r'tags:\s*(\[[^\n]+\])', source)[1])
            for tag in ('1.0', '1.10', '4.0', '12.299'):
                self.assertTrue(any(fnmatch.fnmatchcase(tag, pattern) for pattern in patterns),
                                name + ' does not trigger for ' + tag)
            self.assertFalse(any(fnmatch.fnmatchcase('chart-v0.1.0', pattern) for pattern in patterns))

    def test_arch_matrix_release_inputs_reach_publisher(self):
        source = (ROOT / 'scripts/test-package-arch-matrix.sh').read_text()
        builds = re.findall(r'^build_set v[12] (\S+)$', source, re.MULTILINE)
        self.assertEqual(builds, ['1.0', '2.0'])
        publisher_version = re.search(r'--pacman-packages "\$scratch/release-v2" --version (\S+)', source)[1]
        self.assertEqual(publisher_version, builds[1])
        self.assertIn('pkg=olivares_' + publisher_version + '_linux_amd64.pkg.tar.zst', source)
        with tempfile.TemporaryDirectory() as folder:
            tree, packages = Path(folder) / 'tree', Path(folder) / 'packages'
            tree.mkdir()
            packages.mkdir()
            env = dict(os.environ, TMPDIR=folder,
                       OLIVARES_REPO_ADD_BIN=str(Path(folder) / 'absent-repo-add'))
            for version in builds + [publisher_version] + list(INVALID):
                with self.subTest(version=version):
                    result = subprocess.run(['bash', str(ROOT / 'scripts/publish-package-repositories.sh'),
                                             '--mode', 'pacman-render', '--tree', str(tree),
                                             '--pacman-packages', str(packages), '--version', version,
                                             '--source-date-epoch', '1791374400'],
                                            env=env, capture_output=True, text=True)
                    self.assertEqual(result.returncode, 2)
                    self.assertIn('repo-add is not an absolute executable' if version in VALID
                                  else '--version must be MAJOR.MINOR', result.stderr)

    def test_arch_aur_download_uses_bare_tag(self):
        source = (ROOT / 'scripts/test-package-arch-matrix.sh').read_text()
        url = re.search(r'"(https://github.com/olivaresai/olivares/releases/download/[^"\n]+)"', source)[1]
        for version in ('1.0', '1.10'):
            self.assertEqual(url.replace('${aur_ver}', version).replace('$f', 'checksums.txt'),
                             'https://github.com/olivaresai/olivares/releases/download/'
                             + version + '/checksums.txt')

    def test_arch_matrix_preserves_native_version_schema(self):
        source = (ROOT / 'scripts/test-package-arch-matrix.sh').read_text()
        self.assertTrue('scripts/build-native-release-packages.py' in source,
                        'Arch matrix bypasses the exact-version native package producer')
        self.assertTrue('--dist "$proj/dist" --version "$version"' in source,
                        'Arch matrix does not package its own archive/version')
        self.assertNotIn('entry.pop("version_schema", None)', source)

    def test_first_stable_publication_keeps_latest_guard(self):
        source = (ROOT / 'scripts/release-finalize-stable.sh').read_text()
        guard = source.split('# --- latest publication guard: begin', 1)[1].split('# --- latest publication guard: end', 1)[0].split('\n', 1)[1]
        retired = [tag for tag in (ROOT / 'scripts/lib/retired-release-tags.txt').read_text().splitlines()
                   if tag and not tag.startswith('#')]
        self.assertEqual(retired, ['v26.8.0', 'v26.9.0', '26.10.0', '26.10.1'])
        cases = [('1.0', '1.1', '200', 0, 1), ('1.0', '2.0', '200', 0, 1), ('1.0', '0.9', '200', 0, 0),
                 ('1.0', '1.0', '200', 0, 1), ('1.0', '1.0', '503', 1, 2)]
        cases += [('1.0', tag, '200', 0, 0) for tag in retired]
        # 1.0 follows only an exact retired tag; every other origin is blind, never a fresh start.
        cases += [('1.0', origin, '200', 0, 2) for origin in (
            '26.10.2', '26.9.0', 'v26.10.1', 'unknown', '', '1.0-rc.1', '1.0+meta', '1.0.1', '1.0.1.2',
            'v2.0', '26.11.0', '99.1.0', ' 26.10.1', 'x\n26.10.1', '26.10.1\n', '26.10.1\r',
            '26.10.1-rc.1', '26.10.10', '26.10.1.1', 'V26.8.0', 'v26.8.0.0')]
        # The fresh start is 1.0's alone: a later release never follows a retired tag.
        cases += [('1.1', tag, '200', 0, 2) for tag in retired] + [('1.1', '1.0', '200', 0, 0)]
        for candidate, origin, status, returncode, expected in cases:
            with self.subTest(candidate=candidate, origin=origin, status=status), \
                    tempfile.TemporaryDirectory() as folder:
                work = Path(folder)
                (work / 'origin.json').write_text(json.dumps(dict(id=42, tag_name=origin, draft=False, prerelease=False)))
                script = 'set -e\n_say() { :; }\nrefuse() { echo "$*" >&2; exit 1; }\nblind() { echo "$*" >&2; exit 2; }\n'
                script += 'gh_api() { printf "HTTP/2 %s\\n\\n" "$TEST_HTTP_STATUS"; cat "$WORK/origin.json"; return "$TEST_HTTP_RC"; }\n'
                env = dict(os.environ, MAKE_LATEST='true', RELEASE_TAG=candidate, REPOSITORY='fixture/release', ROOT=str(ROOT),
                           JQ_BIN=shutil.which('jq') or 'jq', CMP_BIN=shutil.which('cmp') or 'cmp', WORK=folder,
                           TEST_HTTP_STATUS=status,
                           TEST_HTTP_RC=str(returncode))
                result = subprocess.run(['bash', '-c', script + guard], env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, expected, result.stderr)

    def test_retired_list_readers_agree(self):
        # One list, two readers in two languages: the same bytes are accepted or refused by both.
        source = (ROOT / 'scripts/release-finalize-stable.sh').read_text()
        reader = source[source.index('RETIRED_TAGS="$ROOT/'):]
        reader = reader[:reader.index('\n}\n') + 3]
        namespace = load_checker()
        lists = {'real': (ROOT / 'scripts/lib/retired-release-tags.txt').read_bytes(), 'unterminated': b'v26.8.0\nv26.9.0\n26.10.0\n26.10.1',
                 'crlf': b'v26.8.0\r\n26.10.1\r\n', 'nul': b'v26.8.0\n26.10.1\x00\n', 'padded': b'v26.8.0\n 26.10.1\n',
                 'capital': b'V26.8.0\n26.10.1\n', 'two-numbers': b'v26.8.0\n1.0\n', 'empty': b'# none\n',
                 'nul-in-comment': b'# n\x00ul\n26.10.1\n', 'latin-1-comment': b'# caf\xe9\n26.10.1\n',
                 'control-in-comment': b'# \x01\n26.10.1\n', 'long-number': b'26.10.' + b'1' * 5000 + b'\n',
                 'bom': b'\xef\xbb\xbfv26.8.0\n26.10.1\n'}
        for name, data in lists.items():
            with self.subTest(list=name), tempfile.TemporaryDirectory() as folder:
                path = Path(folder) / 'retired-release-tags.txt'
                path.write_bytes(data)
                shell = subprocess.run(['bash', '-c', 'blind() { exit 2; }; CMP_BIN=cmp; ROOT=x\n' + reader +
                                        '\nRETIRED_TAGS="$1"; retired_tag 26.10.1', 'retired', str(path)],
                                       capture_output=True, text=True)
                try:
                    namespace['read_retired_tags'](path=str(path))
                    python_accepts = True
                except SystemExit as refused:
                    python_accepts = False
                    self.assertEqual(refused.code, 2)
                self.assertEqual(shell.returncode != 2, python_accepts, shell.stderr)
                self.assertEqual(python_accepts, name in ('real', 'unterminated'))

    def test_release_signing_identities(self):
        for name in ('install.sh', 'install-bootstrap.sh', 'install-agentops.sh', 'verify-release.sh'):
            source = (ROOT / 'scripts' / name).read_text()
            pattern = re.search(r"DEFAULT_CERT_IDENTITY='([^']+)'", source)[1]
            for value in VALID + INVALID:
                with self.subTest(script=name, version=value):
                    identity = 'https://github.com/olivaresai/olivares/.github/workflows/release.yml@refs/tags/' + value
                    self.assertEqual(bool(re.fullmatch(pattern, identity)), value in VALID)

    def test_documented_release_signing_identities(self):
        checker = load_checker()
        curated, kept = checker['curated_out'](), checker['curation_kept']()
        count = 0
        for name in sorted(set(checker['surface_files']())):
            if checker['historical_kind'](name, curated, kept) is not None:
                continue  # Frozen records and chart identities retain their own contract.
            text = checker['read_surface'](name)
            for pattern in re.findall(r"--certificate-identity-regexp\s+(?:\\\s*)?'([^']+)'", text):
                if '/workflows/release' not in pattern or 'release-chart' in pattern:
                    continue
                count += 1
                pattern = pattern.replace('<public-owner>', 'olivaresai').replace('<public-repository>', 'olivares')
                for value in VALID + INVALID + ('dev', 'vdev', 'v', ''):
                    with self.subTest(document=name, pattern=pattern, version=value):
                        identity = 'https://github.com/olivaresai/olivares/.github/workflows/release.yml@refs/tags/' + value
                        self.assertEqual(bool(re.search(pattern, identity)), value in VALID)
                for identity in ('https://github.com/other/olivares/.github/workflows/release.yml@refs/tags/1.0',
                                 'prefixhttps://github.com/olivaresai/olivares/.github/workflows/release.yml@refs/tags/1.0',
                                 'https://github.com/olivaresai/olivares/.github/workflows/release.yml@refs/tags/1.0/suffix'):
                    with self.subTest(document=name, untrusted_identity=identity):
                        self.assertIsNone(re.search(pattern, identity))
        self.assertGreater(count, 0, 'documentation identity census is empty')

    def test_shell_release_guards(self):
        # Execute the actual Bash guard with its native regex engine before any
        # publication effects. Each matching guard gets its own subtest. Fleet and
        # offline-bundle publishers moved to Business; this is the Community route.
        paths = [ROOT / 'scripts' / name for name in (
            'release-preflight.sh', 'release-finalize-stable.sh',
            'publish-native-release-packages.sh',
            'render-release-index.sh', 'render-release-installer.sh',
            'package-repository-client-ci.sh',
            'dnf-repository-client.sh', 'check-aur-olivares-bin.sh')]
        paths += [ROOT / '.github/workflows' / name for name in (
            'release.yml', 'publish-packages.yml', 'package-repositories.yml')]
        count = 0
        for path in paths:
            source = path.read_text()
            guards = re.findall(r'\[\[\s*"\$(?:\{)?(?:version|VERSION|pkgver|RELEASE_TAG|RELEASE_VERSION)(?:\})?"\s*=~\s*(\^\S+\$)\s*\]\]', source)
            if path.name == 'release-preflight.sh':
                guards += [re.search(r"PROD_TAG_RE='([^']+)'", source)[1]]
            self.assertTrue(guards, 'no version guard found in ' + str(path))
            for pattern in guards:
                count += 1
                for value in VALID + INVALID:
                    with self.subTest(path=str(path.relative_to(ROOT)), pattern=pattern, version=value):
                        result = subprocess.run(['bash', '-c', '[[ "$1" =~ ' + pattern + ' ]]', 'guard', value])
                        self.assertEqual(result.returncode == 0, value in VALID)
        self.assertGreaterEqual(count, len(paths))


if __name__ == '__main__':
    unittest.main()
