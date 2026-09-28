#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""write_delivery.py --package-dir DIR --key-file KEY — delivery.json for a signed rpm-md tree, the manifest the Fedora
build checks (delivery_check.py; schema olivares.ai/rpm-delivery/v1).

DIR holds the RPMs and the repodata D's S3 scripts wrote and signed (scripts/rpm-payload-sign.py, then
scripts/render-rpm-repodata.py render). KEY is the public key that signed them. The manifest names the key's OpenPGP
fingerprint (computed from the key itself), the sha256 of repodata/repomd.xml and of its signature repomd.xml.asc, and
for each RPM its name, NEVRA and arch, read from its own header, its file name and its sha256: D's shape. The key is
copied beside the tree as olivares-package-repository.asc, the name S3 publishes it under. Nothing is signed or
re-indexed here; the build refuses whatever this does not match.
exit 0 written; exit 1 a file is not an RPM, or the tree has no repomd.xml or repomd.xml.asc; exit 2 usage or an existing
delivery.json."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))
from delivery_check import KEY_FILE, SCHEMA, fingerprint  # noqa: E402

# Main header tags (rpmtag.h): NAME 1000, VERSION 1001, RELEASE 1002, EPOCH 1003 (int32), ARCH 1022.
NAME, VERSION, RELEASE, EPOCH, ARCH = 1000, 1001, 1002, 1003, 1022


def header_fields(data):
    if data[:4] != b"\xed\xab\xee\xdb":
        raise ValueError("not an RPM file")
    offset = 96
    for which in ("signature", "main"):
        if data[offset:offset + 3] != b"\x8e\xad\xe8":
            raise ValueError("no %s header" % which)
        count = int.from_bytes(data[offset + 8:offset + 12], "big")
        size = int.from_bytes(data[offset + 12:offset + 16], "big")
        store = offset + 16 + 16 * count
        if which == "signature":
            offset = store + size + (-(store + size) % 8)
            continue
        fields = {}
        for i in range(count):
            tag, kind, where, _ = (int.from_bytes(data[offset + 16 + 16 * i + 4 * j:offset + 20 + 16 * i + 4 * j], "big")
                                   for j in range(4))
            if kind == 6:
                end = data.index(b"\0", store + where)
                fields[tag] = data[store + where:end].decode()
            elif kind == 4:
                fields[tag] = int.from_bytes(data[store + where:store + where + 4], "big")
        return fields
    raise ValueError("unreachable")


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--package-dir", required=True)
    parser.add_argument("--key-file", required=True)
    try:
        options = parser.parse_args(argv)
    except SystemExit:
        return 2
    tree = Path(options.package_dir)
    if (tree / "delivery.json").exists():
        print("write_delivery.py: %s already has a delivery.json" % tree, file=sys.stderr)
        return 2
    try:
        repomd = hashlib.sha256((tree / "repodata/repomd.xml").read_bytes()).hexdigest()
        repomd_asc = hashlib.sha256((tree / "repodata/repomd.xml.asc").read_bytes()).hexdigest()
        rows = []
        for rpm in sorted(tree.glob("*.rpm")):
            data = rpm.read_bytes()
            fields = header_fields(data)
            epoch = "%d:" % fields[EPOCH] if fields.get(EPOCH) else ""
            rows.append({"name": fields[NAME], "nevra": "%s-%s%s-%s.%s" % (fields[NAME], epoch, fields[VERSION],
                                                                             fields[RELEASE], fields[ARCH]),
                         "arch": fields[ARCH], "file": rpm.name, "sha256": hashlib.sha256(data).hexdigest()})
        key_bytes = Path(options.key_file).read_bytes()
        manifest = {"schema": SCHEMA, "key_fingerprint": fingerprint(key_bytes), "repomd_sha256": repomd,
                    "repomd_asc_sha256": repomd_asc, "packages": rows}
    except (OSError, ValueError, KeyError) as issue:
        print("write_delivery.py: %s" % issue, file=sys.stderr)
        return 1
    shutil.copyfile(options.key_file, tree / KEY_FILE)
    fd = os.open(tree / "delivery.json", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o644)
    with os.fdopen(fd, "w") as out:
        json.dump(manifest, out, indent=2, sort_keys=True)
        out.write("\n")
    print("write_delivery.py: %s, key %s, %d packages" % (tree / "delivery.json", manifest["key_fingerprint"], len(rows)))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
