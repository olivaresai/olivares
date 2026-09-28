#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""efi_image.py — a file of an El Torito EFI boot image, looked up the way UEFI firmware looks it up.

UEFI firmware boots a CD or DVD from its El Torito boot image of platform 0xEF, which it reads as an
EFI system partition: a FAT12, FAT16 or FAT32 file system (UEFI 2.11, 13.3.2.1 and 13.3.1). On
removable media it loads \\EFI\\BOOT\\BOOTX64.EFI on x64 (3.5.1.1), and FAT names match in any case
(13.3.1.2): an 8.3 name is stored in upper case, with flags when its base or its extension is shown
in lower case, and a long name is stored as it was written. KIWI writes the loader as bootx64.efi.
This reads the image itself, so a medium is judged where the firmware reads it, not in its ISO 9660
tree, whose Rock Ridge names are exact.

usage: efi_image.py IMAGE --require PATH [--extract FILE]
  prints every directory and file under /EFI, with its size, under the name the image stores
exit 0  PATH is a file of the image and is not empty (and was copied to FILE)
exit 1  PATH is not in the image, is a directory, or is empty
exit 2  it could not read the image: no FAT boot sector, an image shorter than its boot sector
        declares, a cluster outside the file system, a loop or a free cluster in a chain, or a file
        longer than its chain. An inability is never a pass.
"""
import argparse
from pathlib import Path
import struct
import sys

LISTED = 200  # the listing is for a log: its first lines are enough to diagnose a medium


class Unable(Exception):
    pass


class Fat:
    """The FAT file system of IMAGE bytes, as the firmware's FAT driver reads it."""

    def __init__(self, data):
        self.data = data
        if len(data) < 512 or data[510:512] != b"\x55\xaa":
            raise Unable("no FAT boot sector: no 0x55AA signature at byte 510")
        sector, per_cluster, reserved, fats, root_entries, total16, _, fat16 = struct.unpack_from("<HBHBHHBH", data, 11)
        total32, fat32 = struct.unpack_from("<II", data, 32)
        root_cluster = struct.unpack_from("<I", data, 44)[0]
        fat_sectors, total = fat16 or fat32, total16 or total32
        if sector not in (512, 1024, 2048, 4096) or per_cluster not in (1, 2, 4, 8, 16, 32, 64, 128) or \
                not reserved or not fats or not fat_sectors or not total:
            raise Unable("no FAT boot sector: %d bytes per sector, %d sectors per cluster, %d FATs of %d sectors"
                         % (sector, per_cluster, fats, fat_sectors))
        if total * sector > len(data):
            raise Unable("the image is cut short: %d bytes of the %d its boot sector declares"
                         % (len(data), total * sector))
        root_sectors = -(-root_entries * 32 // sector)
        first_data = reserved + fats * fat_sectors + root_sectors
        self.clusters = (total - first_data) // per_cluster
        if self.clusters < 1:
            raise Unable("no FAT boot sector: %d sectors hold no data cluster" % total)
        # The FAT variant is set by the cluster count alone (Microsoft's FAT specification, which UEFI adopts).
        self.kind = 12 if self.clusters < 4085 else 16 if self.clusters < 65525 else 32
        self.cluster_bytes = sector * per_cluster
        self.data_start = first_data * sector
        self.fat = self.span(reserved * sector, fat_sectors * sector, "the FAT")
        self.end, self.bad = {12: (0xFF8, 0xFF7), 16: (0xFFF8, 0xFFF7), 32: (0x0FFFFFF8, 0x0FFFFFF7)}[self.kind]
        if self.kind == 32:
            self.root = self.chain_bytes(root_cluster)
        else:
            self.root = self.span((reserved + fats * fat_sectors) * sector, root_sectors * sector, "the root directory")

    def span(self, start, length, what):
        if start + length > len(self.data):
            raise Unable("%s ends at byte %d, past the end of the image (%d bytes)"
                         % (what, start + length, len(self.data)))
        return self.data[start:start + length]

    def next_cluster(self, number):
        if self.kind == 12:
            pair = int.from_bytes(self.fat[number * 3 // 2:number * 3 // 2 + 2], "little")
            return pair >> 4 if number % 2 else pair & 0xFFF
        width = self.kind // 8
        value = int.from_bytes(self.fat[number * width:number * width + width], "little")
        return value & 0x0FFFFFFF if self.kind == 32 else value

    def chain_bytes(self, first):
        out, seen, number = [], set(), first
        while True:
            if not 2 <= number < self.clusters + 2:
                raise Unable("cluster %d is outside the file system (clusters 2 to %d)" % (number, self.clusters + 1))
            if number in seen:
                raise Unable("the cluster chain from %d loops at %d" % (first, number))
            seen.add(number)
            out.append(self.span(self.data_start + (number - 2) * self.cluster_bytes, self.cluster_bytes,
                                 "cluster %d" % number))
            following = self.next_cluster(number)
            if following >= self.end:
                return b"".join(out)
            if following < 2 or following == self.bad:
                raise Unable("the cluster chain from %d reaches a free or bad cluster after %d" % (first, number))
            number = following

    def entries(self, raw):
        """(name, 8.3 name, is directory, first cluster, size) of each entry of a directory's bytes."""
        out, pieces = [], []
        for pos in range(0, len(raw) - 31, 32):
            entry = raw[pos:pos + 32]
            if entry[0] == 0:
                break
            if entry[0] == 0xE5:
                pieces = []
                continue
            if entry[11] & 0x3F == 0x0F:
                pieces.append(entry)
                continue
            if entry[11] & 0x08:
                pieces = []
                continue
            base = (b"\xe5" + entry[1:8] if entry[0] == 0x05 else entry[:8]).decode("latin-1").rstrip()
            ext = entry[8:11].decode("latin-1").rstrip()
            long_name, pieces = self.long_name(pieces, entry[:11]), []
            if base in (".", ".."):
                continue
            shown = (base.lower() if entry[12] & 0x08 else base) + \
                ("." + (ext.lower() if entry[12] & 0x10 else ext) if ext else "")
            high = struct.unpack_from("<H", entry, 20)[0] if self.kind == 32 else 0
            cluster = high << 16 | struct.unpack_from("<H", entry, 26)[0]
            out.append((long_name or shown, base + ("." + ext if ext else ""), bool(entry[11] & 0x10), cluster,
                        struct.unpack_from("<I", entry, 28)[0]))
        return out

    @staticmethod
    def long_name(pieces, short):
        """The long name the pieces before an 8.3 entry spell, or None when they do not belong to it."""
        checksum = 0
        for byte in short:
            checksum = (((checksum & 1) << 7) + (checksum >> 1) + byte) & 0xFF
        count = len(pieces)
        if not count or pieces[0][0] != (0x40 | count) or \
                any(p[0] & 0x1F != count - i or p[13] != checksum for i, p in enumerate(pieces)):
            return None
        units = b"".join(p[1:11] + p[14:26] + p[28:32] for p in reversed(pieces)).decode("utf-16-le", "replace")
        return units.split("\0", 1)[0].rstrip("￿") or None

    def children(self, cluster, is_root=False):
        return self.entries(self.root if is_root else self.chain_bytes(cluster))

    def content(self, cluster, size):
        if size == 0:
            return b""
        data = self.chain_bytes(cluster)
        if len(data) < size:
            raise Unable("a file of %d bytes has a cluster chain of %d bytes" % (size, len(data)))
        return data[:size]

    def lookup(self, path):
        """The entry at PATH, each part matched in any case against the long and the 8.3 name, or None."""
        found, listing = None, self.children(0, is_root=True)
        for index, part in enumerate(p for p in path.split("/") if p):
            if index and not (found and found[2]):
                return None
            found = next((e for e in listing if part.upper() in (e[0].upper(), e[1].upper())), None)
            if found is None:
                return None
            if found[2]:
                listing = self.children(found[3])
        return found

    def walk(self, prefix, cluster, seen):
        for name, _, is_dir, first, size in sorted(self.children(cluster), key=lambda e: e[0].upper()):
            if is_dir:
                yield "%s/%s/" % (prefix, name)
                if first not in seen:
                    yield from self.walk("%s/%s" % (prefix, name), first, seen | {first})
            else:
                yield "%s/%s %d bytes" % (prefix, name, size)


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("image")
    parser.add_argument("--require", required=True)
    parser.add_argument("--extract")
    try:
        options = parser.parse_args(argv)
    except SystemExit:
        return 2
    try:
        try:
            fat = Fat(Path(options.image).read_bytes())
        except OSError as issue:
            raise Unable("cannot read %s: %s" % (options.image, issue))
        top = fat.lookup("/EFI")
        lines = ["FAT%d, %d clusters of %d bytes" % (fat.kind, fat.clusters, fat.cluster_bytes)]
        if top and top[2]:
            lines += ["/%s/" % top[0]] + list(fat.walk("/" + top[0], top[3], {top[3]}))
        else:
            lines.append("no /EFI directory")
        if len(lines) > LISTED:
            lines[LISTED:] = ["(%d more lines)" % (len(lines) - LISTED)]
        print("\n".join(lines))
        found = fat.lookup(options.require)
        body = fat.content(found[3], found[4]) if found and not found[2] else b""
    except Unable as issue:
        print("efi_image.py: %s" % issue, file=sys.stderr)
        return 2
    if found is None or found[2] or not body:
        why = ("is not in the image (names compared in any case)" if found is None else
               "is a directory" if found[2] else "is empty")
        print("efi_image.py: %s %s" % (options.require, why), file=sys.stderr)
        return 1
    if options.extract:
        Path(options.extract).write_bytes(body)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
