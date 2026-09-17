#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Native package readers shared by the DIST-24-06 producer and verifier."""

from __future__ import annotations

import base64
import bz2
import gzip
import hashlib
import io
import json
import lzma
import re
import struct
import tarfile
import zlib
from dataclasses import dataclass, field
from pathlib import Path


class ContractError(Exception):
    """A measured input or repository defect."""


class UnmeasurableError(Exception):
    """A required input or local capability could not be measured."""


SAFE_NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._+-]*$")
ASSET_NAME = re.compile(
    r"^olivares_(?P<version>[0-9]+\.[0-9]+\.[0-9]+)_linux_"
    r"(?P<asset_arch>amd64|arm64)\.(?P<format>deb|rpm|apk)$"
)
SHA256 = re.compile(r"^[0-9a-f]{64}$")


@dataclass(frozen=True)
class Package:
    source_name: str
    source_path: Path
    format: str
    asset_arch: str
    name: str
    version: str
    arch: str
    sha256: str
    size: int
    description: str = ""
    url: str = ""
    license: str = ""
    maintainer: str = ""
    installed_size: int = 0
    build_time: int = 0
    release: str = ""
    epoch: int = 0
    vendor: str = ""
    group: str = ""
    control_text: str = ""
    apk_control_hash: str = ""
    rpm_header_start: int = 0
    rpm_header_end: int = 0
    provides: tuple[str, ...] = field(default_factory=tuple)


def regular_file(path: Path, label: str) -> None:
    if not path.is_file() or path.is_symlink():
        raise UnmeasurableError(f"{label} is missing, not regular, or is a symlink: {path}")


def digest_file(path: Path, algorithm: str = "sha256") -> str:
    digest = hashlib.new(algorithm)
    with path.open("rb") as stream:
        while chunk := stream.read(1024 * 1024):
            digest.update(chunk)
    return digest.hexdigest()


def read_checksum_rows(path: Path) -> dict[str, str]:
    regular_file(path, "checksums input")
    rows: dict[str, str] = {}
    for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9][A-Za-z0-9._+-]*)", line)
        if not match:
            raise ContractError(f"malformed or unsafe checksums row {number}")
        digest, name = match.groups()
        if name in rows:
            raise ContractError(f"duplicate checksums row for {name}")
        rows[name] = digest
    if not rows:
        raise ContractError("checksums input contains zero rows")
    return rows


def _parse_deb822(text: str) -> dict[str, str]:
    fields: dict[str, str] = {}
    current = ""
    for line in text.splitlines():
        if line.startswith((" ", "\t")):
            if not current:
                raise ContractError("Debian control has an orphan continuation line")
            fields[current] += "\n" + line[1:]
            continue
        if ":" not in line:
            raise ContractError("Debian control contains a line without a field separator")
        current, value = line.split(":", 1)
        current = current.strip()
        if not current or current in fields:
            raise ContractError(f"Debian control has an empty or duplicate field: {current!r}")
        fields[current] = value.lstrip()
    return fields


def _read_ar_members(data: bytes) -> dict[str, bytes]:
    if not data.startswith(b"!<arch>\n"):
        raise ContractError("Debian package has no ar archive magic")
    members: dict[str, bytes] = {}
    offset = 8
    while offset < len(data):
        if offset + 60 > len(data):
            raise ContractError("Debian ar member header is truncated")
        header = data[offset : offset + 60]
        if header[58:60] != b"`\n":
            raise ContractError("Debian ar member header has invalid trailer")
        name = header[:16].decode("ascii", "strict").strip().rstrip("/")
        try:
            size = int(header[48:58].decode("ascii", "strict").strip())
        except ValueError as exc:
            raise ContractError("Debian ar member size is not numeric") from exc
        start = offset + 60
        end = start + size
        if end > len(data):
            raise ContractError(f"Debian ar member is truncated: {name}")
        if name in members:
            raise ContractError(f"Debian ar member is duplicated: {name}")
        members[name] = data[start:end]
        offset = end + (size % 2)
    return members


def _decompress_control(name: str, payload: bytes) -> bytes:
    try:
        if name.endswith(".gz"):
            return gzip.decompress(payload)
        if name.endswith(".xz"):
            return lzma.decompress(payload)
        if name.endswith(".bz2"):
            return bz2.decompress(payload)
        if name == "control.tar":
            return payload
    except (OSError, EOFError, lzma.LZMAError) as exc:
        raise ContractError(f"Debian control archive cannot be decompressed: {name}") from exc
    raise UnmeasurableError(f"unsupported Debian control compression: {name}")


def parse_deb(path: Path, common: dict[str, object]) -> Package:
    members = _read_ar_members(path.read_bytes())
    names = [name for name in members if name.startswith("control.tar")]
    if len(names) != 1:
        raise ContractError(f"Debian package must carry exactly one control archive, found {len(names)}")
    control_tar = _decompress_control(names[0], members[names[0]])
    try:
        with tarfile.open(fileobj=io.BytesIO(control_tar), mode="r:") as archive:
            control_members = [m for m in archive.getmembers() if m.name.lstrip("./") == "control"]
            if len(control_members) != 1:
                raise ContractError("Debian control archive must contain exactly one control file")
            extracted = archive.extractfile(control_members[0])
            if extracted is None:
                raise ContractError("Debian control file is not readable")
            control_text = extracted.read().decode("utf-8", "strict").rstrip("\n")
    except (tarfile.TarError, UnicodeError) as exc:
        raise ContractError("Debian control archive is invalid") from exc
    fields = _parse_deb822(control_text)
    for required in ("Package", "Version", "Architecture", "Description"):
        if not fields.get(required):
            raise ContractError(f"Debian control is missing {required}")
    try:
        installed_size = int(fields.get("Installed-Size", "0")) * 1024
    except ValueError as exc:
        raise ContractError("Debian Installed-Size is not numeric") from exc
    return Package(
        **common,
        name=fields["Package"],
        version=fields["Version"],
        arch=fields["Architecture"],
        description=fields["Description"],
        url=fields.get("Homepage", ""),
        license="AGPL-3.0-only",
        maintainer=fields.get("Maintainer", ""),
        installed_size=installed_size,
        control_text=control_text,
    )


def gzip_members(data: bytes) -> list[tuple[bytes, bytes]]:
    members: list[tuple[bytes, bytes]] = []
    offset = 0
    while offset < len(data):
        decoder = zlib.decompressobj(16 + zlib.MAX_WBITS)
        try:
            plain = decoder.decompress(data[offset:])
            plain += decoder.flush()
        except zlib.error as exc:
            raise ContractError(f"invalid concatenated gzip member at byte {offset}") from exc
        consumed = len(data[offset:]) - len(decoder.unused_data)
        if consumed <= 0 or not decoder.eof:
            raise ContractError(f"truncated concatenated gzip member at byte {offset}")
        members.append((data[offset : offset + consumed], plain))
        offset += consumed
    return members


def _parse_pkginfo(text: str) -> dict[str, list[str]]:
    fields: dict[str, list[str]] = {}
    current = ""
    for line in text.splitlines():
        if not line or line.startswith("#"):
            continue
        if " = " in line:
            current, value = line.split(" = ", 1)
            fields.setdefault(current, []).append(value)
        elif current and line.startswith((" ", "\t")):
            fields[current][-1] += " " + line.strip()
        else:
            raise ContractError("APK .PKGINFO contains an invalid line")
    return fields


def parse_apk(path: Path, common: dict[str, object]) -> Package:
    controls: list[tuple[bytes, dict[str, list[str]]]] = []
    for compressed, plain in gzip_members(path.read_bytes()):
        try:
            with tarfile.open(fileobj=io.BytesIO(plain), mode="r:") as archive:
                matches = [m for m in archive.getmembers() if m.name.lstrip("/") in (".PKGINFO", "./.PKGINFO")]
                if not matches:
                    continue
                if len(matches) != 1:
                    raise ContractError("APK control stream contains duplicate .PKGINFO")
                extracted = archive.extractfile(matches[0])
                if extracted is None:
                    raise ContractError("APK .PKGINFO is not readable")
                fields = _parse_pkginfo(extracted.read().decode("utf-8", "strict"))
                controls.append((compressed, fields))
        except (tarfile.TarError, UnicodeError) as exc:
            raise ContractError("APK gzip member is not a readable tar stream") from exc
    if len(controls) != 1:
        raise ContractError(f"APK must contain exactly one .PKGINFO control stream, found {len(controls)}")
    compressed, fields = controls[0]

    def one(name: str, required: bool = False) -> str:
        values = fields.get(name, [])
        if len(values) > 1:
            raise ContractError(f"APK .PKGINFO field must be singular: {name}")
        if required and not values:
            raise ContractError(f"APK .PKGINFO is missing {name}")
        return values[0] if values else ""

    try:
        installed_size = int(one("size") or "0")
        build_time = int(one("builddate") or "0")
    except ValueError as exc:
        raise ContractError("APK size/builddate is not numeric") from exc
    control_hash = "Q1" + base64.b64encode(hashlib.sha1(compressed).digest()).decode("ascii")
    return Package(
        **common,
        name=one("pkgname", True),
        version=one("pkgver", True),
        arch=one("arch", True),
        description=one("pkgdesc", True),
        url=one("url"),
        license=one("license"),
        maintainer=one("maintainer"),
        installed_size=installed_size,
        build_time=build_time,
        apk_control_hash=control_hash,
        provides=tuple(fields.get("provides", [])),
    )


def _parse_rpm_header(data: bytes, offset: int) -> tuple[dict[int, object], int]:
    if offset + 16 > len(data) or data[offset : offset + 4] != b"\x8e\xad\xe8\x01":
        raise ContractError(f"RPM header magic is absent at byte {offset}")
    count, store_size = struct.unpack_from(">II", data, offset + 8)
    if count > 100_000 or store_size > len(data):
        raise ContractError("RPM header declares unreasonable index/store sizes")
    index_start = offset + 16
    store_start = index_start + count * 16
    end = store_start + store_size
    if end > len(data):
        raise ContractError("RPM header index/store is truncated")
    values: dict[int, object] = {}
    for number in range(count):
        tag, kind, relative, items = struct.unpack_from(">IIII", data, index_start + number * 16)
        position = store_start + relative
        if position < store_start or position > end:
            raise ContractError(f"RPM tag {tag} points outside its store")
        if kind in (6, 8, 9):
            strings: list[str] = []
            cursor = position
            for _ in range(items):
                try:
                    terminator = data.index(0, cursor, end)
                except ValueError as exc:
                    raise ContractError(f"RPM string tag {tag} is unterminated") from exc
                strings.append(data[cursor:terminator].decode("utf-8", "replace"))
                cursor = terminator + 1
            value: object = strings[0] if kind == 6 and items == 1 else strings
        elif kind in (2, 3, 4, 5):
            widths = {2: 1, 3: 2, 4: 4, 5: 8}
            width = widths[kind]
            if position + items * width > end:
                raise ContractError(f"RPM integer tag {tag} is truncated")
            codes = {2: "B", 3: "H", 4: "I", 5: "Q"}
            numbers = list(struct.unpack_from(">" + codes[kind] * items, data, position))
            value = numbers[0] if items == 1 else numbers
        elif kind in (0, 1, 7):
            value = data[position : position + items]
        else:
            raise UnmeasurableError(f"unsupported RPM tag type {kind} for tag {tag}")
        if tag not in values:
            values[tag] = value
    return values, end


def parse_rpm(path: Path, common: dict[str, object]) -> Package:
    data = path.read_bytes()
    if len(data) < 112 or data[:4] != b"\xed\xab\xee\xdb":
        raise ContractError("RPM package has no lead magic")
    _, signature_end = _parse_rpm_header(data, 96)
    header_start = (signature_end + 7) & ~7
    tags, header_end = _parse_rpm_header(data, header_start)

    def text(tag: int, label: str, required: bool = False) -> str:
        value = tags.get(tag, "")
        if isinstance(value, list):
            value = value[0] if value else ""
        if not isinstance(value, str):
            raise ContractError(f"RPM {label} tag has the wrong type")
        if required and not value:
            raise ContractError(f"RPM header is missing {label}")
        return value

    def number(tag: int, label: str) -> int:
        value = tags.get(tag, 0)
        if isinstance(value, list):
            value = value[0] if value else 0
        if not isinstance(value, int):
            raise ContractError(f"RPM {label} tag has the wrong type")
        return value

    raw_provides = tags.get(1047, [])
    if isinstance(raw_provides, str):
        raw_provides = [raw_provides]
    if not isinstance(raw_provides, list) or any(not isinstance(item, str) for item in raw_provides):
        raise ContractError("RPM provides tag has the wrong type")
    return Package(
        **common,
        name=text(1000, "name", True),
        version=text(1001, "version", True),
        release=text(1002, "release", True),
        epoch=number(1003, "epoch"),
        arch=text(1022, "architecture", True),
        description=text(1005, "description", True),
        url=text(1020, "URL"),
        license=text(1014, "license"),
        maintainer=text(1015, "packager"),
        installed_size=number(1009, "installed size"),
        build_time=number(1006, "build time"),
        vendor=text(1011, "vendor"),
        group=text(1016, "group"),
        rpm_header_start=header_start,
        rpm_header_end=header_end,
        provides=tuple(raw_provides),
    )


def _base_version(package: Package) -> str:
    value = package.version
    if package.format == "deb":
        value = value.split(":", 1)[-1].split("-", 1)[0]
    elif package.format == "apk":
        value = re.sub(r"-r[0-9]+$", "", value)
    return value


def load_release_packages(checksums: Path, artifact_dir: Path, version: str) -> list[Package]:
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", version):
        raise UnmeasurableError("version must be MAJOR.MINOR.PATCH")
    if not artifact_dir.is_dir():
        raise UnmeasurableError(f"artifact directory is missing: {artifact_dir}")
    rows = read_checksum_rows(checksums)
    selected: list[tuple[str, re.Match[str]]] = []
    for name in rows:
        match = ASSET_NAME.fullmatch(name)
        if match and match.group("version") == version:
            selected.append((name, match))
    expected_names = {
        f"olivares_{version}_linux_{arch}.{fmt}"
        for arch in ("amd64", "arm64")
        for fmt in ("deb", "rpm", "apk")
    }
    got_names = {name for name, _ in selected}
    if got_names != expected_names:
        missing = sorted(expected_names - got_names)
        extra = sorted(got_names - expected_names)
        raise ContractError(f"native package inventory is not exact; missing={missing}, extra={extra}")

    packages: list[Package] = []
    parsers = {"deb": parse_deb, "rpm": parse_rpm, "apk": parse_apk}
    internal_arches = {
        "deb": {"amd64": "amd64", "arm64": "arm64"},
        "rpm": {"amd64": "x86_64", "arm64": "aarch64"},
        "apk": {"amd64": "x86_64", "arm64": "aarch64"},
    }
    for name, match in sorted(selected):
        path = artifact_dir / name
        regular_file(path, f"release asset {name}")
        digest = digest_file(path)
        if digest != rows[name]:
            raise ContractError(f"release asset digest differs from checksums.txt: {name}")
        common: dict[str, object] = {
            "source_name": name,
            "source_path": path,
            "format": match.group("format"),
            "asset_arch": match.group("asset_arch"),
            "sha256": digest,
            "size": path.stat().st_size,
        }
        package = parsers[match.group("format")](path, common)
        if package.name != "olivares":
            raise ContractError(f"native asset has unexpected internal package name: {name}")
        if _base_version(package) != version:
            raise ContractError(
                f"native asset internal version {package.version!r} differs from release {version}: {name}"
            )
        expected_arch = internal_arches[package.format][package.asset_arch]
        if package.arch != expected_arch:
            raise ContractError(
                f"native asset architecture {package.arch!r} differs from filename {package.asset_arch}: {name}"
            )
        packages.append(package)
    return packages


def write_canonical_json(path: Path, value: object) -> None:
    path.write_text(json.dumps(value, indent=2, sort_keys=True, ensure_ascii=False) + "\n", encoding="utf-8")
