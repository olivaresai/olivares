#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Hermetic clean-client verifier for signed apt, rpm-md and APK v2 repositories."""

from __future__ import annotations

import argparse
import filecmp
import gzip
import hashlib
import io
import json
import os
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import xml.etree.ElementTree as ET
from pathlib import Path, PurePosixPath

from package_repository_lib import (
    ContractError,
    Package,
    UnmeasurableError,
    digest_file,
    gzip_members,
    load_release_packages,
    regular_file,
)


KEY_PURPOSE = "apt-rpm-apk-repository-metadata"
MANIFEST_SCHEMA = "olivares.ai/package-repositories/v1"
RPM_REPO_NS = "http://linux.duke.edu/metadata/repo"
RPM_COMMON_NS = "http://linux.duke.edu/metadata/common"


def fail(message: str) -> "NoReturn":
    raise ContractError(message)


def blind(message: str) -> "NoReturn":
    raise UnmeasurableError(message)


def require_tool(name: str) -> str:
    path = shutil.which(name)
    if not path:
        blind(f"required clean-client tool is unavailable: {name}")
    return path


def safe_repo_file(root: Path, relative: str, label: str) -> Path:
    pure = PurePosixPath(relative)
    if pure.is_absolute() or not pure.parts or any(part in ("", ".", "..") for part in pure.parts):
        fail(f"{label} path is unsafe: {relative!r}")
    path = root.joinpath(*pure.parts)
    regular_file(path, label)
    try:
        path.resolve().relative_to(root.resolve())
    except ValueError as exc:
        fail(f"{label} escapes repository root: {relative!r}")
    return path


GPG_AGENT_SOCKET = "/S.gpg-agent.browser"
GPG_SUN_PATH_MAX = 107  # Linux sun_path capacity after the terminating NUL.


def require_gpg_home_fit(home: Path, label: str) -> None:
    home_length = len(os.fsencode(home))
    socket_length = home_length + len(GPG_AGENT_SOCKET.encode("ascii"))
    if socket_length > GPG_SUN_PATH_MAX:
        blind(
            f"the gpg homedir for {label} is too long for gpg-agent: "
            f"{home_length} bytes plus {len(GPG_AGENT_SOCKET)} for its socket "
            f"exceeds {GPG_SUN_PATH_MAX} bytes; use a shorter TMPDIR"
        )


class OpenPGPVerifier:
    def __init__(self, key: Path, scratch: Path) -> None:
        regular_file(key, "externally trusted OpenPGP public key")
        self.gpg = require_tool("gpg")
        self.home = scratch / "gnupg-verify"
        # --no-autostart prevents creating an agent for public-key verification, but some gpg
        # builds still try to connect to one after import. Assert the Unix-socket precondition too.
        require_gpg_home_fit(self.home, "the externally trusted key")
        self.home.mkdir(mode=0o700)
        imported = subprocess.run(
            [self.gpg, "--homedir", str(self.home), "--batch", "--no-autostart", "--import", str(key)],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            # stderr is CAPTURED, not discarded. Sending it to DEVNULL cost four CI runs and an
            # hour of somebody's diagnosis: gpg answered 2 because the AGENT would not start
            # while its own output said `imported: 1`, and this check could only report the link
            # it had assumed. A gate that swallows the tool's own words can name nothing but its
            # own guess.
            stderr=subprocess.PIPE,
            check=False,
        )
        if imported.returncode != 0:
            detail = (imported.stderr or b"").decode("utf-8", "replace").strip()
            # Bounded: a tool's stderr is not this process's memory budget, and it ends up in a
            # CI log a human reads.
            if len(detail) > 2000:
                detail = detail[:2000] + " …"
            blind(f"gpg refused the externally trusted OpenPGP key: {detail}")
        listed = subprocess.run(
            [self.gpg, "--homedir", str(self.home), "--batch", "--no-autostart", "--with-colons", "--list-keys"],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        fingerprints = [
            line.split(":")[9]
            for line in listed.stdout.decode("utf-8", "replace").splitlines()
            if line.startswith("fpr:") and len(line.split(":")) > 9
        ]
        if listed.returncode != 0 or not fingerprints:
            blind("externally trusted OpenPGP key has no measurable fingerprint")
        self.fingerprint = fingerprints[0]

    def detached(self, signature: Path, subject: Path, label: str) -> None:
        regular_file(signature, f"{label} signature")
        regular_file(subject, label)
        result = subprocess.run(
            [self.gpg, "--homedir", str(self.home), "--batch", "--no-autostart", "--verify", str(signature), str(subject)],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        if result.returncode != 0:
            fail(f"{label} is not signed by the externally trusted repository key")

    def cleartext(self, signed: Path, output: Path, label: str) -> None:
        regular_file(signed, label)
        result = subprocess.run(
            [
                self.gpg,
                "--homedir",
                str(self.home),
                "--batch", "--no-autostart",
                "--yes",
                "--output",
                str(output),
                "--decrypt",
                str(signed),
            ],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        if result.returncode != 0:
            fail(f"{label} is not clear-signed by the externally trusted repository key")

    def fingerprint_of(self, key: Path, scratch: Path) -> str:
        regular_file(key, "repository-published OpenPGP key")
        home = scratch / "gnupg-published"
        require_gpg_home_fit(home, "the repository-published key")
        home.mkdir(mode=0o700)
        imported = subprocess.run(
            [self.gpg, "--homedir", str(home), "--batch", "--no-autostart", "--import", str(key)],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        listed = subprocess.run(
            [self.gpg, "--homedir", str(home), "--batch", "--no-autostart", "--with-colons", "--list-keys"],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        fingerprints = [
            line.split(":")[9]
            for line in listed.stdout.decode("utf-8", "replace").splitlines()
            if line.startswith("fpr:") and len(line.split(":")) > 9
        ]
        if imported.returncode != 0 or listed.returncode != 0 or not fingerprints:
            fail("repository-published OpenPGP key is not importable")
        return fingerprints[0]


def parse_deb822_stanzas(text: str) -> list[dict[str, str]]:
    stanzas: list[dict[str, str]] = []
    for raw in re.split(r"\n\s*\n", text.strip()):
        fields: dict[str, str] = {}
        current = ""
        for line in raw.splitlines():
            if line.startswith((" ", "\t")):
                if not current:
                    fail("APT Packages contains an orphan continuation")
                fields[current] += "\n" + line[1:]
                continue
            if ":" not in line:
                fail("APT Packages contains a malformed field")
            current, value = line.split(":", 1)
            if current in fields:
                fail(f"APT Packages contains duplicate field {current}")
            fields[current] = value.lstrip()
        stanzas.append(fields)
    return stanzas


def verify_byte_identity(repository_file: Path, release_asset: Path, label: str) -> None:
    if not filecmp.cmp(repository_file, release_asset, shallow=False):
        fail(f"{label} is not byte-identical to the authenticated release asset")


def verify_apt(
    root: Path,
    channel: str,
    expected: dict[str, Package],
    records: list[dict[str, object]],
    pgp: OpenPGPVerifier,
    scratch: Path,
    files: set[str],
) -> None:
    apt_root = root / channel / "apt"
    release_root = apt_root / "dists" / channel
    release = release_root / "Release"
    release_gpg = release_root / "Release.gpg"
    inrelease = release_root / "InRelease"
    for path in (release, release_gpg, inrelease):
        regular_file(path, f"APT {path.name}")
        files.add(path.relative_to(root).as_posix())
    pgp.detached(release_gpg, release, "APT Release")
    clear = scratch / "apt-release.clear"
    pgp.cleartext(inrelease, clear, "APT InRelease")
    if clear.read_bytes() != release.read_bytes():
        fail("APT InRelease cleartext differs from Release")
    text = release.read_text(encoding="utf-8")
    fields: dict[str, str] = {}
    hashes: dict[str, tuple[str, int]] = {}
    in_hashes = False
    for line in text.splitlines():
        if line == "SHA256:":
            in_hashes = True
            continue
        if in_hashes:
            match = re.fullmatch(r" ([0-9a-f]{64}) +([0-9]+) (.+)", line)
            if not match:
                fail("APT Release SHA256 section contains a malformed row")
            digest, size, relative = match.groups()
            if relative in hashes:
                fail(f"APT Release repeats metadata path {relative}")
            hashes[relative] = (digest, int(size))
        else:
            if ":" not in line:
                fail("APT Release contains a malformed field")
            name, value = line.split(":", 1)
            fields[name] = value.lstrip()
    if fields.get("Suite") != channel or fields.get("Codename") != channel:
        fail("APT Release suite/codename differs from the requested channel")
    if fields.get("Architectures") != "amd64 arm64" or fields.get("Components") != "main":
        fail("APT Release architecture/component inventory is not exact")
    expected_hash_paths = {
        f"main/binary-{arch}/Packages{suffix}"
        for arch in ("amd64", "arm64")
        for suffix in ("", ".gz")
    }
    if set(hashes) != expected_hash_paths:
        fail("APT Release does not bind exactly Packages and Packages.gz for both architectures")
    apt_records = {str(record["asset"]): record for record in records}
    if len(apt_records) != 2:
        fail("repository manifest does not contain exactly two apt records")
    for relative, (digest, size) in sorted(hashes.items()):
        metadata = safe_repo_file(release_root, relative, "APT index")
        files.add(metadata.relative_to(root).as_posix())
        if metadata.stat().st_size != size or digest_file(metadata) != digest:
            fail(f"APT Release digest/size differs for {relative}")
        if relative.endswith(".gz"):
            plain = metadata.with_name("Packages")
            if gzip.decompress(metadata.read_bytes()) != plain.read_bytes():
                fail(f"APT compressed index differs from its plain form: {relative}")
            continue
        stanzas = parse_deb822_stanzas(metadata.read_text(encoding="utf-8"))
        if len(stanzas) != 1:
            fail(f"APT clean client expected one package in {relative}")
        stanza = stanzas[0]
        asset_name = Path(stanza.get("Filename", "")).name
        if asset_name not in apt_records or asset_name not in expected:
            fail(f"APT Packages references an unexpected release asset: {asset_name!r}")
        record = apt_records[asset_name]
        package = expected[asset_name]
        if stanza.get("Architecture") != package.arch or stanza.get("Version") != package.version:
            fail(f"APT Packages internal version/architecture drifted for {asset_name}")
        if stanza.get("SHA256") != package.sha256 or stanza.get("Size") != str(package.size):
            fail(f"APT Packages digest/size drifted for {asset_name}")
        repo_file = safe_repo_file(root, str(record["repo_path"]), "APT package")
        files.add(repo_file.relative_to(root).as_posix())
        if digest_file(repo_file) != package.sha256:
            fail(f"APT package bytes differ from signed Packages metadata: {asset_name}")
        verify_byte_identity(repo_file, package.source_path, f"APT package {asset_name}")


def xml_text(parent: ET.Element, path: str, namespaces: dict[str, str], label: str) -> str:
    element = parent.find(path, namespaces)
    if element is None or element.text is None:
        fail(f"RPM metadata is missing {label}")
    return element.text


def verify_rpm(
    root: Path,
    channel: str,
    expected: dict[str, Package],
    records: list[dict[str, object]],
    pgp: OpenPGPVerifier,
    files: set[str],
) -> None:
    rpm_records = {str(record["asset"]): record for record in records}
    if len(rpm_records) != 2:
        fail("repository manifest does not contain exactly two rpm records")
    namespaces = {"repo": RPM_REPO_NS, "common": RPM_COMMON_NS}
    for asset_name, record in sorted(rpm_records.items()):
        package = expected.get(asset_name)
        if package is None:
            fail(f"RPM manifest references an unexpected asset: {asset_name}")
        arch_root = root / channel / "rpm" / package.arch
        repomd = arch_root / "repodata" / "repomd.xml"
        signature = repomd.with_suffix(".xml.asc")
        for path in (repomd, signature):
            regular_file(path, f"RPM {path.name}")
            files.add(path.relative_to(root).as_posix())
        pgp.detached(signature, repomd, f"RPM repomd.xml ({package.arch})")
        try:
            repomd_root = ET.fromstring(repomd.read_bytes())
        except ET.ParseError as exc:
            fail(f"RPM repomd.xml is not XML for {package.arch}: {exc}")
        content = repomd_root.find("repo:tags/repo:content", namespaces)
        if content is None or content.text != f"olivares-{channel}":
            fail(f"RPM repomd.xml channel tag differs for {package.arch}")
        data = repomd_root.findall("repo:data", namespaces)
        if len(data) != 1 or data[0].get("type") != "primary":
            fail(f"RPM repomd.xml must bind exactly primary metadata for {package.arch}")
        item = data[0]
        location = item.find("repo:location", namespaces)
        if location is None or not location.get("href"):
            fail(f"RPM repomd.xml has no primary location for {package.arch}")
        primary_path = safe_repo_file(arch_root, str(location.get("href")), "RPM primary metadata")
        files.add(primary_path.relative_to(root).as_posix())
        compressed = primary_path.read_bytes()
        try:
            primary = gzip.decompress(compressed)
        except (OSError, EOFError) as exc:
            fail(f"RPM primary metadata is not valid gzip for {package.arch}: {exc}")
        checks = {
            "checksum": hashlib.sha256(compressed).hexdigest(),
            "open-checksum": hashlib.sha256(primary).hexdigest(),
            "size": str(len(compressed)),
            "open-size": str(len(primary)),
        }
        for name, wanted in checks.items():
            if xml_text(item, f"repo:{name}", namespaces, name) != wanted:
                fail(f"RPM repomd.xml {name} differs for {package.arch}")
        try:
            primary_root = ET.fromstring(primary)
        except ET.ParseError as exc:
            fail(f"RPM primary metadata is not XML for {package.arch}: {exc}")
        entries = primary_root.findall("common:package", namespaces)
        if len(entries) != 1:
            fail(f"RPM clean client expected one package for {package.arch}")
        entry = entries[0]
        location_entry = entry.find("common:location", namespaces)
        version_entry = entry.find("common:version", namespaces)
        size_entry = entry.find("common:size", namespaces)
        if location_entry is None or version_entry is None or size_entry is None:
            fail(f"RPM primary metadata lacks location/version/size for {package.arch}")
        if location_entry.get("href") != f"Packages/{asset_name}":
            fail(f"RPM primary location differs for {asset_name}")
        if (
            xml_text(entry, "common:name", namespaces, "package name") != package.name
            or xml_text(entry, "common:arch", namespaces, "package arch") != package.arch
            or version_entry.get("ver") != package.version
            or version_entry.get("rel") != package.release
        ):
            fail(f"RPM primary identity differs for {asset_name}")
        if (
            xml_text(entry, "common:checksum", namespaces, "package checksum") != package.sha256
            or size_entry.get("package") != str(package.size)
        ):
            fail(f"RPM primary package digest/size differs for {asset_name}")
        repo_file = safe_repo_file(root, str(record["repo_path"]), "RPM package")
        files.add(repo_file.relative_to(root).as_posix())
        if digest_file(repo_file) != package.sha256:
            fail(f"RPM package bytes differ from signed primary metadata: {asset_name}")
        verify_byte_identity(repo_file, package.source_path, f"RPM package {asset_name}")


def parse_apk_index(text: str) -> list[dict[str, str]]:
    entries: list[dict[str, str]] = []
    for raw in re.split(r"\n\s*\n", text.strip()):
        entry: dict[str, str] = {}
        for line in raw.splitlines():
            if len(line) < 3 or line[1] != ":":
                fail("APKINDEX contains a malformed field")
            if line[0] in entry:
                fail(f"APKINDEX repeats field {line[0]}")
            entry[line[0]] = line[2:]
        entries.append(entry)
    return entries


def verify_apk(
    root: Path,
    channel: str,
    expected: dict[str, Package],
    records: list[dict[str, object]],
    apk_key: Path,
    apk_key_name: str,
    files: set[str],
    scratch: Path,
) -> None:
    openssl = require_tool("openssl")
    regular_file(apk_key, "externally trusted APK public key")
    apk_records = {str(record["asset"]): record for record in records}
    if len(apk_records) != 2:
        fail("repository manifest does not contain exactly two APK records")
    for asset_name, record in sorted(apk_records.items()):
        package = expected.get(asset_name)
        if package is None:
            fail(f"APK manifest references an unexpected asset: {asset_name}")
        arch_root = root / channel / "apk" / package.arch
        index = arch_root / "APKINDEX.tar.gz"
        regular_file(index, f"APKINDEX for {package.arch}")
        files.add(index.relative_to(root).as_posix())
        members = gzip_members(index.read_bytes())
        if len(members) != 2:
            fail(f"signed APKINDEX must contain signature and index gzip streams for {package.arch}")
        try:
            with tarfile.open(fileobj=io.BytesIO(members[0][1]), mode="r:") as archive:
                signatures = archive.getmembers()
                expected_name = f".SIGN.RSA256.{apk_key_name}"
                if len(signatures) != 1 or signatures[0].name != expected_name:
                    fail(f"APKINDEX signature member identity differs for {package.arch}")
                extracted = archive.extractfile(signatures[0])
                if extracted is None:
                    fail(f"APKINDEX signature member is unreadable for {package.arch}")
                signature_bytes = extracted.read()
            with tarfile.open(fileobj=io.BytesIO(members[1][1]), mode="r:") as archive:
                payloads = archive.getmembers()
                if len(payloads) != 1 or payloads[0].name != "APKINDEX":
                    fail(f"APKINDEX data stream inventory differs for {package.arch}")
                extracted = archive.extractfile(payloads[0])
                if extracted is None:
                    fail(f"APKINDEX text is unreadable for {package.arch}")
                index_text = extracted.read().decode("utf-8", "strict")
        except (tarfile.TarError, UnicodeError) as exc:
            fail(f"APKINDEX tar stream is invalid for {package.arch}: {exc}")
        subject = scratch / f"apkindex-{package.arch}.unsigned"
        signature = scratch / f"apkindex-{package.arch}.sig"
        subject.write_bytes(members[1][0])
        signature.write_bytes(signature_bytes)
        verified = subprocess.run(
            [
                openssl,
                "dgst",
                "-sha256",
                "-verify",
                str(apk_key),
                "-signature",
                str(signature),
                str(subject),
            ],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        if verified.returncode != 0:
            fail(f"APKINDEX is not signed by the externally trusted RSA key for {package.arch}")
        entries = parse_apk_index(index_text)
        if len(entries) != 1:
            fail(f"APK clean client expected one package for {package.arch}")
        entry = entries[0]
        if (
            entry.get("P") != package.name
            or entry.get("V") != package.version
            or entry.get("A") != package.arch
            or entry.get("C") != package.apk_control_hash
            or entry.get("S") != str(package.size)
        ):
            fail(f"APKINDEX identity/control hash/size differs for {asset_name}")
        repo_file = safe_repo_file(root, str(record["repo_path"]), "APK package")
        files.add(repo_file.relative_to(root).as_posix())
        if digest_file(repo_file) != package.sha256:
            fail(f"APK package bytes differ from signed release checksum: {asset_name}")
        verify_byte_identity(repo_file, package.source_path, f"APK package {asset_name}")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo-root", required=True, type=Path)
    parser.add_argument("--checksums", required=True, type=Path)
    parser.add_argument("--artifact-dir", required=True, type=Path)
    parser.add_argument("--version", required=True)
    parser.add_argument("--channel", required=True, choices=("stable", "security"))
    parser.add_argument("--openpgp-key", required=True, type=Path)
    parser.add_argument("--apk-key", required=True, type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        if not args.repo_root.is_dir() or args.repo_root.is_symlink():
            blind(f"repository root is missing or unsafe: {args.repo_root}")
        packages = load_release_packages(args.checksums, args.artifact_dir, args.version)
        expected = {package.source_name: package for package in packages}
        scratch_parent = Path(os.environ.get("TMPDIR", "/tmp"))
        if not scratch_parent.is_dir():
            blind(f"TMPDIR is missing: {scratch_parent}")
        # Each character here consumes one byte of gpg-agent's Unix-socket path budget.
        with tempfile.TemporaryDirectory(prefix="verify-pkg-repos.", dir=scratch_parent) as temporary:
            scratch = Path(temporary)
            pgp = OpenPGPVerifier(args.openpgp_key, scratch)
            manifest = args.repo_root / args.channel / "repository-manifest.json"
            manifest_sig = manifest.with_suffix(".json.asc")
            pgp.detached(manifest_sig, manifest, "repository manifest")
            try:
                value = json.loads(manifest.read_text(encoding="utf-8"))
            except (OSError, UnicodeError, json.JSONDecodeError) as exc:
                fail(f"repository manifest is not readable JSON: {exc}")
            required = {
                "schema",
                "schema_version",
                "version",
                "channel",
                "source_date_epoch",
                "valid_until_epoch",
                "generated_from",
                "keys",
                "packages",
            }
            if not isinstance(value, dict) or set(value) != required:
                fail("repository manifest fields are not exact")
            if (
                value["schema"] != MANIFEST_SCHEMA
                or value["schema_version"] != 1
                or value["version"] != args.version
                or value["channel"] != args.channel
            ):
                fail("repository manifest identity/version/channel differs")
            generated = value.get("generated_from")
            if not isinstance(generated, dict) or generated != {
                "checksums": "checksums.txt",
                "checksums_sha256": digest_file(args.checksums),
            }:
                fail("repository manifest is not bound to the supplied release checksums")
            keys = value.get("keys")
            if not isinstance(keys, dict) or keys.get("purpose") != KEY_PURPOSE:
                fail("repository manifest key purpose is not the package-repository domain")
            if keys.get("openpgp_fingerprint") != pgp.fingerprint:
                fail("externally trusted OpenPGP key differs from repository manifest")
            apk_key_name = str(keys.get("apk_public_key_name", ""))
            if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._+-]*\.rsa\.pub", apk_key_name):
                fail("repository manifest APK key name is unsafe")
            regular_file(args.apk_key, "externally trusted APK public key")
            if digest_file(args.apk_key) != keys.get("apk_public_key_sha256"):
                fail("externally trusted APK key differs from repository manifest")
            published_pgp = args.repo_root / "keys" / "olivares-package-repository.asc"
            published_apk = args.repo_root / "keys" / apk_key_name
            if pgp.fingerprint_of(published_pgp, scratch) != pgp.fingerprint:
                fail("repository-published OpenPGP key differs from the external trust anchor")
            regular_file(published_apk, "repository-published APK key")
            if published_apk.read_bytes() != args.apk_key.read_bytes():
                fail("repository-published APK key differs from the external trust anchor")
            records = value.get("packages")
            if not isinstance(records, list) or len(records) != 6:
                fail("repository manifest must contain exactly six native package records")
            by_format: dict[str, list[dict[str, object]]] = {"apt": [], "rpm": [], "apk": []}
            source_format = {"apt": "deb", "rpm": "rpm", "apk": "apk"}
            seen_assets: set[str] = set()
            for record in records:
                if not isinstance(record, dict) or set(record) != {
                    "format",
                    "asset",
                    "asset_arch",
                    "internal_arch",
                    "repo_path",
                    "sha256",
                    "size",
                    "version",
                }:
                    fail("repository manifest package record fields are not exact")
                fmt = str(record["format"])
                asset = str(record["asset"])
                package = expected.get(asset)
                if fmt not in by_format or package is None or package.format != source_format[fmt]:
                    fail(f"repository manifest references an unexpected package: {asset}")
                if asset in seen_assets:
                    fail(f"repository manifest repeats package: {asset}")
                seen_assets.add(asset)
                expected_record_version = (
                    f"{package.version}-{package.release}" if fmt == "rpm" else package.version
                )
                if (
                    record["asset_arch"] != package.asset_arch
                    or record["internal_arch"] != package.arch
                    or record["version"] != expected_record_version
                    or record["sha256"] != package.sha256
                    or record["size"] != package.size
                    or not str(record["repo_path"]).startswith(f"{args.channel}/{fmt}/")
                ):
                    fail(f"repository manifest metadata differs for {asset}")
                by_format[fmt].append(record)
            if seen_assets != set(expected):
                fail("repository manifest omits an authenticated release package")
            files = {
                manifest.relative_to(args.repo_root).as_posix(),
                manifest_sig.relative_to(args.repo_root).as_posix(),
                published_pgp.relative_to(args.repo_root).as_posix(),
                published_apk.relative_to(args.repo_root).as_posix(),
            }
            verify_apt(args.repo_root, args.channel, expected, by_format["apt"], pgp, scratch, files)
            verify_rpm(args.repo_root, args.channel, expected, by_format["rpm"], pgp, files)
            verify_apk(
                args.repo_root,
                args.channel,
                expected,
                by_format["apk"],
                args.apk_key,
                apk_key_name,
                files,
                scratch,
            )
            actual: set[str] = set()
            for path in args.repo_root.rglob("*"):
                if path.is_symlink():
                    fail(f"repository contains a symlink: {path.relative_to(args.repo_root)}")
                if path.is_file():
                    actual.add(path.relative_to(args.repo_root).as_posix())
            if actual != files:
                fail(
                    "repository file inventory differs; "
                    f"unexpected={sorted(actual - files)}, missing={sorted(files - actual)}"
                )
        print(
            f"verify-package-repositories: OK — {args.channel}, apt/rpm/apk signatures valid, "
            "6 repository packages byte-identical to release assets"
        )
        return 0
    except ContractError as exc:
        print(f"verify-package-repositories: HALLAZGO — {exc}", file=sys.stderr)
        return 1
    except UnmeasurableError as exc:
        print(f"verify-package-repositories: NO HE PODIDO MIRAR — {exc}", file=sys.stderr)
        return 2
    except (OSError, ValueError) as exc:
        print(f"verify-package-repositories: NO HE PODIDO MIRAR — local operation failed: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
