#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""initrd_modules.py — the dracut modules an initrd carries, read from the initrd itself.

dracut writes the list of the modules it included into the initrd, at lib/dracut/modules.txt of
the initrd (dracut-ng 106 and later); with a merged /usr that is usr/lib/dracut/modules.txt. This
reads it from the same four places lsinitrd does, in lsinitrd's order.
KIWI builds the installer medium's initrd with dracut's --add kiwi-dump kiwi-dump-reboot only when
dracut in the image root lists those modules; otherwise it logs a warning and builds the initrd
without them, and the medium boots but cannot install. This reads that list, so the medium is
judged by what its initrd carries and not by the build log.

An initrd is one or more newc cpio archives: an optional uncompressed early archive (CPU
microcode), then the main archive, compressed with whatever dracut chose. gzip, xz, lzma and
bzip2 are read here; zstd, lz4 and lzo need their program on PATH.

usage: initrd_modules.py INITRD [--require MODULE]...
  prints the modules of the list, one per line, in its order
exit 0  the list was read and every required module is in it
exit 1  a required module is not in the list
exit 2  it could not read the list: no cpio archive, an unknown compression, a truncated or corrupt
        main archive, a decompressor that is not installed here, or no module list. An inability is
        never a pass.
"""
import argparse
import bz2
import lzma
from pathlib import Path
import shutil
import subprocess
import sys
import zlib

LISTS = tuple(d + "/modules.txt" for d in ("lib64/dracut", "lib/dracut", "usr/lib64/dracut", "usr/lib/dracut"))
CPIO = (b"070701", b"070702")
COMPRESSIONS = ((b"\x1f\x8b", "gzip"), (b"\xfd7zXZ\x00", "xz"), (b"\x5d\x00\x00", "lzma"), (b"BZh", "bzip2"),
                (b"\x28\xb5\x2f\xfd", "zstd"), (b"\x02\x21\x4c\x18", "lz4"), (b"\x89LZO", "lzo"))
PROGRAMS = {"zstd": ["zstd", "-dcq"], "lz4": ["lz4", "-dcq"], "lzo": ["lzop", "-dcq"]}


class Unable(Exception):
    pass


def decompress(kind, data):
    if kind in ("gzip", "xz", "lzma", "bzip2"):
        stream = {"gzip": lambda: zlib.decompressobj(31), "xz": lzma.LZMADecompressor,
                  "lzma": lzma.LZMADecompressor, "bzip2": bz2.BZ2Decompressor}[kind]()
        try:
            out = stream.decompress(data)
        except (zlib.error, lzma.LZMAError, OSError, EOFError) as issue:
            raise Unable("the %s main archive is corrupt: %s" % (kind, issue))
        # A stream that stops before its end marker is a truncated archive, whatever part of it did decompress.
        if not stream.eof:
            raise Unable("the main archive is truncated: its %s stream ends before its end marker" % kind)
        return out
    program = PROGRAMS[kind]
    if not shutil.which(program[0]):
        raise Unable("the main archive is %s-compressed and %s is not installed here" % (kind, program[0]))
    result = subprocess.run(program, input=data, capture_output=True, timeout=300)
    if result.returncode != 0 or not result.stdout:
        raise Unable("%s could not decompress the main archive (exit %d; a truncated or corrupt stream): %s" % (
            program[0], result.returncode, result.stderr.decode(errors="replace")[-300:]))
    return result.stdout


def archive(data, pos, files):
    """Read one newc archive at POS into FILES; return the position after its trailer."""
    while True:
        if data[pos:pos + 6] not in CPIO or len(data) < pos + 110:
            raise Unable("no cpio header at offset %d" % pos)
        try:
            fields = [int(data[pos + 6 + 8 * i:pos + 14 + 8 * i], 16) for i in range(13)]
        except ValueError:
            raise Unable("a malformed cpio header at offset %d" % pos)
        size, namesize = fields[6], fields[11]
        name = data[pos + 110:pos + 110 + namesize - 1].decode("utf-8", "replace")
        pos += 110 + namesize
        pos += -pos % 4
        body = data[pos:pos + size]
        pos += size
        pos += -pos % 4
        if name == "TRAILER!!!":
            return pos
        while name.startswith("./"):
            name = name[2:]
        files[name.lstrip("/")] = body


def read(data):
    """Every file of every archive of the initrd, by its path without a leading ./ or /."""
    files, pos = {}, 0
    while pos < len(data):
        while pos < len(data) and data[pos] == 0:
            pos += 1
        if pos >= len(data):
            break
        if data[pos:pos + 6] in CPIO:
            pos = archive(data, pos, files)
            continue
        kind = next((name for magic, name in COMPRESSIONS if data.startswith(magic, pos)), None)
        if kind is None:
            raise Unable("neither a cpio archive nor a known compression at offset %d" % pos)
        inner = decompress(kind, data[pos:])
        inner_files = read(inner)
        files.update(inner_files)
        break
    return files


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("initrd")
    parser.add_argument("--require", action="append", default=[])
    try:
        options = parser.parse_args(argv)
    except SystemExit:
        return 2
    try:
        try:
            data = Path(options.initrd).read_bytes()
        except OSError as issue:
            raise Unable("cannot read %s: %s" % (options.initrd, issue))
        files = read(data)
        found = next((name for name in LISTS if name in files), None)
        if found is None:
            raise Unable("the initrd holds no module list modules.txt in %s (%d files read)" % (
                ", ".join(n.rsplit("/", 1)[0] for n in LISTS), len(files)))
    except Unable as issue:
        print("initrd_modules.py: %s" % issue, file=sys.stderr)
        return 2
    modules = [line.strip() for line in files[found].decode("utf-8", "replace").splitlines() if line.strip()]
    print("\n".join(modules))
    missing = [module for module in options.require if module not in modules]
    if missing:
        print("initrd_modules.py: the initrd does not carry %s" % " ".join(missing), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
