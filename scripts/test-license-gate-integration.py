#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise the real Go resolver and go-licenses with a planted GPL dependency."""
import json
import os
import re
import shutil
import tarfile
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
with tempfile.TemporaryDirectory(prefix='license-integration-') as tmp:
    project = Path(tmp)
    (project / 'dep').mkdir()
    (project / 'go.mod').write_text('module example.test/app\ngo 1.24\nrequire example.test/dep v0.0.0\nreplace example.test/dep => ./dep\n')
    (project / 'dep/go.mod').write_text('module example.test/dep\ngo 1.24\n')
    (project / 'main.go').write_text('package main\nfunc main() {}\n')
    (project / 'dep/dep.go').write_text('package dep\n')
    allowed_license = (ROOT / 'LICENSES/Apache-2.0.txt').read_text()
    (project / 'LICENSE').write_text(allowed_license)
    (project / 'dep/LICENSE').write_text((ROOT / 'LICENSES/GPL-3.0-only.txt').read_text()
                                        if (ROOT / 'LICENSES/GPL-3.0-only.txt').exists()
                                        else Path('/usr/share/common-licenses/GPL-3').read_text())
    (project / 'NOTICE').write_text('Fixture first-party notice\n')
    package = project / 'web/node_modules/fixture'
    package.mkdir(parents=True)
    (package / 'package.json').write_text(json.dumps({'name': 'fixture', 'version': '1.0.0'}))
    (package / 'NOTICE').write_text('Console upstream NOTICE\n')
    (project / 'console.json').write_text(json.dumps([{'name': 'fixture', 'version': '1.0.0',
                                                       'text': 'Fixture console license'}]))
    env = dict(os.environ, GOWORK='off')
    command = [sys.executable, str(ROOT / 'scripts/license-gate.py')]
    for tags in ('release', 'enterprise,release'):
        (project / 'planted.go').write_text('//go:build ' + ('enterprise' if 'enterprise' in tags else 'release')
                                           + '\n\npackage main\nimport _ "example.test/dep"\n')
        bad = subprocess.run([*command, '--tags', tags, '.'], cwd=project, env=env,
                             text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        assert bad.returncode != 0 and 'disallowed license' in bad.stderr and 'GPL' in bad.stderr, bad.stderr
        print(f'PASS: planted GPL dependency rejected for tags={tags}')
    (project / 'planted.go').unlink()
    (project / 'platform_darwin.go').write_text('package main\nimport _ "example.test/dep"\n')
    bad = subprocess.run([*command, '--tags', 'release', '--target', 'linux/amd64',
                          '--target', 'darwin/arm64', '.'], cwd=project, env=env,
                         text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    assert bad.returncode != 0 and 'disallowed license' in bad.stderr, bad.stderr
    (project / 'platform_darwin.go').unlink()
    print('PASS: GPL dependency imported only on another release platform is rejected')
    # The real connector inventory can be listed without building or deleting.
    (project / 'scripts').mkdir()
    shutil.copyfile(ROOT / 'scripts/build-connectors.sh', project / 'scripts/build-connectors.sh')
    roots = subprocess.check_output(['bash', str(project / 'scripts/build-connectors.sh'), '--list'], text=True).splitlines()
    for root in ['./cmd/olivares', *roots]:
        directory = project / root
        directory.mkdir(parents=True, exist_ok=True)
        (directory / 'main.go').write_text('package main\nfunc main() {}\n')
    (project / roots[-1] / 'gpl.go').write_text('package main\nimport _ "example.test/dep"\n')
    bad = subprocess.run([*command, '--tags', 'release'], cwd=project, env=env,
                         text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    assert bad.returncode != 0 and 'disallowed license' in bad.stderr, bad.stderr
    print('PASS: GPL dependency imported only by an embedded connector is rejected')
    (project / 'planted.go').write_text('package main\nimport _ "example.test/dep"\n')
    (project / 'dep/LICENSE').write_text(allowed_license)
    (project / 'dep/NOTICE').write_text('Upstream NOTICE attribution\n')
    subprocess.run([*command, '--tags', 'enterprise,release', '--notice', 'NOTICE-community',
                    '--web-license', 'console.json', '.'], cwd=project, env=env, check=True)
    notice = (project / 'NOTICE-community').read_text()
    for text in ('Upstream NOTICE attribution', 'Fixture console license', 'Fixture first-party notice', 'Console upstream NOTICE'):
        assert text in notice, text
    assert (project / 'NOTICE-community').stat().st_mode & 0o777 == 0o644
    print('PASS: allowed module produces readable NOTICE with upstream and console attribution')
    if '--archive' in sys.argv:
        goreleaser = os.environ['OLIVARES_GORELEASER']
        (project / '.license-notices').mkdir()
        shutil.copyfile(project / 'NOTICE-community', project / '.license-notices/NOTICE-community')
        # Use the production mapping verbatim: a dst/strip_parent regression
        # must change the artifact under test, not just pass a YAML assertion.
        config = (ROOT / '.goreleaser.yaml').read_text()
        mapping = re.search(r'      - src: \.license-notices/NOTICE-community\n(?:        [^\n]+\n)+', config).group()
        (project / '.goreleaser.yaml').write_text('''version: 2
project_name: fixture
snapshot:
  version_template: 0.0.0-SNAPSHOT
builds:
  - id: fixture
    main: .
    binary: fixture
    goos: [linux]
    goarch: [amd64]
archives:
  - id: fixture
    ids: [fixture]
    formats: [tar.gz]
    files:
''' + mapping)
        for git_args in (['init', '-q'], ['add', '.'],
                         ['-c', 'user.name=fixture', '-c', 'user.email=fixture@example.invalid',
                          'commit', '-qm', 'test(licenses): archive fixture'],
                         ['remote', 'add', 'origin', 'https://example.invalid/fixture.git']):
            subprocess.run(['git', *git_args], cwd=project, check=True)
        subprocess.run([goreleaser, 'release', '--snapshot', '--clean', '--parallelism', '1',
                        '--skip=before,publish,sign,sbom,docker,validate,announce'],
                       cwd=project, env=env, check=True)
        archive = next((project / 'dist').glob('*.tar.gz'))
        with tarfile.open(archive) as bundle:
            assert 'NOTICE' in bundle.getnames(), bundle.getnames()
            assert bundle.extractfile('NOTICE').read().decode() == notice
        print('PASS: release dry run lists NOTICE at archive root with generated attribution')

# A private cache avoids modifying any shared downloaded dependency.
with tempfile.TemporaryDirectory(prefix='license-mpl-') as tmp:
    project = Path(tmp)
    (project / 'go.mod').write_text('module example.test/mplfixture\ngo 1.24\nrequire github.com/hashicorp/golang-lru v1.0.2\n')
    (project / 'main.go').write_text('package main\nimport _ "github.com/hashicorp/golang-lru"\nfunc main() {}\n')
    (project / 'LICENSE').write_text((ROOT / 'LICENSES/Apache-2.0.txt').read_text())
    env = dict(os.environ, GOWORK='off', GOMODCACHE=str(project / 'modcache'))
    subprocess.run(['go', 'mod', 'download', 'github.com/hashicorp/golang-lru'], cwd=project, env=env, check=True)
    command = [sys.executable, str(ROOT / 'scripts/license-gate.py'), '--tags', 'release', '.']
    subprocess.run(command, cwd=project, env=env, check=True)
    source = project / 'modcache/github.com/hashicorp/golang-lru@v1.0.2/lru.go'
    source.chmod(0o644)
    source.write_text(source.read_text() + '\n// planted local modification\n')
    bad = subprocess.run(command, cwd=project, env=env, text=True,
                         stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    assert bad.returncode != 0 and 'modified' in bad.stdout + bad.stderr, bad.stdout + bad.stderr
    print('PASS: unmodified MPL module accepted; modified cached source rejected')
