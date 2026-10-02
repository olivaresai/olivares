#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Package the GoReleaser archives with their exact, unnormalized release version."""
from __future__ import annotations
import argparse
import copy
import datetime
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile

from package_repository_lib import read_checksum_rows, digest_file, regular_file


def valid_version(version: str, snapshot: bool) -> bool:
    if snapshot:
        return bool(re.fullmatch(r"[0-9]+\.[0-9]+(?:\.[0-9]+)?-[A-Za-z0-9.-]+", version))
    if re.fullmatch(r"[0-9]{2}\.(?:[1-9]|1[0-2])(?:\.[1-9][0-9]*)?", version):
        return True
    # Reproducing historical packages is a reader operation, never a new tag cut.
    if re.fullmatch(r"[0-9]{2}\.(?:[1-9]|1[0-2])\.0", version):
        return tuple(map(int, version.split('.')[:2])) <= (26, 10)
    return False


def build(root: Path, dist: Path, version: str, epoch: int, recipe: Path, snapshot: bool) -> list[Path]:
    if not valid_version(version, snapshot):
        raise ValueError('version must be YY.M or YY.M.N with N >= 1; zero patches are historical only')
    if epoch < 0:
        raise ValueError('source date epoch must be nonnegative')
    stamp = datetime.datetime.fromtimestamp(epoch, datetime.timezone.utc).isoformat().replace('+00:00', 'Z')
    specs = json.loads(recipe.read_text())['nfpms']
    if len(specs) != 2 or any(s.get('version_schema') != 'none' for s in specs):
        raise ValueError('the two native payload recipes must use version_schema=none')
    checksum_file = dist / 'checksums.txt'
    rows = read_checksum_rows(checksum_file)
    result = subprocess.run(['bash', str(root / 'scripts/ensure-nfpm.sh'), '--dest',
        os.environ.get('OLIVARES_NFPM_DIR', str(Path(os.environ.get('XDG_CACHE_HOME', str(Path.home() / '.cache'))) / 'olivares/nfpm'))],
        check=True, text=True, stdout=subprocess.PIPE)
    nfpm = result.stdout.strip()
    outputs: list[Path] = []
    # A private stage keeps a failed build from replacing an admitted artifact set.
    with tempfile.TemporaryDirectory(prefix='native-release-', dir=dist) as temporary:
        stage = Path(temporary)
        binaries: dict[str, Path] = {}
        for arch in ('amd64', 'arm64'):
            archive = dist / f'olivares_{version}_linux_{arch}.tar.gz'
            regular_file(archive, 'base archive')
            if rows.get(archive.name) != digest_file(archive):
                raise ValueError(f'base archive does not match checksums.txt: {archive.name}')
            with tarfile.open(archive, 'r:gz') as bundle:
                members = [m for m in bundle.getmembers() if Path(m.name).name == 'olivares']
                if len(members) != 1 or not members[0].isfile() or not members[0].mode & 0o111:
                    raise ValueError('archive must carry exactly one regular executable olivares binary')
                binary = stage / f'olivares-{arch}'
                with bundle.extractfile(members[0]) as source, binary.open('wb') as target:
                    shutil.copyfileobj(source, target)
                binary.chmod(0o755)
                os.utime(binary, (epoch, epoch))
                binaries[arch] = binary
        for spec in specs:
            formats = spec['formats']
            if formats not in (['deb', 'rpm', 'apk'], ['archlinux']):
                raise ValueError('native format inventory differs from the seven-asset contract')
            arches = ('amd64',) if formats == ['archlinux'] else ('amd64', 'arm64')
            for arch in arches:
                config = copy.deepcopy(spec)
                config['name'] = config.pop('package_name')
                bindir = config.pop('bindir', '/usr/bin')
                for key in ('id', 'ids', 'formats'):
                    config.pop(key, None)
                config.update(version=version, arch=arch, platform='linux', mtime=stamp)
                config['contents'].append({'src': str(binaries[arch]), 'dst': f'{bindir}/olivares',
                                           'file_info': {'mode': 0o755, 'mtime': stamp}})
                config_file = stage / f'config-{arch}.json'
                config_file.write_text(json.dumps(config, indent=2) + '\n')
                for fmt in formats:
                    ext = 'pkg.tar.zst' if fmt == 'archlinux' else fmt
                    path = stage / f'olivares_{version}_linux_{arch}.{ext}'
                    subprocess.run([nfpm, 'package', '--config', str(config_file), '--packager', fmt,
                                    '--target', str(path)], cwd=root, check=True)
                    regular_file(path, 'native package')
                    if path.stat().st_size == 0:
                        raise ValueError('nFPM produced an empty package')
                    outputs.append(path)
        if len(outputs) != 7:
            raise ValueError('native package inventory must have exactly seven assets')
        additions = {path.name: digest_file(path) for path in outputs}
        updated = dict(rows, **additions)
        for path in outputs:
            os.replace(path, dist / path.name)
        next_checksums = stage / 'checksums.next'
        next_checksums.write_text(''.join(f'{digest}  {name}\n' for name, digest in sorted(updated.items())))
        os.replace(next_checksums, checksum_file)
    return [dist / path.name for path in outputs]


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', required=True)
    parser.add_argument('--dist', type=Path, required=True)
    parser.add_argument('--root', type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument('--recipe', type=Path)
    parser.add_argument('--source-date-epoch', type=int)
    parser.add_argument('--snapshot', action='store_true')
    args = parser.parse_args()
    root, dist = args.root.resolve(), args.dist.resolve()
    epoch = args.source_date_epoch
    if epoch is None:
        epoch = int(subprocess.check_output(['git', 'show', '-s', '--format=%ct', 'HEAD'], cwd=root, text=True).strip())
    for path in build(root, dist, args.version, epoch, args.recipe or root / 'packaging/nfpm/packages.json', args.snapshot):
        print(path.name)


if __name__ == '__main__':
    main()
