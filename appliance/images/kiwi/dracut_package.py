#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""dracut_package.py — the builder's KIWI dracut modules as the package KIWI requires by name.

An oem image with an installer medium needs KIWI's kiwi-dump dracut module in the image root, and
KIWI's runtime check for that case (check_dracut_module_for_oem_install_in_package_list) reads the
image's package list for dracut-kiwi-oem-dump before any image work. Debian 13 packages no KIWI
dracut module. So the stage step builds that package here, inside the admitted builder, from the
modules the builder installed from the verified KIWI source, and writes it into the local package
repository the build already indexes. The image installs it like any other package, and dpkg
then owns the files.

The package carries the whole modules directory of the builder, unchanged, under
usr/lib/dracut/modules.d. Its version is the locked KIWI version exactly, with no Debian revision
or epoch: KIWI's check after prepare (check_dracut_module_versions_compatible_to_kiwi) reads it with
dpkg-query and compares it as dotted integers.

On the Fedora builder (--format rpm) the same modules become an RPM, built with the builder's
rpmbuild from a spec written here: Fedora 44 packages dracut-kiwi-oem-dump 11.0.2 only, not the
locked version. Its version is the locked KIWI version, its release 1, and rpm then owns the
files in the image; KIWI's check reads the version with rpm -q.

usage: dracut_package.py --lock LOCK [--format deb|rpm] --root DIR --output DIR
         build the package from DIR/usr/lib/dracut/modules.d into the directory OUTPUT
       dracut_package.py --lock LOCK --indexed PACKAGES
         the repository index PACKAGES (a Packages file) lists the package at the locked version
       dracut_package.py --lock LOCK --format rpm --indexed DIR
         the createrepo_c metadata of the repository DIR lists the package at the locked version
exit 0  built, or listed
exit 1  refused: a module the installer medium needs is missing, the output exists, rpmbuild
        failed or wrote no package, or the index does not list the package (dpkg-scanpackages skips
        a package it cannot read, with a warning)
exit 2  it could not run: usage, a lock without an exact x.y.z KIWI version, or no readable
        repository metadata
"""
import argparse
import calendar
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import sys
import subprocess
import tarfile
import tempfile
import time
import xml.etree.ElementTree as ET

NAME = "dracut-kiwi-oem-dump"
MODULES = "usr/lib/dracut/modules.d"
# The modules the installer medium's initrd is built with: KIWI adds kiwi-dump and
# kiwi-dump-reboot, and both depend on kiwi-lib. The lock's required_files qualify the same three.
REQUIRED = ("55kiwi-dump/module-setup.sh", "59kiwi-dump-reboot/module-setup.sh", "59kiwi-lib/module-setup.sh")
MAINTAINER = "Olivares.AI <enterprise@olivares.ai>"


class Refused(Exception):
    pass


class Unable(Exception):
    pass


def locked(lock_path):
    """The locked KIWI version and the lock's own time, which every member of the package carries."""
    try:
        lock = json.loads(Path(lock_path).read_text())
        version = lock["toolchain"]["kiwi"]["upstream_version"]
        recorded = lock["recorded_at"]
    except (OSError, ValueError, KeyError, TypeError) as issue:
        raise Unable("the lock does not name the KIWI version and its time: %s" % issue)
    if not isinstance(version, str) or not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", version):
        raise Unable("the lock's KIWI version is not an exact x.y.z: %r" % (version,))
    try:
        mtime = calendar.timegm(time.strptime(recorded, "%Y-%m-%dT%H:%M:%SZ"))
    except (TypeError, ValueError) as issue:
        raise Unable("the lock's recorded_at is not a UTC time: %s" % issue)
    return version, mtime


def control(version, installed_kib):
    return ("Package: %s\nVersion: %s\nArchitecture: all\nMaintainer: %s\nInstalled-Size: %d\n"
            "Depends: dracut-core\nSection: admin\nPriority: optional\n"
            "Description: KIWI dracut modules for the appliance installer medium\n"
            " The kiwi-dump and kiwi-dump-reboot dracut modules, with the other KIWI dracut\n"
            " modules, copied unchanged from the verified KIWI %s source of the\n"
            " appliance builder.\n" % (NAME, version, MAINTAINER, installed_kib, version))


def tar_gz(members, mtime):
    """A gzip-compressed GNU tar of MEMBERS [(name, mode, bytes or None for a directory)], root-owned."""
    raw = io.BytesIO()
    with tarfile.open(fileobj=raw, mode="w", format=tarfile.GNU_FORMAT) as tar:
        for name, mode, data in members:
            info = tarfile.TarInfo(name)
            info.mode, info.mtime, info.uid, info.gid, info.uname, info.gname = mode, mtime, 0, 0, "root", "root"
            if data is None:
                info.type = tarfile.DIRTYPE
                tar.addfile(info)
            else:
                info.size = len(data)
                tar.addfile(info, io.BytesIO(data))
    packed = io.BytesIO()
    with gzip.GzipFile(filename="", fileobj=packed, mode="wb", compresslevel=9, mtime=0) as stream:
        stream.write(raw.getvalue())
    return packed.getvalue()


def ar(members, mtime):
    """The ar archive a .deb is: each member with dpkg's own header shape, padded to an even size."""
    out = [b"!<arch>\n"]
    for name, data in members:
        header = "%-16s%-12d%-6d%-6d%-8s%-10d`\n" % (name, mtime, 0, 0, "100644", len(data))
        out.append(header.encode("ascii"))
        out.append(data)
        if len(data) % 2:
            out.append(b"\n")
    return b"".join(out)


def build(lock_path, root, output):
    version, mtime = locked(lock_path)
    top = Path(root) / MODULES
    missing = [rel for rel in REQUIRED if not (top / rel).is_file()]
    if missing:
        raise Refused("the builder's modules lack what the installer medium needs: " + ", ".join(missing))
    target = Path(output) / ("%s_%s_all.deb" % (NAME, version))
    if target.exists():
        raise Refused("the package already exists: %s" % target)
    directories, files = ["./"], []
    parts = MODULES.split("/")
    directories += ["./" + "/".join(parts[:i]) + "/" for i in range(1, len(parts) + 1)]
    for path in sorted(top.rglob("*")):
        rel = path.relative_to(root).as_posix()
        if path.is_symlink() or not (path.is_dir() or path.is_file()):
            raise Refused("the modules directory holds something other than files and directories: " + rel)
        if path.is_dir():
            directories.append("./" + rel + "/")
        else:
            files.append((rel, 0o755 if path.stat().st_mode & 0o111 else 0o644, path.read_bytes()))
    members = [(d, 0o755, None) for d in directories] + [("./" + rel, mode, data) for rel, mode, data in files]
    members.sort(key=lambda m: m[0])
    installed_kib = sum((len(data) + 1023) // 1024 for _, _, data in files) + len(directories)
    md5sums = "".join("%s  %s\n" % (hashlib.md5(data).hexdigest(), rel) for rel, _, data in files)
    control_tar = tar_gz([("./", 0o755, None), ("./control", 0o644, control(version, installed_kib).encode()),
                          ("./md5sums", 0o644, md5sums.encode())], mtime)
    data_tar = tar_gz(members, mtime)
    package = ar([("debian-binary", b"2.0\n"), ("control.tar.gz", control_tar), ("data.tar.gz", data_tar)], mtime)
    temporary = target.with_name(target.name + ".partial")
    try:
        with open(temporary, "xb") as handle:
            handle.write(package)
    except FileExistsError:
        raise Refused("a partial package already exists: %s" % temporary)
    os.replace(temporary, target)
    print("%s: %s, %d files, %d bytes, sha256 %s" % (
        Path(sys.argv[0]).name, target, len(files), len(package), hashlib.sha256(package).hexdigest()))


def spec(version, root, files, directories):
    """The rpmbuild spec of the package: the modules copied unchanged from ROOT, each file listed with its mode. The
    build-root policy scripts are off, so no shebang or file is rewritten on the way in."""
    listed = ["%%dir /%s" % d for d in directories] + ["%%attr(0%o,root,root) /%s" % (mode, rel) for rel, mode in files]
    return """%%global debug_package %%{nil}
%%global __os_install_post %%{nil}
%%global __brp_mangle_shebangs %%{nil}
Name: %s
Version: %s
Release: 1
Summary: KIWI dracut modules for the appliance installer medium
License: GPL-3.0-or-later
Packager: %s
BuildArch: noarch
Requires: dracut

%%description
The kiwi-dump and kiwi-dump-reboot dracut modules, with the other KIWI dracut modules, copied
unchanged from the verified KIWI %s source of the appliance builder.

%%install
mkdir -p %%{buildroot}/%s
cp -a %s/%s/. %%{buildroot}/%s/

%%files
%s
""" % (NAME, version, MAINTAINER, version, MODULES, str(root).rstrip("/"), MODULES, MODULES, "\n".join(listed))


def build_rpm(lock_path, root, output):
    version, mtime = locked(lock_path)
    top = Path(root) / MODULES
    missing = [rel for rel in REQUIRED if not (top / rel).is_file()]
    if missing:
        raise Refused("the builder's modules lack what the installer medium needs: " + ", ".join(missing))
    target = Path(output) / ("%s-%s-1.noarch.rpm" % (NAME, version))
    if target.exists():
        raise Refused("the package already exists: %s" % target)
    directories, files = [], []
    for path in sorted(top.rglob("*")):
        rel = path.relative_to(root).as_posix()
        if path.is_symlink() or not (path.is_dir() or path.is_file()):
            raise Refused("the modules directory holds something other than files and directories: " + rel)
        if path.is_dir():
            directories.append(rel)
        else:
            files.append((rel, 0o755 if path.stat().st_mode & 0o111 else 0o644))
    with tempfile.TemporaryDirectory(prefix="dracut-rpm-") as topdir:
        spec_file = Path(topdir) / (NAME + ".spec")
        spec_file.write_text(spec(version, root, files, directories))
        argv = ["rpmbuild", "-bb",
                "--define", "_topdir %s" % topdir,
                "--define", "_rpmdir %s" % Path(output),
                "--define", "_build_name_fmt %%{NAME}-%%{VERSION}-%%{RELEASE}.%%{ARCH}.rpm",
                "--define", "_buildhost olivares-appliance-builder",
                "--define", "use_source_date_epoch_as_buildtime 1",
                "--define", "clamp_mtime_to_source_date_epoch 1",
                "--define", "_build_id_links none",
                str(spec_file)]
        result = subprocess.run(argv, capture_output=True, text=True, timeout=600,
                                env=dict(os.environ, SOURCE_DATE_EPOCH=str(mtime)))
    if result.returncode != 0:
        raise Refused("rpmbuild exited %d: %s" % (result.returncode, result.stderr[-2000:]))
    if not target.is_file():
        raise Refused("rpmbuild wrote no %s" % target.name)
    data = target.read_bytes()
    print("%s: %s, %d files, %d bytes, sha256 %s" % (
        Path(sys.argv[0]).name, target, len(files), len(data), hashlib.sha256(data).hexdigest()))


def indexed_rpm(lock_path, directory):
    """createrepo_c's metadata of DIRECTORY lists the package at the locked version. The stage step asks createrepo_c
    for gzip metadata, which this reads with the standard library."""
    version, _ = locked(lock_path)
    common = "{http://linux.duke.edu/metadata/common}"
    try:
        repomd = ET.parse(Path(directory) / "repodata/repomd.xml").getroot()
        href = [d.find("{http://linux.duke.edu/metadata/repo}location").get("href")
                for d in repomd.iter("{http://linux.duke.edu/metadata/repo}data") if d.get("type") == "primary"][0]
        if not href.endswith(".gz"):
            raise Unable("the primary metadata %s is not gzip; the stage step asks createrepo_c for gzip" % href)
        primary = ET.fromstring(gzip.decompress((Path(directory) / href).read_bytes()))
    except (OSError, ET.ParseError, IndexError, AttributeError) as issue:
        raise Unable("no readable repository metadata in %s: %s" % (directory, issue))
    for package in primary.iter(common + "package"):
        if package.findtext(common + "name") == NAME and package.find(common + "version").get("ver") == version:
            print("%s: %s %s is in %s as %s" % (Path(sys.argv[0]).name, NAME, version, directory,
                                                package.find(common + "location").get("href")))
            return
    raise Refused("the repository metadata does not list %s %s" % (NAME, version))


def indexed(lock_path, packages):
    version, _ = locked(lock_path)
    try:
        stanzas = Path(packages).read_text().split("\n\n")
    except OSError as issue:
        raise Unable("no repository index: %s" % issue)
    for stanza in stanzas:
        fields = dict(line.split(": ", 1) for line in stanza.splitlines() if ": " in line and not line.startswith(" "))
        if fields.get("Package") == NAME and fields.get("Version") == version:
            print("%s: %s %s is in %s as %s" % (Path(sys.argv[0]).name, NAME, version, packages, fields.get("Filename")))
            return
    raise Refused("the repository index does not list %s %s: dpkg-scanpackages skips a package it "
                  "cannot read" % (NAME, version))


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--lock", required=True)
    parser.add_argument("--root")
    parser.add_argument("--output")
    parser.add_argument("--indexed")
    parser.add_argument("--format", choices=("deb", "rpm"), default="deb")
    try:
        options = parser.parse_args(argv)
    except SystemExit:
        return 2
    try:
        if options.indexed and not (options.root or options.output):
            (indexed_rpm if options.format == "rpm" else indexed)(options.lock, options.indexed)
        elif options.root and options.output and not options.indexed:
            (build_rpm if options.format == "rpm" else build)(options.lock, options.root, options.output)
        else:
            raise Unable("give --root and --output, or --indexed")
    except Refused as issue:
        print("%s: %s" % (Path(sys.argv[0]).name, issue), file=sys.stderr)
        return 1
    except Unable as issue:
        print("%s: %s" % (Path(sys.argv[0]).name, issue), file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
