#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""delivery_check.py --package-dir DIR --expected-fingerprint FPR — the product repository D's S3 delivers, checked before
KIWI reads it.

DIR is S3's signed rpm-md tree for the image: the RPMs, repodata/ (createrepo_c metadata with gzip compression, and
repodata/repomd.xml.asc), the package-repository public key as S3 publishes it (olivares-package-repository.asc), and
delivery.json:

  {"schema": "olivares.ai/rpm-delivery/v1", "key_fingerprint": "<40 hex>", "repomd_sha256": "<64 hex>",
   "repomd_asc_sha256": "<64 hex>", "packages": [{"name", "nevra", "arch", "file", "sha256"}, ...]}

That is D's shape (2026-09-27), no more and no less, at the top and in each package. The check refuses (exit 1):
  * a manifest with any other field set, or a package whose arch is not x86_64 or noarch (named), or not the arch of
    its NEVRA and of the signed metadata;
  * a key whose OpenPGP fingerprint is not FPR, or not the one delivery.json names;
  * a repomd.xml or repomd.xml.asc whose sha256 differs from delivery.json, no repomd.xml.asc, or a signature that gpg
    does not verify as made by FPR's key (its VALIDSIG status line names the primary key);
  * a package set other than exactly olivares and olivares-appliance-base, in delivery.json or in the signed metadata;
  * a package file whose sha256 differs from delivery.json or from the metadata's checksum;
  * an RPM whose signature header carries no OpenPGP signature.
It cannot check (exit 2) a directory without a readable delivery.json, key or metadata, or a host without gpg. On success
it prints one JSON record of what it accepted, for the build log and the manifest.

The fingerprint is computed here from the key's public-key packet (RFC 4880 §12.2 for v4; RFC 9580 §5.5.4 for v6), so
the pin never depends on a keyring's own listing. Also imported: fingerprint(armored_bytes)."""
import argparse
import base64
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import xml.etree.ElementTree as ET

SCHEMA = "olivares.ai/rpm-delivery/v1"
# The appliance's own repository: the product, the appliance layer and its SELinux policy module (RECIPE-INTERFACE 1).
PRODUCT = ("olivares", "olivares-appliance-base", "olivares-selinux")
KEY_FILE = "olivares-package-repository.asc"
# D's delivery.json shape: exactly these fields at the top and in each package, and the architectures of this image.
FIELDS = ("schema", "key_fingerprint", "repomd_sha256", "repomd_asc_sha256", "packages")
PACKAGE_FIELDS = ("name", "nevra", "arch", "file", "sha256")
ARCHES = ("x86_64", "noarch")
# Signature header tags that hold an OpenPGP signature: DSAHEADER 267, RSAHEADER 268, PGP 1002, GPG 1005 (rpm 4) and
# OPENPGP 278 (rpm 6).
SIGNATURE_TAGS = {267, 268, 1002, 1005, 278}
REPO = "{http://linux.duke.edu/metadata/repo}"
COMMON = "{http://linux.duke.edu/metadata/common}"


class Refused(Exception):
    pass


class Unable(Exception):
    pass


def unarmor(data):
    text = data.decode("ascii", "replace")
    match = re.search(r"-----BEGIN PGP PUBLIC KEY BLOCK-----\r?\n(.*?)-----END PGP PUBLIC KEY BLOCK-----", text, re.S)
    if not match:
        return data
    body = match.group(1).split("\n\n", 1)[-1] if "\n\n" in match.group(1) else match.group(1)
    lines = [l.strip() for l in body.splitlines() if l.strip() and not l.strip().startswith("=") and ":" not in l]
    return base64.b64decode("".join(lines))


def packets(data):
    pos = 0
    while pos < len(data):
        head = data[pos]
        if not head & 0x80:
            raise ValueError("not an OpenPGP packet at %d" % pos)
        if head & 0x40:
            tag, first = head & 0x3F, data[pos + 1]
            if first < 192:
                length, pos = first, pos + 2
            elif first < 224:
                length, pos = ((first - 192) << 8) + data[pos + 2] + 192, pos + 3
            elif first == 255:
                length, pos = int.from_bytes(data[pos + 2:pos + 6], "big"), pos + 6
            else:
                raise ValueError("partial body length in a key packet")
        else:
            tag, kind = (head >> 2) & 0x0F, head & 0x03
            size = {0: 1, 1: 2, 2: 4}.get(kind)
            if size is None:
                raise ValueError("indeterminate length in a key packet")
            length, pos = int.from_bytes(data[pos + 1:pos + 1 + size], "big"), pos + 1 + size
        yield tag, data[pos:pos + length]
        pos += length


def fingerprint(armored):
    """The OpenPGP fingerprint, in upper-case hex, of the first public-key packet (tag 6) of ARMORED."""
    for tag, body in packets(unarmor(armored)):
        if tag == 6:
            version = body[0]
            if version == 4:
                return hashlib.sha1(b"\x99" + len(body).to_bytes(2, "big") + body).hexdigest().upper()
            if version in (5, 6):
                prefix = b"\x9a" if version == 5 else b"\x9b"
                return hashlib.sha256(prefix + len(body).to_bytes(4, "big") + body).hexdigest().upper()
            raise ValueError("public key version %d" % version)
    raise ValueError("no public-key packet")


def rpm_signature_tags(data):
    if data[:4] != b"\xed\xab\xee\xdb":
        raise Refused("not an RPM file")
    offset = 96
    if data[offset:offset + 3] != b"\x8e\xad\xe8":
        raise Refused("no signature header")
    count = int.from_bytes(data[offset + 8:offset + 12], "big")
    return {int.from_bytes(data[offset + 16 + 16 * i:offset + 20 + 16 * i], "big") for i in range(count)}


def sha256(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def verify_signature(directory, key, expected):
    gpg = shutil.which("gpg")
    if gpg is None:
        raise Unable("gpg is absent: the repomd.xml signature cannot be verified here")
    home = tempfile.mkdtemp(prefix="g.")
    try:
        os.chmod(home, 0o700)
        base = [gpg, "--homedir", home, "--batch", "--no-tty"]
        imported = subprocess.run(base + ["--import", str(key)], capture_output=True, timeout=60)
        if imported.returncode != 0:
            raise Refused("the key file does not import: %s" % imported.stderr.decode(errors="replace")[-300:])
        verified = subprocess.run(base + ["--status-fd", "1", "--verify", str(directory / "repodata/repomd.xml.asc"),
                                          str(directory / "repodata/repomd.xml")], capture_output=True, timeout=60)
        valid = [line.split() for line in verified.stdout.decode(errors="replace").splitlines()
                 if line.startswith("[GNUPG:] VALIDSIG ")]
        if verified.returncode != 0 or len(valid) != 1 or valid[0][-1].upper() != expected:
            raise Refused("repodata/repomd.xml.asc is not a valid signature of repomd.xml by %s" % expected)
    finally:
        subprocess.run(["gpgconf", "--homedir", home, "--kill", "all"], capture_output=True, timeout=30)
        shutil.rmtree(home, ignore_errors=True)


def check(directory, expected):
    directory = Path(directory)
    try:
        manifest = json.loads((directory / "delivery.json").read_text())
    except (OSError, ValueError) as issue:
        raise Unable("no readable delivery.json in %s: %s" % (directory, issue))
    if not isinstance(manifest, dict) or set(manifest) != set(FIELDS) or manifest["schema"] != SCHEMA:
        raise Refused("delivery.json is not exactly %s with the fields %s; it has %s"
                      % (SCHEMA, ", ".join(FIELDS), sorted(manifest) if isinstance(manifest, dict) else type(manifest)))
    rows = manifest["packages"]
    if not isinstance(rows, list) or not all(isinstance(r, dict) and set(r) == set(PACKAGE_FIELDS) for r in rows):
        raise Refused("each package of delivery.json has exactly the fields %s; it names %s"
                      % (", ".join(PACKAGE_FIELDS), [sorted(r) if isinstance(r, dict) else r for r in rows]
                         if isinstance(rows, list) else rows))
    for row in rows:
        if row["arch"] not in ARCHES:
            raise Refused("%s: delivery.json names arch %r; this image takes %s only" % (row["name"], row["arch"],
                                                                                           " or ".join(ARCHES)))
        if not str(row["nevra"]).endswith("." + row["arch"]):
            raise Refused("%s: delivery.json names arch %s for the NEVRA %s" % (row["name"], row["arch"], row["nevra"]))
    try:
        key_bytes = (directory / KEY_FILE).read_bytes()
    except OSError as issue:
        raise Unable("no key file %s: %s" % (KEY_FILE, issue))
    try:
        found = fingerprint(key_bytes)
    except (ValueError, IndexError) as issue:
        raise Refused("the key file is not an OpenPGP public key: %s" % issue)
    if found != expected or str(manifest["key_fingerprint"]).upper() != expected:
        raise Refused("the key fingerprint is %s and delivery.json names %s; this build expects %s"
                      % (found, manifest["key_fingerprint"], expected))
    repomd = directory / "repodata/repomd.xml"
    if not repomd.is_file():
        raise Unable("no repodata/repomd.xml")
    if sha256(repomd) != manifest["repomd_sha256"]:
        raise Refused("repodata/repomd.xml has sha256 %s; delivery.json names %s" % (sha256(repomd), manifest["repomd_sha256"]))
    signature = directory / "repodata/repomd.xml.asc"
    if not signature.is_file():
        raise Refused("repodata/repomd.xml.asc is missing: the metadata is not signed")
    if sha256(signature) != manifest["repomd_asc_sha256"]:
        raise Refused("repodata/repomd.xml.asc has sha256 %s; delivery.json names repomd_asc_sha256 %s"
                      % (sha256(signature), manifest["repomd_asc_sha256"]))
    verify_signature(directory, directory / KEY_FILE, expected)
    if sorted(r["name"] for r in rows) != sorted(PRODUCT):
        raise Refused("the delivery must hold exactly %s; delivery.json names %s"
                      % (", ".join(PRODUCT), [r["name"] for r in rows]))
    try:
        data = [d for d in ET.parse(repomd).getroot().iter(REPO + "data") if d.get("type") == "primary"][0]
        href = data.find(REPO + "location").get("href")
        named = data.find(REPO + "checksum")
        primary_path = directory / href
        if named is not None and named.text.strip() != sha256(primary_path):
            raise Refused("the primary metadata's sha256 differs from repomd.xml")
        primary = ET.fromstring(gzip.decompress(primary_path.read_bytes()))
    except (OSError, ET.ParseError, IndexError, AttributeError) as issue:
        raise Unable("the signed metadata cannot be read: %s" % issue)
    listed = {p.findtext(COMMON + "name"): (p.find(COMMON + "location").get("href"), p.findtext(COMMON + "checksum"),
                                            p.findtext(COMMON + "arch"))
              for p in primary.iter(COMMON + "package")}
    if sorted(listed) != sorted(PRODUCT):
        raise Refused("the signed metadata must list exactly %s; it lists %s" % (", ".join(PRODUCT), sorted(listed)))
    accepted = []
    for row in rows:
        path = directory / row["file"]
        if row["file"] != row["nevra"] + ".rpm" or "/" in row["file"] or not path.is_file():
            raise Refused("%s: the file %r is not the package's own NEVRA file" % (row["name"], row["file"]))
        digest = sha256(path)
        if digest != row["sha256"] or listed[row["name"]][:2] != (row["file"], digest):
            raise Refused("%s: sha256 %s differs from delivery.json or the signed metadata" % (row["file"], digest))
        if listed[row["name"]][2] != row["arch"]:
            raise Refused("%s: delivery.json names arch %s; the signed metadata names %s" % (row["name"], row["arch"],
                                                                                           listed[row["name"]][2]))
        if not rpm_signature_tags(path.read_bytes()) & SIGNATURE_TAGS:
            raise Refused("%s carries no OpenPGP signature in its signature header" % row["file"])
        accepted.append({"name": row["name"], "nevra": row["nevra"], "arch": row["arch"], "file": row["file"],
                         "sha256": digest})
    return {"schema": SCHEMA, "key_fingerprint": expected, "repomd_sha256": manifest["repomd_sha256"],
            "repomd_asc_sha256": manifest["repomd_asc_sha256"], "packages": sorted(accepted, key=lambda r: r["name"])}


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--package-dir", required=True)
    parser.add_argument("--expected-fingerprint", required=True)
    try:
        options = parser.parse_args(argv)
    except SystemExit:
        return 2
    expected = options.expected_fingerprint.upper()
    if not re.fullmatch(r"[0-9A-F]{40}", expected):
        print("delivery_check.py: the expected fingerprint is not 40 hex digits", file=sys.stderr)
        return 2
    try:
        record = check(options.package_dir, expected)
    except Refused as issue:
        print("delivery_check.py: refused: %s" % issue, file=sys.stderr)
        return 1
    except Unable as issue:
        print("delivery_check.py: cannot check: %s" % issue, file=sys.stderr)
        return 2
    print(json.dumps(record, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
