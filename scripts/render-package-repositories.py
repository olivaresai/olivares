#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Render deterministic, signed apt/rpm-md/apk repositories from release packages."""

from __future__ import annotations

import argparse
import datetime as dt
import gzip
import hashlib
import io
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
from email.utils import format_datetime
from html import escape
from pathlib import Path

from package_repository_lib import (
    ContractError,
    Package,
    SHA256,
    UnmeasurableError,
    digest_file,
    load_release_packages,
    write_canonical_json,
)


KEY_SCHEMA = "olivares.ai/package-repository-key/v1"
MANIFEST_SCHEMA = "olivares.ai/package-repositories/v1"
KEY_PURPOSE = "apt-rpm-apk-repository-metadata"


def fail(message: str) -> "NoReturn":
    raise ContractError(message)


def blind(message: str) -> "NoReturn":
    raise UnmeasurableError(message)


def require_tool(name: str) -> str:
    path = shutil.which(name)
    if not path:
        blind(f"required tool is unavailable: {name}")
    return path


def secret_file_from_env(name: str) -> Path:
    raw = os.environ.get(name, "")
    if not raw:
        blind(f"NO PUEDO FIRMAR: required key file variable is absent: {name}")
    path = Path(raw)
    if not path.is_file() or path.is_symlink():
        blind(f"NO PUEDO FIRMAR: key input from {name} is missing, not regular, or is a symlink")
    mode = stat.S_IMODE(path.stat().st_mode)
    if mode & 0o077:
        fail(f"key input from {name} is mode {mode:04o}; require no group/other access")
    return path


def read_descriptor() -> tuple[dict[str, object], Path, Path, Path | None, Path | None]:
    descriptor_raw = os.environ.get("OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE", "")
    if not descriptor_raw:
        blind("NO PUEDO FIRMAR: OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE is absent")
    descriptor_path = Path(descriptor_raw)
    if not descriptor_path.is_file() or descriptor_path.is_symlink():
        blind("NO PUEDO FIRMAR: repository key descriptor file is missing, not regular, or is a symlink")
    try:
        descriptor = json.loads(descriptor_path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        fail("repository key descriptor is not readable canonical JSON")
    required_keys = {
        "schema",
        "purpose",
        "environment",
        "openpgp_fingerprint",
        "apk_public_key_name",
        "apk_public_key_sha256",
    }
    if not isinstance(descriptor, dict) or set(descriptor) != required_keys:
        fail("repository key descriptor fields are not exact")
    if descriptor["schema"] != KEY_SCHEMA or descriptor["purpose"] != KEY_PURPOSE:
        fail("repository key descriptor has the wrong schema or purpose")
    environment = descriptor["environment"]
    if environment not in ("test", "preprod", "production"):
        fail("repository key descriptor environment is not test, preprod, or production")
    test_only = os.environ.get("OLIVARES_PACKAGE_REPO_TEST_ONLY", "")
    if environment == "test" and test_only != "1":
        fail("test repository key is forbidden outside an explicit test-only battery")
    if environment != "test" and test_only:
        fail("OLIVARES_PACKAGE_REPO_TEST_ONLY cannot accompany a non-test key")
    fingerprint = str(descriptor["openpgp_fingerprint"])
    if not re.fullmatch(r"[0-9A-F]{40}", fingerprint):
        fail("repository OpenPGP fingerprint must be uppercase 40-hex")
    key_name = str(descriptor["apk_public_key_name"])
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._+-]*\.rsa\.pub", key_name):
        fail("APK public key name is unsafe or lacks .rsa.pub")
    if not SHA256.fullmatch(str(descriptor["apk_public_key_sha256"])):
        fail("APK public key fingerprint must be lowercase SHA-256")

    openpgp_secret = secret_file_from_env("OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE")
    apk_secret = secret_file_from_env("OLIVARES_PACKAGE_REPO_APK_PRIVATE_KEY_FILE")
    openpgp_passphrase = None
    apk_passphrase = None
    if os.environ.get("OLIVARES_PACKAGE_REPO_OPENPGP_PASSPHRASE_FILE"):
        openpgp_passphrase = secret_file_from_env("OLIVARES_PACKAGE_REPO_OPENPGP_PASSPHRASE_FILE")
    if os.environ.get("OLIVARES_PACKAGE_REPO_APK_PASSPHRASE_FILE"):
        apk_passphrase = secret_file_from_env("OLIVARES_PACKAGE_REPO_APK_PASSPHRASE_FILE")
    return descriptor, openpgp_secret, apk_secret, openpgp_passphrase, apk_passphrase


class Signer:
    def __init__(
        self,
        descriptor: dict[str, object],
        openpgp_secret: Path,
        apk_secret: Path,
        openpgp_passphrase: Path | None,
        apk_passphrase: Path | None,
        epoch: int,
        scratch: Path,
    ) -> None:
        self.descriptor = descriptor
        self.fingerprint = str(descriptor["openpgp_fingerprint"])
        self.apk_public_name = str(descriptor["apk_public_key_name"])
        self.apk_secret = apk_secret
        self.apk_passphrase = apk_passphrase
        self.epoch = epoch
        self.gpg = require_tool("gpg")
        self.openssl = require_tool("openssl")
        self.home = scratch / "gnupg"
        self.home.mkdir(mode=0o700)
        import_result = subprocess.run(
            [self.gpg, "--homedir", str(self.home), "--batch", "--import", str(openpgp_secret)],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
            check=False,
        )
        if import_result.returncode != 0:
            blind("NO PUEDO FIRMAR: OpenPGP secret key import failed")
        listed = subprocess.run(
            [self.gpg, "--homedir", str(self.home), "--batch", "--with-colons", "--list-secret-keys"],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        if listed.returncode != 0:
            blind("NO PUEDO FIRMAR: imported OpenPGP secret key cannot be enumerated")
        fingerprints = {
            line.split(":")[9]
            for line in listed.stdout.decode("utf-8", "replace").splitlines()
            if line.startswith("fpr:") and len(line.split(":")) > 9
        }
        if self.fingerprint not in fingerprints:
            fail("OpenPGP secret key does not match the descriptor fingerprint")
        self.gpg_passphrase_args = ["--pinentry-mode", "loopback"]
        if openpgp_passphrase:
            self.gpg_passphrase_args += ["--passphrase-file", str(openpgp_passphrase)]
        else:
            self.gpg_passphrase_args += ["--passphrase", ""]
        self.openssl_passphrase_args: list[str] = []
        if apk_passphrase:
            self.openssl_passphrase_args = ["-passin", f"file:{apk_passphrase}"]
        public_result = subprocess.run(
            [
                self.openssl,
                "pkey",
                "-in",
                str(apk_secret),
                *self.openssl_passphrase_args,
                "-pubout",
            ],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        if public_result.returncode != 0 or not public_result.stdout.startswith(b"-----BEGIN PUBLIC KEY-----"):
            blind("NO PUEDO FIRMAR: APK RSA private key cannot yield a public key")
        self.apk_public = public_result.stdout
        if hashlib.sha256(self.apk_public).hexdigest() != descriptor["apk_public_key_sha256"]:
            fail("APK RSA private key does not match the descriptor fingerprint")

    def export_openpgp(self) -> bytes:
        result = subprocess.run(
            [
                self.gpg,
                "--homedir",
                str(self.home),
                "--batch",
                "--armor",
                "--export-options",
                "export-minimal",
                "--export",
                self.fingerprint,
            ],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        if result.returncode != 0 or b"BEGIN PGP PUBLIC KEY BLOCK" not in result.stdout:
            blind("NO PUEDO FIRMAR: OpenPGP public key export failed")
        return result.stdout

    def _gpg_sign(self, subject: Path, output: Path, clear: bool) -> None:
        mode = "--clearsign" if clear else "--detach-sign"
        command = [
            self.gpg,
            "--homedir",
            str(self.home),
            "--batch",
            "--yes",
            *self.gpg_passphrase_args,
            "--faked-system-time",
            f"{self.epoch}!",
            "--local-user",
            f"{self.fingerprint}!",
            "--digest-algo",
            "SHA256",
            "--armor",
            mode,
            "--output",
            str(output),
            str(subject),
        ]
        result = subprocess.run(
            command,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        if result.returncode != 0:
            blind(f"NO PUEDO FIRMAR: OpenPGP signing failed for {subject.name}")

    def detach(self, subject: Path, output: Path) -> None:
        self._gpg_sign(subject, output, False)

    def clearsign(self, subject: Path, output: Path) -> None:
        self._gpg_sign(subject, output, True)

    def apk_signature(self, unsigned_index: Path, output: Path) -> None:
        command = [
            self.openssl,
            "dgst",
            "-sha256",
            "-sign",
            str(self.apk_secret),
            *self.openssl_passphrase_args,
            "-out",
            str(output),
            str(unsigned_index),
        ]
        result = subprocess.run(
            command,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        if result.returncode != 0 or not output.is_file() or output.stat().st_size == 0:
            blind("NO PUEDO FIRMAR: APK RSA256 signing failed")


def gzip_bytes(payload: bytes) -> bytes:
    encoded = bytearray(gzip.compress(payload, compresslevel=9, mtime=0))
    if len(encoded) < 10:
        fail("internal gzip encoder returned a truncated stream")
    encoded[9] = 255  # Make the OS header byte independent of the Python host.
    return bytes(encoded)


def tar_one(name: str, payload: bytes, epoch: int) -> bytes:
    stream = io.BytesIO()
    with tarfile.open(fileobj=stream, mode="w", format=tarfile.USTAR_FORMAT) as archive:
        info = tarfile.TarInfo(name)
        info.size = len(payload)
        info.mode = 0o644
        info.uid = 0
        info.gid = 0
        info.uname = ""
        info.gname = ""
        info.mtime = epoch
        archive.addfile(info, io.BytesIO(payload))
    return stream.getvalue()


def write_bytes(path: Path, payload: bytes, epoch: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(payload)
    path.chmod(0o644)
    os.utime(path, (epoch, epoch), follow_symlinks=False)


def copy_package(package: Package, destination: Path, epoch: int) -> None:
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(package.source_path, destination)
    destination.chmod(0o644)
    os.utime(destination, (epoch, epoch), follow_symlinks=False)
    if digest_file(destination) != package.sha256:
        fail(f"copied repository package differs from release asset: {package.source_name}")


def render_apt(root: Path, channel: str, packages: list[Package], signer: Signer, epoch: int, valid: int) -> list[dict[str, object]]:
    apt_root = root / channel / "apt"
    records: list[dict[str, object]] = []
    release_hashes: list[tuple[str, int, str]] = []
    for package in sorted((item for item in packages if item.format == "deb"), key=lambda item: item.arch):
        pool_rel = Path("pool/main/o/olivares") / package.source_name
        copy_package(package, apt_root / pool_rel, epoch)
        packages_rel = Path("main") / f"binary-{package.arch}" / "Packages"
        package_index = (
            package.control_text.rstrip("\n")
            + f"\nFilename: {pool_rel.as_posix()}\n"
            + f"Size: {package.size}\n"
            + f"SHA256: {package.sha256}\n"
            + f"SHA512: {digest_file(package.source_path, 'sha512')}\n\n"
        ).encode("utf-8")
        packages_path = apt_root / "dists" / channel / packages_rel
        write_bytes(packages_path, package_index, epoch)
        compressed_path = packages_path.with_name("Packages.gz")
        write_bytes(compressed_path, gzip_bytes(package_index), epoch)
        for candidate in (packages_path, compressed_path):
            relative = candidate.relative_to(apt_root / "dists" / channel).as_posix()
            release_hashes.append((digest_file(candidate), candidate.stat().st_size, relative))
        records.append(
            {
                "format": "apt",
                "asset": package.source_name,
                "asset_arch": package.asset_arch,
                "internal_arch": package.arch,
                "repo_path": (Path(channel) / "apt" / pool_rel).as_posix(),
                "sha256": package.sha256,
                "size": package.size,
                "version": package.version,
            }
        )
    date = dt.datetime.fromtimestamp(epoch, tz=dt.timezone.utc)
    valid_until = dt.datetime.fromtimestamp(valid, tz=dt.timezone.utc)
    release = [
        "Origin: Olivares.AI",
        "Label: Olivares AI",
        f"Suite: {channel}",
        f"Codename: {channel}",
        f"Version: {packages[0].source_name.split('_')[1]}",
        f"Date: {format_datetime(date, usegmt=True)}",
        f"Valid-Until: {format_datetime(valid_until, usegmt=True)}",
        "Architectures: amd64 arm64",
        "Components: main",
        f"Description: Olivares AI {channel} repository",
        "SHA256:",
    ]
    for digest, size, relative in sorted(release_hashes, key=lambda item: item[2]):
        release.append(f" {digest} {size:16d} {relative}")
    release_path = apt_root / "dists" / channel / "Release"
    write_bytes(release_path, ("\n".join(release) + "\n").encode("utf-8"), epoch)
    signer.detach(release_path, release_path.with_name("Release.gpg"))
    signer.clearsign(release_path, release_path.with_name("InRelease"))
    for signed in (release_path.with_name("Release.gpg"), release_path.with_name("InRelease")):
        signed.chmod(0o644)
        os.utime(signed, (epoch, epoch), follow_symlinks=False)
    return records


def rpm_primary(package: Package, location: str, epoch: int) -> bytes:
    summary = " ".join(package.description.splitlines()).strip()
    evr = f'{package.version}-{package.release}'
    provides = package.provides or (package.name,)
    entries = "".join(
        f'<rpm:entry name="{escape(name)}"/>' for name in sorted(set(provides))
    )
    value = f'''<?xml version="1.0" encoding="UTF-8"?>
<metadata xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm" packages="1">
  <package type="rpm">
    <name>{escape(package.name)}</name>
    <arch>{escape(package.arch)}</arch>
    <version epoch="{package.epoch}" ver="{escape(package.version)}" rel="{escape(package.release)}"/>
    <checksum type="sha256" pkgid="YES">{package.sha256}</checksum>
    <summary>{escape(summary)}</summary>
    <description>{escape(package.description)}</description>
    <packager>{escape(package.maintainer)}</packager>
    <url>{escape(package.url)}</url>
    <time file="{epoch}" build="{package.build_time or epoch}"/>
    <size package="{package.size}" installed="{package.installed_size}" archive="{package.installed_size}"/>
    <location href="{escape(location)}"/>
    <format>
      <rpm:license>{escape(package.license)}</rpm:license>
      <rpm:vendor>{escape(package.vendor)}</rpm:vendor>
      <rpm:group>{escape(package.group)}</rpm:group>
      <rpm:buildhost></rpm:buildhost>
      <rpm:sourcerpm>{escape(package.name)}-{escape(evr)}.src.rpm</rpm:sourcerpm>
      <rpm:header-range start="{package.rpm_header_start}" end="{package.rpm_header_end}"/>
      <rpm:provides>{entries}</rpm:provides>
    </format>
  </package>
</metadata>
'''
    return value.encode("utf-8")


def render_rpm(root: Path, channel: str, packages: list[Package], signer: Signer, epoch: int) -> list[dict[str, object]]:
    records: list[dict[str, object]] = []
    for package in sorted((item for item in packages if item.format == "rpm"), key=lambda item: item.arch):
        arch_root = root / channel / "rpm" / package.arch
        package_rel = Path("Packages") / package.source_name
        copy_package(package, arch_root / package_rel, epoch)
        primary = rpm_primary(package, package_rel.as_posix(), epoch)
        primary_gz = gzip_bytes(primary)
        primary_digest = hashlib.sha256(primary_gz).hexdigest()
        primary_name = f"{primary_digest}-primary.xml.gz"
        repodata = arch_root / "repodata"
        write_bytes(repodata / primary_name, primary_gz, epoch)
        repomd = f'''<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <revision>{epoch}</revision>
  <tags><content>olivares-{channel}</content></tags>
  <data type="primary">
    <checksum type="sha256">{primary_digest}</checksum>
    <open-checksum type="sha256">{hashlib.sha256(primary).hexdigest()}</open-checksum>
    <location href="repodata/{primary_name}"/>
    <timestamp>{epoch}</timestamp>
    <size>{len(primary_gz)}</size>
    <open-size>{len(primary)}</open-size>
  </data>
</repomd>
'''.encode("utf-8")
        repomd_path = repodata / "repomd.xml"
        write_bytes(repomd_path, repomd, epoch)
        signature = repodata / "repomd.xml.asc"
        signer.detach(repomd_path, signature)
        signature.chmod(0o644)
        os.utime(signature, (epoch, epoch), follow_symlinks=False)
        records.append(
            {
                "format": "rpm",
                "asset": package.source_name,
                "asset_arch": package.asset_arch,
                "internal_arch": package.arch,
                "repo_path": (Path(channel) / "rpm" / package.arch / package_rel).as_posix(),
                "sha256": package.sha256,
                "size": package.size,
                "version": f"{package.version}-{package.release}",
            }
        )
    return records


def apk_index_entry(package: Package, epoch: int) -> str:
    description = " ".join(package.description.splitlines()).strip()
    fields = [
        f"C:{package.apk_control_hash}",
        f"P:{package.name}",
        f"V:{package.version}",
        f"A:{package.arch}",
        f"S:{package.size}",
        f"I:{package.installed_size}",
        f"T:{description}",
        f"U:{package.url}",
        f"L:{package.license}",
        f"o:{package.name}",
    ]
    if package.maintainer:
        fields.append(f"m:{package.maintainer}")
    fields.append(f"t:{package.build_time or epoch}")
    if package.provides:
        fields.append("p:" + " ".join(sorted(package.provides)))
    return "\n".join(fields) + "\n\n"


def render_apk(root: Path, channel: str, packages: list[Package], signer: Signer, epoch: int, scratch: Path) -> list[dict[str, object]]:
    records: list[dict[str, object]] = []
    for package in sorted((item for item in packages if item.format == "apk"), key=lambda item: item.arch):
        arch_root = root / channel / "apk" / package.arch
        repo_name = f"{package.name}-{package.version}.apk"
        copy_package(package, arch_root / repo_name, epoch)
        index_text = apk_index_entry(package, epoch).encode("utf-8")
        unsigned = gzip_bytes(tar_one("APKINDEX", index_text, epoch))
        unsigned_path = scratch / f"APKINDEX-{package.arch}.unsigned.tar.gz"
        signature_path = scratch / f"APKINDEX-{package.arch}.signature"
        write_bytes(unsigned_path, unsigned, epoch)
        signer.apk_signature(unsigned_path, signature_path)
        signature_tar = gzip_bytes(tar_one(f".SIGN.RSA256.{signer.apk_public_name}", signature_path.read_bytes(), epoch))
        write_bytes(arch_root / "APKINDEX.tar.gz", signature_tar + unsigned, epoch)
        records.append(
            {
                "format": "apk",
                "asset": package.source_name,
                "asset_arch": package.asset_arch,
                "internal_arch": package.arch,
                "repo_path": (Path(channel) / "apk" / package.arch / repo_name).as_posix(),
                "sha256": package.sha256,
                "size": package.size,
                "version": package.version,
            }
        )
    return records


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--checksums", required=True, type=Path)
    parser.add_argument("--artifact-dir", required=True, type=Path)
    parser.add_argument("--version", required=True)
    parser.add_argument("--channel", required=True, choices=("stable", "security"))
    parser.add_argument("--source-date-epoch", required=True, type=int)
    parser.add_argument("--valid-until-epoch", required=True, type=int)
    parser.add_argument("--out", required=True, type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        if args.source_date_epoch < 946684800:
            blind("source-date-epoch must be a post-2000 UNIX timestamp")
        if args.valid_until_epoch <= args.source_date_epoch:
            blind("valid-until-epoch must be later than source-date-epoch")
        if args.checksums.name != "checksums.txt":
            blind("checksums input must be named checksums.txt")
        if args.out.exists():
            fail(f"output already exists; refusing replacement: {args.out}")
        if not args.out.parent.is_dir():
            blind(f"output parent is missing: {args.out.parent}")
        descriptor, openpgp_secret, apk_secret, openpgp_passphrase, apk_passphrase = read_descriptor()
        packages = load_release_packages(args.checksums, args.artifact_dir, args.version)
        scratch_parent = Path(os.environ.get("TMPDIR", "/tmp"))
        if not scratch_parent.is_dir():
            blind(f"TMPDIR is missing: {scratch_parent}")
        with tempfile.TemporaryDirectory(prefix="package-repositories.", dir=scratch_parent) as temporary:
            scratch = Path(temporary)
            signer = Signer(
                descriptor,
                openpgp_secret,
                apk_secret,
                openpgp_passphrase,
                apk_passphrase,
                args.source_date_epoch,
                scratch,
            )
            staging = scratch / "repository"
            staging.mkdir()
            keys = staging / "keys"
            write_bytes(keys / "olivares-package-repository.asc", signer.export_openpgp(), args.source_date_epoch)
            write_bytes(keys / signer.apk_public_name, signer.apk_public, args.source_date_epoch)
            records = []
            records += render_apt(
                staging, args.channel, packages, signer, args.source_date_epoch, args.valid_until_epoch
            )
            records += render_rpm(staging, args.channel, packages, signer, args.source_date_epoch)
            records += render_apk(staging, args.channel, packages, signer, args.source_date_epoch, scratch)
            manifest = {
                "schema": MANIFEST_SCHEMA,
                "schema_version": 1,
                "version": args.version,
                "channel": args.channel,
                "source_date_epoch": args.source_date_epoch,
                "valid_until_epoch": args.valid_until_epoch,
                "generated_from": {
                    "checksums": "checksums.txt",
                    "checksums_sha256": digest_file(args.checksums),
                },
                "keys": {
                    "purpose": KEY_PURPOSE,
                    "environment": descriptor["environment"],
                    "openpgp_fingerprint": descriptor["openpgp_fingerprint"],
                    "apk_public_key_name": descriptor["apk_public_key_name"],
                    "apk_public_key_sha256": descriptor["apk_public_key_sha256"],
                },
                "packages": sorted(records, key=lambda item: (str(item["format"]), str(item["asset_arch"]))),
            }
            manifest_path = staging / args.channel / "repository-manifest.json"
            manifest_path.parent.mkdir(parents=True, exist_ok=True)
            write_canonical_json(manifest_path, manifest)
            manifest_path.chmod(0o644)
            os.utime(manifest_path, (args.source_date_epoch, args.source_date_epoch), follow_symlinks=False)
            manifest_signature = manifest_path.with_suffix(".json.asc")
            signer.detach(manifest_path, manifest_signature)
            manifest_signature.chmod(0o644)
            os.utime(
                manifest_signature,
                (args.source_date_epoch, args.source_date_epoch),
                follow_symlinks=False,
            )
            for directory, directories, _ in os.walk(staging):
                Path(directory).chmod(0o755)
                for name in directories:
                    (Path(directory) / name).chmod(0o755)
            os.replace(staging, args.out)
        print(
            f"render-package-repositories: OK — {args.channel}, 6 release assets, "
            f"apt/rpm/apk metadata signed -> {args.out}"
        )
        return 0
    except ContractError as exc:
        print(f"render-package-repositories: HALLAZGO — {exc}", file=sys.stderr)
        return 1
    except UnmeasurableError as exc:
        print(f"render-package-repositories: NO HE PODIDO MIRAR — {exc}", file=sys.stderr)
        return 2
    except (OSError, ValueError) as exc:
        print(f"render-package-repositories: NO HE PODIDO MIRAR — local operation failed: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
