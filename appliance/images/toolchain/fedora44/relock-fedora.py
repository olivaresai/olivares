#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""relock-fedora.py --metadata DIR --output NEW --utc UTC [--fetch] [--add SECTION=NAME ...] — a new Fedora 44 lock after
the updates tree moved, or with new roots.

The updates tree keeps only current builds, so the pinned repomd.xml stops being served after an updates push and the
builder refuses to install (bootstrap.sh). This writes, into the new directory NEW, the successor of this directory's
input-lock.json and bootstrap.sh and of the description's pins (appliance/images/kiwi/fedora-repositories.json), to be
reviewed and committed in their place. It never writes them in place.

DIR holds releases/ and updates/, and the source trees releases-source/ and updates-source/, each with repomd.xml and the
primary file it names (with --fetch, all are first read by GET from the pinned base URLs). For each tree:
  * the primary's sha256 and its decompressed sha256 must be the repomd.xml's checksum and open-checksum;
  * the new pin is the repomd.xml's sha256.
For each locked RPM (the builder's and the image's), the candidate is the highest EVR (rpmvercmp) of its name among the
x86_64 and noarch packages of both trees, and it must move forward: a locked name that is gone, an older EVR, or the same
NEVRA with other bytes is refused. The new entry takes its NEVRA, sha256, location and tree from the metadata, and names
its retained source: Koji's copy signed with the Fedora 44 key,
  https://kojipkgs.fedoraproject.org/packages/<source name>/<version>/<release>/data/signed/<key id>/<arch>/<file>,
which for dnf5-5.4.5.0-1.fc44 is byte-equal to the updates tree's (measured 2026-09-27; the lock's sha256 verifies it).
Each entry also names its source RPM (srpm): the NEVRA, sha256 and location of its SOURCERPM in the source tree of the
same release, and Koji's signed copy of it (.../data/signed/<key id>/src/<file>), which for less-704-4.fc44.src.rpm is
byte-equal to the updates source tree's (measured 2026-09-27). A source RPM that is in neither source tree, or whose
NEVRA the lock already pins with other bytes, is refused. The key pin never changes. The lock is written in the canonical form the preflight's collector compares with. UTC is
when the metadata was read: the time of the --fetch, or of the GET that read DIR's files.
--add SECTION=NAME (SECTION system_packages, the builder's, or image_packages) pins a new root from the same metadata,
its newest candidate, like every other entry; a name no tree serves is refused, and a name the lock pins already or an
unknown section is a usage error.
exit 0 written; exit 1 refused (checksums, a loosened pin); exit 2 usage, an existing NEW, or unreadable inputs."""
import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import sys
import urllib.request
import xml.etree.ElementTree as ET

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[3]
PINS = ROOT / "appliance/images/kiwi/fedora-repositories.json"
TREES = {"fedora-44-releases": "releases", "fedora-44-updates": "updates"}
SOURCE_TREES = {"fedora-44-releases-source": "releases-source", "fedora-44-updates-source": "updates-source"}
# The source trees of the same two releases, for a lock that does not pin them yet.
SOURCE_BASEURLS = {
    "fedora-44-releases-source": "https://dl.fedoraproject.org/pub/fedora/linux/releases/44/Everything/source/tree/",
    "fedora-44-updates-source": "https://dl.fedoraproject.org/pub/fedora/linux/updates/44/Everything/source/tree/",
}
REPO = "{http://linux.duke.edu/metadata/repo}"
COMMON = "{http://linux.duke.edu/metadata/common}"
RPM = "{http://linux.duke.edu/metadata/rpm}"
KOJI = "https://kojipkgs.fedoraproject.org/packages"


class Refused(Exception):
    pass


class Unable(Exception):
    pass


def vercmp(a, b):
    """rpm's rpmvercmp: segments of digits or letters, digits newer than letters, ~ sorts first, ^ after the end."""
    if a == b:
        return 0
    while a or b:
        a, b = re.sub(r"^[^A-Za-z0-9~^]+", "", a), re.sub(r"^[^A-Za-z0-9~^]+", "", b)
        if a.startswith("~") or b.startswith("~"):
            if not a.startswith("~"):
                return 1
            if not b.startswith("~"):
                return -1
            a, b = a[1:], b[1:]
            continue
        if a.startswith("^") or b.startswith("^"):
            if not a:
                return -1
            if not b:
                return 1
            if not a.startswith("^"):
                return 1
            if not b.startswith("^"):
                return -1
            a, b = a[1:], b[1:]
            continue
        if not a or not b:
            break
        digit = a[0].isdigit()
        pattern = r"^\d+" if digit else r"^[A-Za-z]+"
        sa, sb = re.match(pattern, a), re.match(pattern, b)
        if not sb:
            return 1 if digit else -1
        sa, sb = sa.group(0), sb.group(0)
        a, b = a[len(sa):], b[len(sb):]
        if digit:
            sa, sb = sa.lstrip("0"), sb.lstrip("0")
            if len(sa) != len(sb):
                return 1 if len(sa) > len(sb) else -1
        if sa != sb:
            return 1 if sa > sb else -1
    if not a and not b:
        return 0
    return 1 if a else -1


def evr_cmp(x, y):
    for key in ("epoch", "version", "release"):
        c = vercmp(str(x[key] or "0"), str(y[key] or "0"))
        if c:
            return c
    return 0


def decompress(name, data):
    if name.endswith(".gz"):
        return gzip.decompress(data)
    if name.endswith(".zst"):
        try:
            from compression import zstd  # Python 3.14, the Fedora 44 builder's
            return zstd.decompress(data)
        except ImportError:
            return zstd_ctypes(name, data)
    raise Unable("%s: unknown compression" % name)


def zstd_ctypes(name, data):
    """zstd frames through the host's libzstd.so.1 (the streaming API of zstd.h), where Python has no zstd module."""
    import ctypes

    class Buffer(ctypes.Structure):
        _fields_ = [("src", ctypes.c_void_p), ("size", ctypes.c_size_t), ("pos", ctypes.c_size_t)]
    try:
        lib = ctypes.CDLL("libzstd.so.1")
    except OSError:
        raise Unable("%s is zstd, and neither a zstd module nor libzstd.so.1 is here" % name)
    lib.ZSTD_createDStream.restype = ctypes.c_void_p
    lib.ZSTD_freeDStream.argtypes = [ctypes.c_void_p]
    lib.ZSTD_initDStream.argtypes = [ctypes.c_void_p]
    lib.ZSTD_decompressStream.argtypes = [ctypes.c_void_p, ctypes.POINTER(Buffer), ctypes.POINTER(Buffer)]
    lib.ZSTD_decompressStream.restype = ctypes.c_size_t
    lib.ZSTD_isError.argtypes = [ctypes.c_size_t]
    lib.ZSTD_DStreamOutSize.restype = ctypes.c_size_t
    stream = lib.ZSTD_createDStream()
    try:
        lib.ZSTD_initDStream(stream)
        source = ctypes.create_string_buffer(data, len(data))
        in_buf = Buffer(ctypes.cast(source, ctypes.c_void_p), len(data), 0)
        chunk = lib.ZSTD_DStreamOutSize()
        target = ctypes.create_string_buffer(chunk)
        out, last = [], 0
        while True:
            out_buf = Buffer(ctypes.cast(target, ctypes.c_void_p), chunk, 0)
            last = lib.ZSTD_decompressStream(stream, ctypes.byref(out_buf), ctypes.byref(in_buf))
            if lib.ZSTD_isError(last):
                raise Refused("%s does not decompress as zstd" % name)
            out.append(target.raw[:out_buf.pos])
            if in_buf.pos == in_buf.size and out_buf.pos < chunk:
                break
        if last != 0:
            raise Refused("%s is truncated" % name)
        return b"".join(out)
    finally:
        lib.ZSTD_freeDStream(stream)


def fetch(pins, directory, trees=TREES):
    for alias, short in trees.items():
        base = pins[alias]["baseurl"].rstrip("/") + "/"
        (directory / short).mkdir(parents=True)
        with urllib.request.urlopen(base + "repodata/repomd.xml", timeout=60) as response:
            repomd = response.read()
        (directory / short / "repomd.xml").write_bytes(repomd)
        href = primary_href(repomd)
        with urllib.request.urlopen(base + href, timeout=600) as response:
            (directory / short / Path(href).name).write_bytes(response.read())


def primary_href(repomd):
    data = [d for d in ET.fromstring(repomd).iter(REPO + "data") if d.get("type") == "primary"][0]
    return data.find(REPO + "location").get("href")


def read_tree(directory, short, arches=("x86_64", "noarch")):
    repomd = (directory / short / "repomd.xml").read_bytes()
    data = [d for d in ET.fromstring(repomd).iter(REPO + "data") if d.get("type") == "primary"][0]
    href = data.find(REPO + "location").get("href")
    raw = (directory / short / Path(href).name).read_bytes()
    if hashlib.sha256(raw).hexdigest() != data.find(REPO + "checksum").text.strip():
        raise Refused("%s: the primary's sha256 is not the repomd.xml checksum" % short)
    opened = decompress(href, raw)
    if hashlib.sha256(opened).hexdigest() != data.find(REPO + "open-checksum").text.strip():
        raise Refused("%s: the decompressed primary is not the repomd.xml open-checksum" % short)
    packages = {}
    for element in ET.fromstring(opened).iter(COMMON + "package"):
        version = element.find(COMMON + "version")
        arch = element.findtext(COMMON + "arch")
        if arch not in arches:
            continue
        packages.setdefault(element.findtext(COMMON + "name"), []).append({
            "name": element.findtext(COMMON + "name"),
            "epoch": version.get("epoch"), "version": version.get("ver"), "release": version.get("rel"), "arch": arch,
            "sha256": element.findtext(COMMON + "checksum"), "href": element.find(COMMON + "location").get("href"),
            "sourcerpm": element.findtext("%sformat/%ssourcerpm" % (COMMON, RPM))})
    return repomd, hashlib.sha256(raw).hexdigest(), packages


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--metadata", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--utc", required=True)
    parser.add_argument("--fetch", action="store_true")
    parser.add_argument("--add", action="append", default=[], metavar="SECTION=NAME")
    try:
        options = parser.parse_args(argv)
    except SystemExit:
        return 2
    if not re.fullmatch(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ", options.utc):
        print("relock-fedora.py: --utc is a UTC time like 2026-09-28T00:00:00Z", file=sys.stderr)
        return 2
    output, metadata = Path(options.output), Path(options.metadata)
    if output.exists():
        print("relock-fedora.py: %s exists; a relock never overwrites" % output, file=sys.stderr)
        return 2
    try:
        lock = json.loads((HERE / "input-lock.json").read_text())
        sections = {"system_packages": lock["distribution"]["system_packages"], "image_packages": lock["image_packages"]}
        for request in options.add:
            section, _, name = request.partition("=")
            if section not in sections or not name or any(name in s for s in sections.values()):
                print("relock-fedora.py: --add %s: a new name for system_packages or image_packages" % request,
                      file=sys.stderr)
                return 2
            sections[section][name] = None
        pins = json.loads(PINS.read_text())
        bootstrap = (HERE / "bootstrap.sh").read_text()
        source_pins = pins.get("source_repositories") or {a: {"baseurl": b} for a, b in SOURCE_BASEURLS.items()}
        if options.fetch:
            fetch(pins["repositories"], metadata)
            fetch(source_pins, metadata, SOURCE_TREES)
        trees, candidates, changes, source_trees, sources = {}, {}, [], {}, {}
        for alias, short in SOURCE_TREES.items():
            repomd, primary_sha256, packages = read_tree(metadata, short, ("src",))
            source_trees[alias] = {"baseurl": source_pins[alias]["baseurl"], "repomd_sha256": hashlib.sha256(repomd).hexdigest(),
                                   "repomd_bytes": len(repomd), "primary_sha256": primary_sha256, "recorded_at": options.utc}
            for entries in packages.values():
                for entry in entries:
                    sources.setdefault(Path(entry["href"]).name, dict(entry, repo=alias))
        for alias, short in TREES.items():
            repomd, primary_sha256, packages = read_tree(metadata, short)
            trees[alias] = {"repomd_sha256": hashlib.sha256(repomd).hexdigest(), "repomd_bytes": len(repomd),
                            "primary_sha256": primary_sha256, "recorded_at": options.utc}
            for name, entries in packages.items():
                candidates.setdefault(name, []).extend(dict(e, repo=alias) for e in entries)
        key_id = lock["distribution"]["key"]["fingerprint"][-8:].lower()
        for section in (lock["distribution"]["system_packages"], lock["image_packages"]):
            for name, old in sorted(section.items()):
                found = candidates.get(name)
                if not found:
                    raise Refused("%s is %s and no longer in the metadata" % (name, "locked" if old else "to be added"))
                best = found[0]
                for entry in found[1:]:
                    if evr_cmp(entry, best) > 0:
                        best = entry
                added = old is None
                old = old or {"nevra": "(added)"}
                order = 1 if added else evr_cmp(best, old)
                if order < 0:
                    raise Refused("%s: the metadata's newest is older than the locked %s" % (name, old["nevra"]))
                if order == 0 and best["sha256"] != old["sha256"]:
                    raise Refused("%s: the same NEVRA %s with other bytes" % (name, old["nevra"]))
                epoch = "" if (best["epoch"] or "0") == "0" else best["epoch"] + ":"
                source = (best["sourcerpm"] or "").rsplit(".src.rpm", 1)[0].rsplit("-", 2)
                entry = {"nevra": "%s-%s%s-%s.%s" % (name, epoch, best["version"], best["release"], best["arch"]),
                         "epoch": best["epoch"], "version": best["version"], "release": best["release"],
                         "arch": best["arch"], "sha256": best["sha256"], "href": best["href"], "repo": best["repo"],
                         "sourcerpm": best["sourcerpm"],
                         "koji_signed": "%s/%s/%s/%s/data/signed/%s/%s/%s" % (
                             KOJI, source[0], source[1], source[2], key_id, best["arch"], Path(best["href"]).name)
                         if len(source) == 3 else None}
                source_rpm = sources.get(best["sourcerpm"])
                if source_rpm is None:
                    raise Refused("%s: its source RPM %s is in neither source tree" % (name, best["sourcerpm"]))
                source_epoch = "" if (source_rpm["epoch"] or "0") == "0" else source_rpm["epoch"] + ":"
                entry["srpm"] = {"nevra": "%s-%s%s-%s.src" % (source_rpm["name"], source_epoch, source_rpm["version"],
                                                               source_rpm["release"]),
                                 "sha256": source_rpm["sha256"], "href": source_rpm["href"], "repo": source_rpm["repo"],
                                 "koji_signed": "%s/%s/%s/%s/data/signed/%s/src/%s" % (
                                     KOJI, source_rpm["name"], source_rpm["version"], source_rpm["release"], key_id,
                                     best["sourcerpm"])}
                pinned = old.get("srpm") or {}
                if pinned.get("nevra") == entry["srpm"]["nevra"] and pinned.get("sha256") != entry["srpm"]["sha256"]:
                    raise Refused("%s: the same source RPM %s with other bytes" % (name, best["sourcerpm"]))
                if entry["nevra"] != old["nevra"]:
                    changes.append("%s: %s -> %s" % (name, old["nevra"], entry["nevra"]))
                section[name] = entry
    except Refused as issue:
        print("relock-fedora.py: refused: %s" % issue, file=sys.stderr)
        return 1
    except (OSError, ValueError, KeyError, IndexError, AttributeError, Unable) as issue:
        print("relock-fedora.py: cannot relock: %s" % issue, file=sys.stderr)
        return 2
    for alias, tree in trees.items():
        lock["distribution"]["repositories"][alias].update(tree)
        pins["repositories"][alias].update(tree)
    lock["distribution"]["source_repositories"] = json.loads(json.dumps(source_trees))
    pins["source_repositories"] = source_trees
    lock["recorded_at"] = options.utc
    text = json.dumps(lock, indent=2, sort_keys=True) + "\n"
    digest = hashlib.sha256(text.encode()).hexdigest()
    bootstrap = re.sub(r"(printf '%s  %s\\n' ')[0-9a-f]{64}(' input-lock\.json)", r"\g<1>%s\g<2>" % digest, bootstrap)
    for alias, tree in trees.items():
        bootstrap = re.sub(r"(?m)^%s [0-9a-f]{64}$" % re.escape(alias), "%s %s" % (alias, tree["repomd_sha256"]),
                           bootstrap)
    start = bootstrap.index("install --setopt=install_weak_deps=False \\\n") + len("install --setopt=install_weak_deps=False \\\n")
    end = bootstrap.index("\n# Retain only the installed inventory")
    system = lock["distribution"]["system_packages"]
    bootstrap = bootstrap[:start] + " \\\n".join("    " + system[n]["nevra"] for n in sorted(system)) + bootstrap[end:]
    output.mkdir(parents=True)
    (output / "input-lock.json").write_text(text)
    (output / "bootstrap.sh").write_text(bootstrap)
    (output / "fedora-repositories.json").write_text(json.dumps(pins, indent=2) + "\n")
    report = ["relocked at %s from %s" % (options.utc, metadata)]
    report += ["%s: repomd %s" % (alias, tree["repomd_sha256"]) for alias, tree in dict(trees, **source_trees).items()]
    report += changes or ["no locked NEVRA changed"]
    report.append("input-lock.json sha256 %s" % digest)
    (output / "RELOCK.txt").write_text("\n".join(report) + "\n")
    print("\n".join(report))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
