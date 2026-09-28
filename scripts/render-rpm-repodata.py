#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Build a signed rpm-md repository with createrepo_c.

createrepo_c(8) synopsis, retrieved 2026-09-27 from the upstream manual:

    createrepo_c [options] <directory>

``--checksum sha256`` is the documented default, named here so a later
default cannot change the hash the client checks. ``--general-compress-type
gz`` is a documented compression type and keeps the open checksum readable
with the Python standard library.

The same package-repository OpenPGP key that signs rpm headers signs
``repodata/repomd.xml`` into ``repodata/repomd.xml.asc``. Every ``repomd``
data entry's location, size, and checksum must resolve inside the repository.
A metadata file under ``repodata/`` that the signed repomd does not name is
refused.

After the signature, ``delivery.json`` is written beside ``repodata/``
(schema ``olivares.ai/rpm-delivery/v1``): the signing key fingerprint, the
sha256 of ``repomd.xml`` and ``repomd.xml.asc``, and one entry per package in
the signed primary metadata with its name, NEVRA, arch, file and sha256. The
NEVRA omits a zero epoch. ``--expect-package`` names the exact package set a
consumer requires; the appliance lane passes ``olivares`` and
``olivares-appliance-base``. The renderer verifies the manifest against the
tree before it writes ``checksums.txt``; ``verify-delivery`` repeats that check.
"""

from __future__ import annotations

import argparse
import bz2
import contextlib
import gzip
import hashlib
import json
import lzma
import os
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import xml.etree.ElementTree as ET
from collections.abc import Iterator
from pathlib import Path
from typing import NoReturn


KEY_SCHEMA = "olivares.ai/package-repository-key/v1"
KEY_PURPOSE = "apt-rpm-apk-repository-metadata"
REPO_NS = "http://linux.duke.edu/metadata/repo"
# createrepo_c(8). The self-test matches this tuple.
DOCUMENTED_CREATEREPO_ARGV = ("createrepo_c", "--checksum", "sha256", "--general-compress-type", "gz")
FINGERPRINT = re.compile(r"[0-9A-F]{40}")
HASHES = {
    "sha256": hashlib.sha256,
    "sha384": hashlib.sha384,
    "sha512": hashlib.sha512,
    "sha1": hashlib.sha1,
    "md5": hashlib.md5,
}
STANDARD_TYPES = ("primary", "filelists", "other")
REPOMD_NAME = "repomd.xml"
SIGNATURE_NAME = "repomd.xml.asc"
DELIVERY_NAME = "delivery.json"
DELIVERY_SCHEMA = "olivares.ai/rpm-delivery/v1"
DELIVERY_FIELDS = {"schema", "key_fingerprint", "repomd_sha256", "repomd_asc_sha256", "packages"}
PACKAGE_FIELDS = {"name", "nevra", "arch", "file", "sha256"}
SHA256_HEX = re.compile(r"[0-9a-f]{64}")
MAX_PRIMARY_BYTES = 256 * 1024 * 1024
MAX_REPOMD_BYTES = 20 * 1024 * 1024
MAX_OPEN_BYTES = 4 * 1024 * 1024 * 1024
# Compressions whose open-checksum and open-size this checker can recompute with
# the standard library, by leading magic bytes. createrepo_c(8) also writes zstd
# and zchunk; a repomd that supplies open fields for those is refused.
OPENERS = (
    (b"\x1f\x8b", gzip.open),
    (b"BZh", bz2.open),
    (b"\xfd7zXZ\x00", lzma.open),
)


def stderr(message: str) -> None:
    print(f"rpm-repodata: {message}", file=sys.stderr)


def refuse(message: str) -> NoReturn:
    stderr(f"refused — {message}")
    raise SystemExit(1)


def blind(message: str) -> NoReturn:
    stderr(f"could not check — {message}")
    raise SystemExit(2)


def which(name: str) -> str | None:
    for directory in os.environ.get("PATH", "").split(os.pathsep):
        if not directory:
            continue
        candidate = Path(directory) / name
        if candidate.is_file() and os.access(candidate, os.X_OK):
            return str(candidate)
    return None


def require_tool(name: str) -> str:
    found = which(name)
    if found is None:
        blind(f"{name} is absent")
    return found


def secret_file(name: str) -> Path:
    raw = os.environ.get(name, "")
    if not raw:
        blind(f"required key file variable is absent: {name}")
    path = Path(raw)
    if not path.is_absolute() or not path.is_file() or path.is_symlink():
        blind(f"key input from {name} is missing, not absolute, not regular, or is a symlink")
    mode = stat.S_IMODE(path.stat().st_mode)
    if mode & 0o077:
        refuse(f"key input from {name} is mode {mode:04o}; require no group or other access")
    return path


def optional_secret(name: str) -> Path | None:
    if not os.environ.get(name):
        return None
    return secret_file(name)


def load_fingerprint() -> str:
    raw = os.environ.get("OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE", "")
    if not raw:
        blind("OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE is absent")
    path = Path(raw)
    if not path.is_absolute() or not path.is_file() or path.is_symlink():
        blind("repository key descriptor is missing, not absolute, not regular, or is a symlink")
    try:
        descriptor = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError):
        refuse("repository key descriptor is not readable JSON")
    required = {
        "schema",
        "purpose",
        "environment",
        "openpgp_fingerprint",
        "apk_public_key_name",
        "apk_public_key_sha256",
    }
    if not isinstance(descriptor, dict) or set(descriptor) != required:
        refuse("repository key descriptor fields are not exact")
    if descriptor["schema"] != KEY_SCHEMA or descriptor["purpose"] != KEY_PURPOSE:
        refuse("repository key descriptor has the wrong schema or purpose")
    environment = descriptor["environment"]
    if environment not in ("test", "preprod", "production"):
        refuse("repository key descriptor environment is not test, preprod, or production")
    test_only = os.environ.get("OLIVARES_PACKAGE_REPO_TEST_ONLY", "")
    if environment == "test" and test_only != "1":
        refuse("test repository key is forbidden outside an explicit test-only battery")
    if environment != "test" and test_only:
        refuse("OLIVARES_PACKAGE_REPO_TEST_ONLY cannot accompany a non-test key")
    fingerprint = str(descriptor["openpgp_fingerprint"])
    if not FINGERPRINT.fullmatch(fingerprint):
        refuse("repository OpenPGP fingerprint must be uppercase 40-hex")
    return fingerprint


def absolute_directory(raw: str) -> Path:
    path = Path(raw)
    if not path.is_absolute() or not path.is_dir() or path.is_symlink():
        blind(f"repository path is not an absolute real directory: {raw}")
    return path


def run(command: list[str], env: dict[str, str] | None = None) -> subprocess.CompletedProcess[bytes]:
    return subprocess.run(
        command,
        env=env,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )


# gpg-agent binds $GNUPGHOME/S.gpg-agent* sockets; Linux sun_path holds 107 bytes
# plus the terminating NUL. A long TMPDIR must not turn a check into a refusal.
GPG_AGENT_SOCKET = "/S.gpg-agent.browser"
SUN_PATH_MAX = 107


@contextlib.contextmanager
def gnupg_home() -> Iterator[Path]:
    """Yield a new GnuPG home path whose agent socket fits sun_path.

    The home is inside an owned 0700 temporary directory under TMPDIR, or under
    /tmp when TMPDIR is too long. Its agent is stopped and the directory removed
    afterwards. When neither fits, the check could not run: exit 2.
    """
    for base in (os.environ.get("TMPDIR") or tempfile.gettempdir(), "/tmp"):
        if not os.path.isdir(base):
            continue
        with tempfile.TemporaryDirectory(prefix="gpg-", dir=base) as temporary:
            home = Path(temporary) / "h"
            if len(os.fsencode(home)) + len(GPG_AGENT_SOCKET) > SUN_PATH_MAX:
                continue
            try:
                yield home
            finally:
                gpgconf = shutil.which("gpgconf")
                if gpgconf and home.is_dir():
                    run([gpgconf, "--homedir", str(home), "--kill", "gpg-agent"])
            return
    blind(f"no TMPDIR or /tmp path leaves room for the gpg-agent socket ({SUN_PATH_MAX} bytes)")


def import_secret(gpg: str, home: Path, secret: Path) -> dict[str, str]:
    home.mkdir(mode=0o700, exist_ok=False)
    os.chmod(home, 0o700)
    (home / "gpg.conf").write_text("batch\nno-tty\n", encoding="utf-8")
    os.chmod(home / "gpg.conf", 0o600)
    env = os.environ.copy()
    env["GNUPGHOME"] = str(home)
    env["LC_ALL"] = "C"
    imported = run([gpg, "--homedir", str(home), "--batch", "--import", str(secret)], env)
    if imported.returncode != 0:
        blind("OpenPGP secret key import failed")
    return env


def sign_repomd(gpg: str, home: Path, env: dict[str, str], fingerprint: str, repomd: Path, signature: Path, passphrase: Path | None) -> None:
    command = [
        gpg,
        "--homedir",
        str(home),
        "--batch",
        "--yes",
        "--pinentry-mode",
        "loopback",
        "--local-user",
        f"{fingerprint}!",
        "--digest-algo",
        "SHA256",
        "--armor",
        "--detach-sign",
        "--output",
        str(signature),
    ]
    if passphrase is None:
        command.extend(["--passphrase", ""])
    else:
        command.extend(["--passphrase-file", str(passphrase)])
    command.append(str(repomd))
    signed = run(command, env)
    if signed.returncode != 0 or not signature.is_file() or signature.stat().st_size == 0:
        blind("OpenPGP signing of repomd.xml failed")
    os.chmod(signature, 0o644)
    signer = signing_fingerprint(gpg, home, env, signature, repomd, "the package-repository key")
    if signer != fingerprint:
        refuse(f"repomd.xml.asc was made by {signer}, not the descriptor key {fingerprint}")


def signing_fingerprint(gpg: str, home: Path, env: dict[str, str], signature: Path, repomd: Path, key: str) -> str:
    """Verify signature over repomd and return the primary key fingerprint.

    gpg --status-fd writes one VALIDSIG line per good signature; its last field
    is the primary key fingerprint (GnuPG doc/DETAILS).
    """
    verified = run(
        [gpg, "--homedir", str(home), "--batch", "--status-fd", "1", "--verify", str(signature), str(repomd)],
        env,
    )
    if verified.returncode != 0:
        refuse(f"repomd.xml.asc does not verify with {key}")
    fields = [
        line.split()
        for line in verified.stdout.decode("utf-8", "replace").splitlines()
        if line.startswith("[GNUPG:] VALIDSIG ")
    ]
    if len(fields) != 1 or len(fields[0]) < 3:
        refuse("repomd.xml.asc does not carry exactly one valid signature")
    primary = fields[0][11] if len(fields[0]) > 11 else fields[0][2]
    return primary.upper()


def local_name(tag: str) -> str:
    if tag.startswith("{"):
        return tag.split("}", 1)[1]
    return tag


def child_text(element: ET.Element, name: str) -> str | None:
    for child in list(element):
        if local_name(child.tag) == name and child.text:
            return child.text.strip()
    return None


def child_attr(element: ET.Element, name: str, attr: str) -> str | None:
    for child in list(element):
        if local_name(child.tag) == name:
            value = child.attrib.get(attr)
            if value:
                return value
    return None


def parse_repomd(path: Path) -> ET.Element:
    try:
        raw = path.read_bytes()
    except OSError:
        refuse("repomd.xml is not readable")
    if len(raw) > MAX_REPOMD_BYTES:
        refuse("repomd.xml exceeds the size this checker will parse")
    if b"<!DOCTYPE" in raw.upper() or b"<!ENTITY" in raw.upper():
        refuse("repomd.xml contains a doctype or entity declaration")
    try:
        root = ET.fromstring(raw)
    except ET.ParseError:
        refuse("repomd.xml is not well-formed XML")
    if local_name(root.tag) != "repomd":
        refuse("repository metadata root is not repomd")
    return root


def safe_location(href: str) -> Path:
    if not href or href.startswith("/") or "\\" in href or "://" in href:
        refuse(f"repomd location is not a relative repository path: {href}")
    parts = Path(href).parts
    if not parts or any(part in ("..", "") for part in parts):
        refuse(f"repomd location escapes the repository: {href}")
    return Path(*parts)


def digest_file(path: Path, algorithm: str) -> str:
    builder = HASHES[algorithm]()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            builder.update(chunk)
    return builder.hexdigest()


def child_present(element: ET.Element, name: str) -> bool:
    return any(local_name(child.tag) == name for child in list(element))


def open_digest(target: Path, relative: str, algorithm: str) -> tuple[str, int]:
    """Return the digest and length of the uncompressed bytes of target."""
    with target.open("rb") as handle:
        magic = handle.read(6)
    opener = next((function for prefix, function in OPENERS if magic.startswith(prefix)), None)
    if opener is None:
        if relative.endswith(".xml") and magic.lstrip()[:1] == b"<":
            opener = open
        else:
            refuse(f"open-checksum or open-size is supplied for an unsupported compression: {relative}")
    builder = HASHES[algorithm]()
    size = 0
    try:
        with opener(target, "rb") as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                size += len(chunk)
                if size > MAX_OPEN_BYTES:
                    refuse(f"uncompressed metadata exceeds the size this checker will read: {relative}")
                builder.update(chunk)
    except (OSError, EOFError, lzma.LZMAError):
        refuse(f"compressed metadata does not decompress: {relative}")
    return builder.hexdigest(), size


def inside(root: Path, candidate: Path) -> bool:
    try:
        candidate.resolve(strict=True).relative_to(root.resolve(strict=True))
    except (OSError, ValueError):
        return False
    return True


def symlink_component(repo: Path, relative: Path) -> bool:
    current = repo
    for part in relative.parts:
        current = current / part
        if current.is_symlink():
            return True
    return False


def verify_repository(repo: Path, require_standard: bool) -> list[str]:
    repodata = repo / "repodata"
    if repodata.is_symlink() or not repodata.is_dir():
        refuse("repodata directory is absent or is a symlink")
    repomd_path = repodata / REPOMD_NAME
    if not repomd_path.is_file() or repomd_path.is_symlink():
        refuse("repomd.xml is missing or is a symlink")
    root = parse_repomd(repomd_path)
    named: set[str] = set()
    types: list[str] = []
    entries = [element for element in list(root) if local_name(element.tag) == "data"]
    if not entries:
        refuse("repomd.xml names no metadata")
    for element in entries:
        data_type = element.attrib.get("type", "")
        if not data_type or not re.fullmatch(r"[A-Za-z0-9_-]+", data_type):
            refuse("repomd data type is missing or unsafe")
        types.append(data_type)
        href = child_attr(element, "location", "href")
        if not href:
            refuse(f"repomd data {data_type} has no location")
        relative = safe_location(href)
        named.add(relative.as_posix())
        target = repo.joinpath(relative)
        if symlink_component(repo, relative) or not target.is_file():
            refuse(f"repomd data {data_type} location does not resolve: {relative.as_posix()}")
        if not inside(repo, target):
            refuse(f"repomd data {data_type} location leaves the repository")
        size_text = child_text(element, "size")
        if size_text is None or not size_text.isascii() or not size_text.isdigit():
            refuse(f"repomd data {data_type} size is missing")
        if target.stat().st_size != int(size_text):
            refuse(f"repomd data {data_type} size does not match {relative.as_posix()}")
        algorithm = (child_attr(element, "checksum", "type") or "").lower()
        checksum = child_text(element, "checksum")
        if algorithm not in HASHES or not checksum:
            refuse(f"repomd data {data_type} checksum is missing or uses an unknown type")
        actual = digest_file(target, algorithm)
        if actual.lower() != checksum.lower():
            refuse(f"repomd data {data_type} checksum does not match {relative.as_posix()}")
        has_open_checksum = child_present(element, "open-checksum")
        has_open_size = child_present(element, "open-size")
        if has_open_checksum or has_open_size:
            open_algorithm = (child_attr(element, "open-checksum", "type") or "").lower()
            open_checksum = child_text(element, "open-checksum")
            open_size = child_text(element, "open-size")
            if has_open_checksum and (open_algorithm not in HASHES or not open_checksum):
                refuse(f"repomd data {data_type} open checksum is incomplete")
            if has_open_size and (open_size is None or not open_size.isascii() or not open_size.isdigit()):
                refuse(f"repomd data {data_type} open size is not a decimal byte count")
            opened_digest, opened_size = open_digest(target, relative.as_posix(), open_algorithm or "sha256")
            if has_open_checksum and opened_digest.lower() != open_checksum.lower():
                refuse(f"repomd data {data_type} open checksum does not match")
            if has_open_size and int(open_size) != opened_size:
                refuse(f"repomd data {data_type} open size does not match")
    if require_standard:
        missing = [name for name in STANDARD_TYPES if name not in types]
        if missing:
            refuse("createrepo_c repomd is missing " + ", ".join(missing))
    allowed = {f"repodata/{REPOMD_NAME}", f"repodata/{SIGNATURE_NAME}", *named}
    # os.walk lists a symlinked directory in dirnames and does not descend into
    # it, so every name is checked before the closed-set test.
    for directory, dirnames, filenames in os.walk(repodata):
        for name in (*dirnames, *filenames):
            candidate = Path(directory) / name
            if candidate.is_symlink():
                refuse(f"symlink under repodata is refused: {candidate.relative_to(repo).as_posix()}")
        for filename in filenames:
            candidate = Path(directory) / filename
            relative = candidate.relative_to(repo).as_posix()
            if not candidate.is_file():
                refuse(f"metadata entry is not a regular file: {relative}")
            if relative not in allowed:
                refuse(f"metadata file is not named in repomd: {relative}")
    signature = repodata / SIGNATURE_NAME
    if not signature.is_file() or signature.is_symlink() or signature.stat().st_size == 0:
        refuse("repomd.xml.asc is missing or empty")
    try:
        armor = signature.read_text(encoding="utf-8")
    except (OSError, UnicodeError):
        refuse("repomd.xml.asc is not readable armored text")
    if "BEGIN PGP SIGNATURE" not in armor or "END PGP SIGNATURE" not in armor:
        refuse("repomd.xml.asc is not an armored OpenPGP signature")
    return types


def repomd_location(repo: Path, data_type: str) -> Path:
    root = parse_repomd(repo / "repodata" / REPOMD_NAME)
    for element in list(root):
        if local_name(element.tag) == "data" and element.attrib.get("type") == data_type:
            return repo / safe_location(child_attr(element, "location", "href") or "")
    refuse(f"repomd.xml names no {data_type} metadata")


def read_opened(target: Path) -> bytes:
    with target.open("rb") as handle:
        magic = handle.read(6)
    opener = next((function for prefix, function in OPENERS if magic.startswith(prefix)), open)
    try:
        with opener(target, "rb") as stream:
            data = stream.read(MAX_PRIMARY_BYTES + 1)
    except (OSError, EOFError, lzma.LZMAError):
        refuse("primary metadata does not decompress")
    if len(data) > MAX_PRIMARY_BYTES:
        refuse("primary metadata exceeds the size this checker will parse")
    return data


def primary_packages(repo: Path) -> list[dict[str, str]]:
    """Return one delivery entry per rpm in the signed primary metadata.

    Each file is re-hashed and must equal the checksum the primary records.
    """
    raw = read_opened(repomd_location(repo, "primary"))
    if b"<!DOCTYPE" in raw.upper() or b"<!ENTITY" in raw.upper():
        refuse("primary metadata contains a doctype or entity declaration")
    try:
        root = ET.fromstring(raw)
    except ET.ParseError:
        refuse("primary metadata is not well-formed XML")
    packages: list[dict[str, str]] = []
    for element in list(root):
        if local_name(element.tag) != "package":
            continue
        name = child_text(element, "name") or ""
        arch = child_text(element, "arch") or ""
        version = next((child for child in list(element) if local_name(child.tag) == "version"), None)
        href = child_attr(element, "location", "href") or ""
        checksum_type = (child_attr(element, "checksum", "type") or "").lower()
        checksum = (child_text(element, "checksum") or "").lower()
        if not name or not arch or version is None or not href:
            refuse("primary metadata has a package without name, arch, version, or location")
        epoch = version.attrib.get("epoch", "0") or "0"
        ver = version.attrib.get("ver", "")
        rel = version.attrib.get("rel", "")
        if not ver or not rel:
            refuse(f"primary metadata package {name} has no version or release")
        relative = safe_location(href)
        target = repo / relative
        if relative.suffix != ".rpm" or symlink_component(repo, relative) or not target.is_file():
            refuse(f"primary package {name} does not resolve to a regular rpm: {relative.as_posix()}")
        if checksum_type != "sha256" or not SHA256_HEX.fullmatch(checksum):
            refuse(f"primary package {name} has no sha256 checksum")
        actual = digest_file(target, "sha256")
        if actual != checksum:
            refuse(f"package {relative.as_posix()} does not match its primary checksum")
        packages.append(
            {
                "name": name,
                "nevra": f"{name}-{'' if epoch == '0' else epoch + ':'}{ver}-{rel}.{arch}",
                "arch": arch,
                "file": relative.as_posix(),
                "sha256": actual,
            }
        )
    names = [package["name"] for package in packages]
    if not packages or len(set(names)) != len(names):
        refuse("primary metadata must name each package exactly once")
    return sorted(packages, key=lambda package: package["name"])


def delivery_document(repo: Path, fingerprint: str) -> dict[str, object]:
    repodata = repo / "repodata"
    return {
        "schema": DELIVERY_SCHEMA,
        "key_fingerprint": fingerprint,
        "repomd_sha256": digest_file(repodata / REPOMD_NAME, "sha256"),
        "repomd_asc_sha256": digest_file(repodata / SIGNATURE_NAME, "sha256"),
        "packages": primary_packages(repo),
    }


def load_delivery(repo: Path) -> dict[str, object]:
    path = repo / DELIVERY_NAME
    if path.is_symlink() or not path.is_file():
        refuse("delivery.json is missing, not regular, or is a symlink")
    try:
        document = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError):
        refuse("delivery.json is not readable JSON")
    if not isinstance(document, dict) or set(document) != DELIVERY_FIELDS:
        refuse("delivery.json fields are not exact")
    if document["schema"] != DELIVERY_SCHEMA:
        refuse("delivery.json has the wrong schema")
    if not isinstance(document["key_fingerprint"], str) or not FINGERPRINT.fullmatch(document["key_fingerprint"]):
        refuse("delivery.json key_fingerprint must be uppercase 40-hex")
    for field in ("repomd_sha256", "repomd_asc_sha256"):
        if not isinstance(document[field], str) or not SHA256_HEX.fullmatch(document[field]):
            refuse(f"delivery.json {field} must be lowercase 64-hex")
    packages = document["packages"]
    if not isinstance(packages, list) or not packages:
        refuse("delivery.json packages must be a non-empty list")
    for package in packages:
        if not isinstance(package, dict) or set(package) != PACKAGE_FIELDS:
            refuse("delivery.json package fields are not exact")
        if not all(isinstance(value, str) and value for value in package.values()):
            refuse("delivery.json package values must be non-empty strings")
    return document


def verify_delivery(
    repo: Path,
    fingerprint: str | None,
    public_key: Path | None,
    expect: list[str],
) -> dict[str, object]:
    """Refuse a delivery.json that does not describe the tree beside it."""
    verify_repository(repo, require_standard=True)
    document = load_delivery(repo)
    if fingerprint is not None and document["key_fingerprint"] != fingerprint:
        refuse(f"delivery.json key_fingerprint is not {fingerprint}")
    repodata = repo / "repodata"
    if document["repomd_sha256"] != digest_file(repodata / REPOMD_NAME, "sha256"):
        refuse("delivery.json repomd_sha256 does not match repomd.xml")
    if document["repomd_asc_sha256"] != digest_file(repodata / SIGNATURE_NAME, "sha256"):
        refuse("delivery.json repomd_asc_sha256 does not match repomd.xml.asc")
    actual = primary_packages(repo)
    listed = sorted(document["packages"], key=lambda package: package["name"])
    if [package["name"] for package in listed] != [package["name"] for package in actual]:
        refuse("delivery.json package set does not match the signed primary metadata")
    for mine, theirs in zip(listed, actual):
        if mine != theirs:
            refuse(f"delivery.json entry for {mine['name']} does not match the tree (digest, file, or NEVRA)")
    files = {package["file"] for package in actual}
    for directory, dirnames, filenames in os.walk(repo):
        dirnames[:] = [name for name in dirnames if not (directory == str(repo) and name == "repodata")]
        for filename in filenames:
            relative = (Path(directory) / filename).relative_to(repo).as_posix()
            if filename.endswith(".rpm") and relative not in files:
                refuse(f"rpm in the tree is not named in delivery.json: {relative}")
    if expect:
        if len(set(expect)) != len(expect) or sorted(expect) != [package["name"] for package in actual]:
            refuse("package set is not exactly " + ", ".join(sorted(expect)))
    if public_key is not None:
        signer = verify_signature(repo, public_key)
        if signer != document["key_fingerprint"]:
            refuse(f"repomd.xml.asc was made by {signer}, not delivery.json key_fingerprint")
    return document


def write_delivery(repo: Path, fingerprint: str, expect: list[str]) -> None:
    destination = repo / DELIVERY_NAME
    destination.write_text(json.dumps(delivery_document(repo, fingerprint), indent=2) + "\n", encoding="utf-8")
    os.chmod(destination, 0o644)
    try:
        verify_delivery(repo, fingerprint, None, expect)
    except SystemExit:
        destination.unlink()
        raise


def write_checksums(repo: Path) -> None:
    lines: list[str] = []
    for directory, dirnames, filenames in os.walk(repo):
        dirnames.sort()
        for filename in sorted(filenames):
            path = Path(directory) / filename
            if path.name == "checksums.txt" or path.is_symlink() or not path.is_file():
                continue
            relative = path.relative_to(repo).as_posix()
            lines.append(f"{digest_file(path, 'sha256')}  {relative}")
    destination = repo / "checksums.txt"
    destination.write_text("".join(f"{line}\n" for line in lines), encoding="utf-8")
    os.chmod(destination, 0o644)


def createrepo_command(createrepo: str, repo: Path) -> list[str]:
    """Return the createrepo_c argv. createrepo is the resolved path from require_tool."""
    arguments = ["--checksum", "sha256", "--general-compress-type", "gz"]
    # The program is compared by its file name and the arguments on their own,
    # so a resolved /usr/bin/createrepo_c matches the documented bare name.
    if Path(createrepo).name != DOCUMENTED_CREATEREPO_ARGV[0]:
        blind(f"repository program is not {DOCUMENTED_CREATEREPO_ARGV[0]}: {createrepo}")
    if tuple(arguments) != DOCUMENTED_CREATEREPO_ARGV[1:]:
        blind("createrepo_c argv drifted from the documented command")
    return [createrepo, *arguments, str(repo)]


def render(repo: Path, expect: list[str] | None = None) -> None:
    createrepo = require_tool("createrepo_c")
    gpg = require_tool("gpg")
    fingerprint = load_fingerprint()
    secret = secret_file("OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE")
    passphrase = optional_secret("OLIVARES_PACKAGE_REPO_OPENPGP_PASSPHRASE_FILE")
    rpms = [path for path in repo.iterdir() if path.is_file() and not path.is_symlink() and path.suffix == ".rpm"]
    if not rpms:
        refuse("repository directory contains no rpm")
    repodata = repo / "repodata"
    if repodata.exists() or repodata.is_symlink():
        if repodata.is_symlink() or not repodata.is_dir():
            refuse("repodata exists and is not a directory")
        for child in repodata.iterdir():
            if child.is_symlink() or not child.is_file():
                refuse(f"refusing to remove unexpected repodata entry: {child.name}")
            child.unlink()
        repodata.rmdir()
    for stale in (repo / "checksums.txt", repo / DELIVERY_NAME):
        if stale.exists() or stale.is_symlink():
            if stale.is_symlink() or not stale.is_file():
                refuse(f"{stale.name} exists and is not a regular file")
            stale.unlink()
    created = run(createrepo_command(createrepo, repo))
    if created.returncode != 0:
        detail = created.stderr.decode("utf-8", "replace").strip().splitlines()
        tail = detail[-1] if detail else "no stderr"
        refuse(f"createrepo_c failed: {tail}")
    repomd = repodata / REPOMD_NAME
    if not repomd.is_file():
        refuse("createrepo_c did not write repomd.xml")
    with gnupg_home() as home:
        env = import_secret(gpg, home, secret)
        sign_repomd(gpg, home, env, fingerprint, repomd, repodata / SIGNATURE_NAME, passphrase)
    verify_repository(repo, require_standard=True)
    write_delivery(repo, fingerprint, expect or [])
    write_checksums(repo)
    print(f"rpm-repodata: signed rpm-md repository and {DELIVERY_NAME} at {repo}")


def verify_signature(repo: Path, public_key: Path) -> str:
    gpg = require_tool("gpg")
    signature = repo / "repodata" / SIGNATURE_NAME
    repomd = repo / "repodata" / REPOMD_NAME
    with gnupg_home() as home:
        home.mkdir(mode=0o700)
        os.chmod(home, 0o700)
        env = os.environ.copy()
        env["GNUPGHOME"] = str(home)
        env["LC_ALL"] = "C"
        imported = run([gpg, "--homedir", str(home), "--batch", "--import", str(public_key)], env)
        if imported.returncode != 0:
            blind("OpenPGP public key import failed")
        return signing_fingerprint(gpg, home, env, signature, repomd, "the supplied public key")


def verify_only(repo: Path, public_key: Path | None) -> None:
    verify_repository(repo, require_standard=False)
    if public_key is not None:
        verify_signature(repo, public_key)
    print(f"rpm-repodata: repository metadata resolves at {repo}")


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description="Render or verify a signed rpm-md repository.")
    sub = parser.add_subparsers(dest="command", required=True)
    render_parser = sub.add_parser("render", help="Run createrepo_c, sign repomd.xml, and verify the result.")
    render_parser.add_argument("--repo", required=True)
    render_parser.add_argument("--expect-package", action="append", default=[], help="Exact package name; repeat.")
    verify_parser = sub.add_parser("verify", help="Refuse a repo whose repomd links, sizes, or hashes do not resolve.")
    verify_parser.add_argument("--repo", required=True)
    verify_parser.add_argument("--public-key", default="")
    delivery_parser = sub.add_parser("verify-delivery", help="Refuse a delivery.json that does not describe its tree.")
    delivery_parser.add_argument("--repo", required=True)
    delivery_parser.add_argument("--public-key", default="")
    delivery_parser.add_argument("--fingerprint", default="", help="Expected uppercase 40-hex key fingerprint.")
    delivery_parser.add_argument("--expect-package", action="append", default=[], help="Exact package name; repeat.")
    args = parser.parse_args(argv)
    repo = absolute_directory(args.repo)
    if args.command == "render":
        render(repo, args.expect_package)
        return 0
    public_key = None
    if args.public_key:
        public_key = Path(args.public_key)
        if not public_key.is_absolute() or not public_key.is_file() or public_key.is_symlink():
            blind("--public-key is not an absolute regular file")
    if args.command == "verify":
        verify_only(repo, public_key)
        return 0
    if args.fingerprint and not FINGERPRINT.fullmatch(args.fingerprint):
        blind("--fingerprint must be uppercase 40-hex")
    verify_delivery(repo, args.fingerprint or None, public_key, args.expect_package)
    print(f"rpm-repodata: {DELIVERY_NAME} describes the signed tree at {repo}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main(sys.argv[1:]))
    except BrokenPipeError:
        raise SystemExit(1)
