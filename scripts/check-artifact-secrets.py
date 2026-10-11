#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Scan built files, package payloads and saved image layers; never print matches."""
import argparse
import gzip
import hashlib
import io
import itertools
import json
import lzma
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import tarfile
import zipfile

from package_repository_lib import digest_file, read_checksum_rows, regular_file, _parse_rpm_header


ARCHIVES = ('.tar', '.tar.gz', '.tgz', '.tar.xz', '.tar.zst', '.zip', '.deb', '.rpm', '.apk',
            '.gz', '.gzip', '.xz', '.zst', '.zstd', '.cpio')
ROOT = Path(__file__).resolve().parents[1]


def pinned_exemptions():
    """Only reviewed exact findings are eligible; gitleaks still runs every rule."""
    path = Path(__file__).with_name('artifact-secrets-exemptions.json')
    regular_file(path, 'pinned exemptions')
    data = json.loads(path.read_text())
    if (data.get('version') != 1 or not re.fullmatch(r'[0-9a-f]{40}', data.get('scanned_tree', ''))
            or not isinstance(data.get('findings'), list)):
        raise ValueError('invalid pinned exemptions')
    for row in data['findings']:
        if (not isinstance(row, dict) or not row.get('rule') or not row.get('member_role')
                or not re.fullmatch(r'[0-9a-f]{64}', row.get('match_sha256', ''))
                or type(row.get('max_occurrences_per_member')) is not int
                or row['max_occurrences_per_member'] < 1
                or row.get('class') not in ('test fixture', 'documented example',
                                          'random-looking non-secret', 'third-party public test key')
                or not row.get('reason') or not isinstance(row.get('sources'), list)
                or not row['sources']):
            raise ValueError('invalid pinned finding')
        for source in row['sources']:
            path = Path(source['path'])
            if (source['kind'] not in ('repository', 'goroot', 'gomodcache')
                    or path.is_absolute() or '..' in path.parts or not path.parts
                    or type(source.get('line')) is not int or source['line'] < 1
                    or not re.fullmatch(r'[0-9a-f]{64}', source.get('sha256', ''))):
                raise ValueError('invalid pinned source')
    return data['findings']


def sources_unchanged(sources, cache):
    for source in sources:
        key = (source['kind'], source['path'], source['line'], source['sha256'])
        if key not in cache:
            try:
                if source['kind'] == 'repository':
                    base = ROOT
                else:
                    if 'go-env' not in cache:
                        env = dict(os.environ, GOTOOLCHAIN='local')
                        result = subprocess.run(['go', 'env', '-json', 'GOROOT', 'GOMODCACHE'],
                                                cwd=ROOT, env=env, capture_output=True,
                                                check=True, timeout=10)
                        cache['go-env'] = json.loads(result.stdout)
                    base = Path(cache['go-env'][source['kind'].upper()])
                base = base.resolve(strict=True)
                path = base / source['path']
                regular_file(path, 'pinned source')
                if not path.resolve(strict=True).is_relative_to(base):
                    raise ValueError('source escapes its root')
                content = path.read_bytes()
                cache[key] = (hashlib.sha256(content).hexdigest() == source['sha256']
                              and source['line'] <= len(content.splitlines()))
            except Exception:
                cache[key] = False
        if not cache[key]:
            return False
    return True


def member_role(origin):
    # Only the measured executable paths share a role. The outer archive name
    # and its release version never participate in an exemption lookup.
    member = origin.rsplit('!', 1)[-1].removeprefix('./').lstrip('/')
    for name in ('olivares', 'olivares-fips'):
        if member in (name, 'usr/bin/' + name):
            return 'engine binary ' + name
    return 'artifact member ' + member


def unexempted_findings(findings, locations):
    pins = pinned_exemptions()
    source_cache = {}
    occurrences = {}
    blocked = []
    for finding in findings:
        origin = locations[finding['FileSHA256']]
        key = (origin, finding['RuleID'], finding['MatchSHA256'])
        candidates = [row for row in pins if row['rule'] == finding['RuleID']
                      and row['member_role'] == member_role(origin)]
        accepted = next((row for row in candidates if row['match_sha256'] == finding['MatchSHA256']
                         and occurrences.get(key, 0) < row['max_occurrences_per_member']
                         and not any(tag.startswith('decoded') for tag in finding.get('Tags', []))
                         and sources_unchanged(row['sources'], source_cache)), None)
        if accepted is None:
            blocked.append(finding)
        else:
            # Repeated delivery observations never widen one member's budget.
            occurrences[key] = occurrences.get(key, 0) + 1
    return blocked


def ar_members(data):
    if not data.startswith(b'!<arch>\n'):
        raise ValueError('invalid ar archive')
    offset = 8
    while offset < len(data):
        header = data[offset:offset + 60]
        if len(header) != 60 or header[-2:] != b'`\n':
            raise ValueError('truncated ar header')
        name = header[:16].decode('ascii').strip().removesuffix('/')
        size = int(header[48:58])
        start = offset + 60
        if size < 0 or start + size + size % 2 > len(data) or name in ('', '/', '//'):
            raise ValueError('unsupported or truncated ar member')
        yield name, data[start:start + size]
        offset = start + size + size % 2


def cpio_members(data):
    offset = 0
    while True:
        header = data[offset:offset + 110]
        if len(header) != 110 or header[:6] not in (b'070701', b'070702'):
            raise ValueError('invalid newc payload')
        fields = [int(header[6 + n * 8:14 + n * 8], 16) for n in range(13)]
        size, namesize = fields[6], fields[11]
        start = offset + 110
        name = data[start:start + namesize]
        if not name.endswith(b'\0'):
            raise ValueError('truncated newc name')
        start = (start + namesize + 3) & ~3
        if start + size > len(data):
            raise ValueError('truncated newc content')
        if name == b'TRAILER!!!\0':
            if any(data[start + size:]):
                raise ValueError('unexpected bytes after newc trailer')
            return
        if fields[1] & 0o170000 in (0o100000, 0o120000):
            yield name[:-1].decode('utf-8'), data[start:start + size]
        offset = (start + size + 3) & ~3


def validate_tar(data):
    # tarfile with ignore_zeros also ignores invalid headers. Check every record
    # first, allowing the concatenated tar streams used by APK, without hiding
    # a truncated member or a corrupt header behind an apparent end-of-archive.
    if len(data) % tarfile.BLOCKSIZE:
        raise ValueError('truncated tar record')
    offset = 0
    while offset < len(data):
        header = data[offset:offset + tarfile.BLOCKSIZE]
        offset += tarfile.BLOCKSIZE
        if not any(header):
            continue
        member = tarfile.TarInfo.frombuf(header, 'utf-8', 'surrogateescape')
        if member.size < 0 or member.type == tarfile.GNUTYPE_SPARSE:
            raise ValueError('unsupported tar member')
        offset += (member.size + tarfile.BLOCKSIZE - 1) // tarfile.BLOCKSIZE * tarfile.BLOCKSIZE
        if offset > len(data):
            raise ValueError('truncated tar member')


def archive_members(data, name):
    if name.endswith('.deb'):
        yield from ar_members(data)
        return
    if name.endswith('.rpm'):
        if data[:4] != b'\xed\xab\xee\xdb':
            raise ValueError('invalid RPM lead')
        _, end = _parse_rpm_header(data, 96)
        tags, end = _parse_rpm_header(data, (end + 7) & ~7)
        if tags.get(1124) != 'cpio':
            raise ValueError('unsupported RPM payload format')
        yield 'payload.cpio.' + tags[1125], data[end:]
        return
    if name.endswith('.zip'):
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            for member in archive.infolist():
                if not member.is_dir():
                    yield member.filename, archive.read(member)
        return
    if name.endswith(('.gz', '.tgz', '.apk', '.gzip')):
        data = gzip.decompress(data)
        if name.endswith(('.tar.gz', '.tgz', '.apk')):
            name = 'content.tar'
        else:
            name = name.rsplit('.', 1)[0]
    elif name.endswith('.xz'):
        data = lzma.decompress(data)
        name = name[:-3]
    elif name.endswith(('.zst', '.zstd')):
        # Python 3.14 supplies zstd in the standard library. Older runtimes fail
        # closed instead of treating a compressed native package as inspected.
        from compression import zstd
        data = zstd.decompress(data)
        name = name.rsplit('.', 1)[0]
    if name.endswith('.cpio'):
        yield from cpio_members(data)
    elif name.endswith('.tar'):
        # APK consists of concatenated gzip/tar streams. Inspect every stream.
        validate_tar(data)
        with tarfile.open(fileobj=io.BytesIO(data), mode='r:', ignore_zeros=True) as archive:
            for member in archive:
                if member.isfile():
                    yield member.name, archive.extractfile(member).read()
                elif member.issym() or member.islnk():
                    yield member.name + '.link', member.linkname.encode()
    else:
        yield name, data


def prepare(path, targets, counter, origins, origin, depth=0):
    regular_file(path, 'artifact member')
    index = str(next(counter))
    origins[index] = origin
    destination = targets / index
    destination.mkdir()
    staged = destination / path.name
    shutil.copyfile(path, staged)
    # Gitleaks skips binary files. Printable strings must also be inspected, including
    # those in executables embedded inside archives and older, whiteouted image layers.
    with staged.open('rb') as source, (destination / 'printable.txt').open('wb') as output:
        tail = b''
        while block := source.read(1024 * 1024):
            block = tail + block
            tail = b''
            for match in re.finditer(rb'[\x20-\x7e]+', block):
                if match.end() == len(block):
                    tail = match.group()
                elif len(match.group()) >= 4:
                    output.write(match.group() + b'\n')
        if len(tail) >= 4:
            output.write(tail + b'\n')
    archive_name = path.name if path.name.endswith(ARCHIVES) else ''
    if not archive_name:
        # Docker/OCI saves can name layer blobs by digest without a suffix.
        # Detect their compression and tar header rather than silently scanning
        # only the compressed bytes. The decompressed result is inspected again.
        with staged.open('rb') as source:
            prefix = source.read(tarfile.BLOCKSIZE)
        for magic, suffix in ((b'\x1f\x8b', '.gz'), (b'\xfd7zXZ\x00', '.xz'),
                              (b'\x28\xb5\x2f\xfd', '.zst'), (b'PK\x03\x04', '.zip'),
                              (b'PK\x05\x06', '.zip'), (b'070701', '.cpio'), (b'070702', '.cpio')):
            if prefix.startswith(magic):
                archive_name = path.name + suffix
                break
        if not archive_name and prefix[257:262] == b'ustar':
            archive_name = path.name + '.tar'
    if archive_name:
        if depth >= 10:
            raise ValueError('archive nesting exceeds the inspection limit')
        with tempfile.TemporaryDirectory(prefix='artifact-unpack-') as temporary:
            for number, (name, data) in enumerate(archive_members(staged.read_bytes(), archive_name)):
                # Flatten members; archive paths and links never address host files.
                member = Path(temporary) / str(number) / Path(name).name
                member.parent.mkdir()
                member.write_bytes(data)
                prepare(member, targets, counter, origins, origin + '!' + name, depth + 1)


def scan(paths):
    scanner = shutil.which('gitleaks')
    if not scanner:
        raise ValueError('gitleaks is unavailable')
    with tempfile.TemporaryDirectory(prefix='artifact-secrets-') as temporary:
        scratch = Path(temporary)
        targets = scratch / 'targets'
        targets.mkdir()
        origins = {}
        counter = itertools.count()
        for path in paths:
            prepare(path, targets, counter, origins, path.name)
        locations = {hashlib.sha256(str(path).encode()).hexdigest(): origins[path.parent.name]
                     for path in targets.glob('*/*') if path.is_file()}
        ignore = scratch / 'ignore'
        ignore.write_text('')
        report = scratch / 'report.json'
        template = scratch / 'report.tmpl'
        # Hash inside gitleaks before writing: no match, secret, context or raw
        # filename enters a report. Redaction would replace the bytes before
        # hashing, so use its supported hash-only template with verbosity off.
        template.write_text('[' + '{{ range $i, $f := . }}{{ if $i }},{{ end }}'
                            '{"RuleID":{{ $f.RuleID | toJson }},'
                            '"FileSHA256":{{ $f.File | sha256sum | toJson }},'
                            '"StartLine":{{ $f.StartLine }},"EndLine":{{ $f.EndLine }},'
                            '"StartColumn":{{ $f.StartColumn }},"EndColumn":{{ $f.EndColumn }},'
                            '"MatchSHA256":{{ $f.Match | sha256sum | toJson }},'
                            '"SecretSHA256":{{ $f.Secret | sha256sum | toJson }},'
                            '"Tags":{{ $f.Tags | toJson }}}'
                            '{{ end }}]')
        env = {key: value for key, value in os.environ.items() if not key.startswith('GITLEAKS_')}
        result = subprocess.run([scanner, 'dir', str(targets), '--no-banner', '--no-color',
                                 '--redact=0', '--log-level=info', '--ignore-gitleaks-allow',
                                 '--config', str(Path(__file__).with_name('artifact-secrets.toml')),
                                 '--gitleaks-ignore-path', str(ignore), '--max-archive-depth=0',
                                 '--max-target-megabytes=0', '--max-decode-depth=5', '--timeout=600',
                                 '--report-format=template', '--report-template', str(template),
                                 '--report-path', str(report)],
                                cwd=scratch, env=env, capture_output=True, timeout=620)
        # An empty report after a skipped/unreadable archive is not a clean scan.
        problems = [line for line in result.stderr.splitlines()
                    if re.search(rb'\b(?:WRN|ERR|FTL)\b', line) and b'leaks found' not in line]
        if problems or not report.is_file():
            raise ValueError('scanner did not completely inspect the artifacts')
        findings = json.loads(report.read_text())
        if not isinstance(findings, list) or result.returncode not in (0, 1):
            raise ValueError('scanner returned an invalid result')
        blocked = unexempted_findings(findings, locations)
        if blocked:
            print(f'artifact-secrets: BLOCKED ({len(blocked)} findings; values withheld)', file=sys.stderr)
            for finding in blocked:
                # A filename can itself contain a credential. Emit a stable member
                # identifier rather than disclosing filenames, matches or context.
                print(json.dumps({'RuleID': finding.get('RuleID'),
                                  'FileSHA256': hashlib.sha256(locations[finding['FileSHA256']].encode()).hexdigest(),
                                  'StartLine': finding.get('StartLine')}), file=sys.stderr)
            return 1
        if result.returncode and not findings:
            raise ValueError('scanner failed without a finding')
        print(f'artifact-secrets: PASS ({len(paths)} built files; {len(findings)} pinned findings)')
        return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--checksums', type=Path)
    parser.add_argument('files', type=Path, nargs='*')
    args = parser.parse_args()
    paths = args.files
    if args.checksums:
        regular_file(args.checksums, 'checksums')
        for name, digest in read_checksum_rows(args.checksums).items():
            path = args.checksums.parent / name
            regular_file(path, 'checksummed artifact')
            if digest_file(path) != digest:
                raise ValueError('artifact changed after checksumming')
            paths.append(path)
        paths.append(args.checksums)
    if not paths:
        raise ValueError('no built artifacts supplied')
    return scan(paths)


if __name__ == '__main__':
    try:
        sys.exit(main())
    except Exception:
        # Neither exception text nor scanner logs may disclose an artifact's contents.
        print('artifact-secrets: COULD NOT SCAN; publication blocked', file=sys.stderr)
        sys.exit(2)
