#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Rebuild or verify the built-in skills catalog (modules/skills/builtin).

The only update path. A maintainer edits `sources` in PIN.json (a commit and the
selection), runs `skills-catalog.py build`, reviews the diff and commits it. `verify`
rebuilds from the pinned commits and fails if a committed archive differs. Nothing
here runs at install time: the product only reads the committed archives.

Each pack is a deterministic tar.gz of upstream bytes plus the upstream LICENSE. The
pinned digests are the sha256 of the committed archive (read by the product) and of
its uncompressed tar (what `verify` compares, so it does not depend on the local zlib).
A source's `exclude` names upstream paths left out, each with the reason; the product's
own validator decides what it can import, and its test fails if a pack is refused.
"""
import argparse
import gzip
import hashlib
import http.client
import io
import json
import os
import re
import sys
import tarfile
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DATA = ROOT / 'modules/skills/builtin'
PIN = DATA / 'PIN.json'
MAX_SOURCE_BYTES = 256 << 20
PACK_ID = re.compile(r'[a-z0-9]+(?:-[a-z0-9]+)*')


def fetch(repository: str, commit: str, exclude: dict[str, str]) -> dict[str, tuple[bytes | None, int]]:
    """Return {path: (bytes, mode)} for every file of the commit except `exclude`.

    A link or device has body None: it may exist outside what a pack selects, and a pack
    that would contain one stops the build (see subtree and build_pack).
    """
    owner_repo = repository.removeprefix('https://github.com/')
    url = f'https://codeload.github.com/{owner_repo}/tar.gz/{commit}'
    with urllib.request.urlopen(url, timeout=120) as response:
        raw = response.read(MAX_SOURCE_BYTES + 1)
    if len(raw) > MAX_SOURCE_BYTES:
        raise ValueError(f'{url}: source exceeds {MAX_SOURCE_BYTES} bytes')
    files = {}
    with tarfile.open(fileobj=io.BytesIO(raw), mode='r:gz') as tar:
        # git archive records the commit it exported; a tag or branch cannot fake it.
        if tar.pax_headers.get('comment') != commit:
            raise ValueError(f'{url}: archive is not commit {commit}')
        total = 0
        for member in tar:
            if member.isdir():
                continue
            _, _, name = member.name.partition('/')
            if not member.isreg():
                files[name] = (None, 0)
                continue
            total += member.size
            if total > MAX_SOURCE_BYTES:
                raise ValueError(f'{url}: expanded source exceeds {MAX_SOURCE_BYTES} bytes')
            files[name] = (tar.extractfile(member).read(), 0o755 if member.mode & 0o111 else 0o644)
    for path in exclude:
        if not any(name.startswith(path + '/') for name in files):
            raise ValueError(f'{repository}: excluded path {path} is not in commit {commit}')
    return {n: v for n, v in files.items() if not any(n.startswith(p + '/') for p in exclude)}


def make_tar(files: dict[str, tuple[bytes, int]]) -> bytes:
    out = io.BytesIO()
    # GNU format: the product refuses PAX records. Sorted, no owner, no time.
    with tarfile.open(fileobj=out, mode='w', format=tarfile.GNU_FORMAT) as tar:
        for name in sorted(files):
            body, mode = files[name]
            info = tarfile.TarInfo(name)
            info.size, info.mode, info.mtime = len(body), mode, 0
            tar.addfile(info, io.BytesIO(body))
    return out.getvalue()


def make_gzip(tar: bytes) -> bytes:
    out = io.BytesIO()
    with gzip.GzipFile(filename='', mode='wb', fileobj=out, compresslevel=9, mtime=0) as gz:
        gz.write(tar)
    return out.getvalue()


def subtree(files: dict, prefix: str) -> dict:
    prefix = prefix.rstrip('/') + '/'
    chosen = {name.removeprefix(prefix): value for name, value in files.items() if name.startswith(prefix)}
    links = sorted(name for name, (body, _) in chosen.items() if body is None)
    if links:
        raise ValueError(f'{prefix}: not a regular file: {links}')
    return chosen


def declared_licenses(pack: dict) -> set[str]:
    found = set()
    for name, (body, _) in pack.items():
        if name.count('/') == 1 and name.endswith('/SKILL.md'):
            front = re.match(r'---\r?\n(.*?)\r?\n---', body.decode('utf-8'), re.S)
            if not front or not re.search(r'^license\s*:', front.group(1), re.M):
                continue
            match = re.search(r'^license:[ \t]*["\']?([A-Za-z0-9.+-]+)["\']?[ \t]*\r?$', front.group(1), re.M)
            if not match:
                raise ValueError(f'{name}: license line is not a single SPDX identifier')
            found.add(match.group(1))
    return found


def ecc_groups(files: dict) -> list[tuple[str, str, dict]]:
    """Group skills as ECC's own install manifest does, one pack per module."""
    manifest = json.loads(files['manifests/install-modules.json'][0])
    skills = {name.split('/')[1] for name in files if name.startswith('skills/') and name.endswith('/SKILL.md')}
    seen, groups = set(), []
    for module in manifest['modules']:
        names = {p.split('/', 1)[1] for p in module['paths'] if p.startswith('skills/') and p.split('/', 1)[1] in skills}
        if not names:
            continue
        if names & seen:
            raise ValueError(f'module {module["id"]}: skill listed by two modules')
        seen |= names
        pack = {}
        for name in names:
            pack.update({f'{name}/{rest}': v for rest, v in subtree(files, f'skills/{name}').items()})
        groups.append((f'ecc-{module["id"]}', module['description'], pack))
    if seen != skills:
        raise ValueError(f'skills outside every ECC module: {sorted(skills - seen)}')
    return groups


def build_pack(source: dict, files: dict, pack_id: str, description: str, pack: dict, folder: str) -> tuple[dict, dict]:
    if not PACK_ID.fullmatch(pack_id):
        raise ValueError(f'{pack_id!r}: not a pack id')
    if files[source['license_file']][0] is None:
        raise ValueError(f'{pack_id}: the license file is not a regular file')
    pack = dict(pack, LICENSE=files[source['license_file']])
    # A skill under another license needs its own notice: stop and let a person add it.
    other = declared_licenses(pack) - {source['license']}
    if other:
        raise ValueError(f'{pack_id}: skills declare {sorted(other)}, not {source["license"]}')
    tar = make_tar(pack)
    archive = make_gzip(tar)
    entry = {
        'id': pack_id, 'source': source['id'], 'description': description,
        'file': f'{folder}{pack_id}.tar.gz', 'sha256': hashlib.sha256(archive).hexdigest(),
        'tar_sha256': hashlib.sha256(tar).hexdigest(),
        'skills': sum(1 for n in pack if n.count('/') == 1 and n.endswith('/SKILL.md')),
        'files': len(pack),
    }
    return entry, {entry['file']: archive}


def build(pin: dict) -> tuple[dict, dict]:
    packs, data, archives = [], [], {}
    for source_id, source in pin['sources'].items():
        source = dict(source, id=source_id)
        files = fetch(source['repository'], source['commit'], source.get('exclude', {}))
        if source_id == 'ecc':
            groups = ecc_groups(files)
            plain = [('ecc-agents', 'Agent definitions (data).', subtree(files, 'agents')),
                     ('ecc-rules', 'Rules (data).', subtree(files, 'rules'))]
        else:
            groups = [(pack_id, f'Upstream {path}.', subtree(files, path)) for pack_id, path in source['select'].items()]
            plain = []
        # Agents and rules are not skills: pinned and vendored under data/, not embedded.
        for into, items, folder in ((packs, groups, ''), (data, plain, 'data/')):
            for pack_id, description, pack in items:
                if not pack:
                    raise ValueError(f'{pack_id}: nothing selected')
                entry, produced = build_pack(source, files, pack_id, description, pack, folder)
                into.append(entry)
                archives.update(produced)
    result = {'schema': 1, 'sources': pin['sources'], 'packs': sorted(packs, key=lambda p: p['id']),
              'data': sorted(data, key=lambda p: p['id'])}
    return result, archives


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('command', choices=('build', 'verify'))
    command = parser.parse_args().command
    pin = json.loads(PIN.read_text())
    rebuilt, archives = build(pin)
    if command == 'verify':
        verify(pin, rebuilt, archives)
        print(f'skills-catalog: {len(archives)} archives match the pinned sources')
        return
    (DATA / 'data').mkdir(exist_ok=True)
    # Replace each file whole, PIN.json last, so a failure never leaves a torn archive.
    for name, archive in archives.items():
        write_atomic(DATA / name, archive)
    for stale in DATA.glob('**/*.tar.gz'):
        if str(stale.relative_to(DATA)) not in archives:
            stale.unlink()
    write_atomic(PIN, (json.dumps(rebuilt, indent=2) + '\n').encode())
    print(f'skills-catalog: wrote {len(archives)} archives')


def write_atomic(path: Path, body: bytes) -> None:
    temporary = path.with_name(path.name + '.tmp')
    temporary.write_bytes(body)
    os.replace(temporary, path)


def verify(pin: dict, rebuilt: dict, archives: dict) -> None:
    """The committed pin and archives must be exactly what the pinned commits produce."""
    for section in ('packs', 'data'):
        committed = {e['id']: e for e in pin.get(section, [])}
        expected = {e['id']: e for e in rebuilt[section]}
        if len(committed) != len(pin.get(section, [])) or committed.keys() != expected.keys():
            raise ValueError(f'{section}: PIN.json lists different packs than the pinned sources produce')
        for pack_id, entry in expected.items():
            # Everything but the archive digest, which depends on the local zlib.
            if {k: v for k, v in committed[pack_id].items() if k != 'sha256'} != {k: v for k, v in entry.items() if k != 'sha256'}:
                raise ValueError(f'{pack_id}: PIN.json entry differs from the pinned source')
            on_disk = (DATA / entry['file']).read_bytes()
            if (hashlib.sha256(on_disk).hexdigest() != committed[pack_id]['sha256']
                    or hashlib.sha256(gzip.decompress(on_disk)).hexdigest() != entry['tar_sha256']):
                raise ValueError(f'{pack_id}: committed archive differs from the pinned source')
    present = {str(p.relative_to(DATA)) for p in DATA.glob('**/*.tar.gz')}
    if present != archives.keys():
        raise ValueError(f'archives not in PIN.json: {sorted(present - archives.keys())}; missing: {sorted(archives.keys() - present)}')
    if pin.get('sources') != rebuilt['sources'] or pin.get('schema') != rebuilt['schema']:
        raise ValueError('PIN.json sources differ from the rebuilt pin')


if __name__ == '__main__':
    try:
        main()
    except (OSError, EOFError, http.client.HTTPException, ValueError, KeyError, json.JSONDecodeError, tarfile.TarError) as error:
        print(f'skills-catalog: {error}', file=sys.stderr)
        sys.exit(1)
