#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ova.sh — one file a hypervisor imports: the disk as a stream-optimized vmdk, the OVF
# envelope that describes the machine, and the manifest of both.
#
# The envelope is this directory's own (ova/envelope-template.xml) and the archive is written here,
# in the order the OVF specification asks for: the descriptor first, its manifest next, the
# disk last, so an importer can read what it is about to unpack before it unpacks a gigabyte.
# The design named ova-compose for this step; this session could not read that tool's
# documentation (its network is limited to the Debian and KIWI pages it cites), so the
# envelope is written from the format itself rather than from a schema nobody here verified.
# Swapping in ova-compose later changes this file and nothing else.
#
# What this proves and what it does not: the archive is well formed and every digest in the
# manifest is the digest of what is inside it. Whether vSphere, VirtualBox and Proxmox import
# it is the owner's laboratory row of the design (section 8, A2), stated and not run here.
#
# usage: ova.sh [--target-dir DIR] [--output-dir DIR]
set -euo pipefail
assembly=ova.sh
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=appliance/images/formats/common.sh
. "$here/common.sh"

repo=$(cd "$here/../../.." && pwd)
target_dir=${TARGET_DIR:-$repo/dist/appliance}
output_dir=${OUTPUT_DIR:-$target_dir}
cpus=${OVA_CPUS:-2}
memory_mib=${OVA_MEMORY_MIB:-4096}
# The envelope names VERSION as the build does: X.Y.Z or 0.0.0-dev, which an unset VERSION means.
version=${VERSION-0.0.0-dev}
while [ $# -gt 0 ]; do
  case $1 in
    --target-dir) target_dir=${2:?}; shift 2 ;;
    --output-dir) output_dir=${2:?}; shift 2 ;;
    *) unmeasurable "unknown option: $1" ;;
  esac
done
[[ "$version" =~ ^([0-9]+\.[0-9]+\.[0-9]+|0\.0\.0-dev)$ ]] || unmeasurable "VERSION is X.Y.Z or 0.0.0-dev, not '$version'"

require_tool qemu-img tar sha256sum stat python3
mkdir -p "$output_dir"

disk=$(find_disk "$target_dir")
name=$(formats_query ova file)
artifact="$output_dir/$name"
base=${name%.ova}
work="$output_dir/.ova-$base"
rm -rf "$work"
mkdir -p "$work"

# streamOptimized is the subformat an OVA carries: compressed, written once, read in order.
qemu-img convert -p -f raw -O vmdk -o subformat=streamOptimized,adapter_type=lsilogic \
  "$disk" "$work/$base-disk1.vmdk"

capacity=$(stat -c %s "$disk")
disk_bytes=$(stat -c %s "$work/$base-disk1.vmdk")

sed -e "s|@DISK_FILE@|$base-disk1.vmdk|g" \
    -e "s|@DISK_BYTES@|$disk_bytes|g" \
    -e "s|@DISK_CAPACITY@|$capacity|g" \
    -e "s|@SYSTEM_ID@|$base|g" \
    -e "s|@PRODUCT@|$(formats_query "" product)|g" \
    -e "s|@VENDOR@|$(formats_query "" vendor)|g" \
    -e "s|@VERSION@|$version|g" \
    -e "s|@BASE@|Based on Debian 13 (trixie)|g" \
    -e "s|@CPUS@|$cpus|g" \
    -e "s|@MEMORY_MIB@|$memory_mib|g" \
    "$here/ova/envelope-template.xml" > "$work/$base.ovf"
python3 -c 'import sys,xml.dom.minidom; xml.dom.minidom.parse(sys.argv[1])' "$work/$base.ovf" ||
  fail "the envelope this assembly wrote is not well-formed XML"

# The OVF manifest: every file of the archive except the manifest itself.
( cd "$work" && for file in "$base.ovf" "$base-disk1.vmdk"; do
    printf 'SHA256(%s)= %s\n' "$file" "$(sha256sum "$file" | cut -d' ' -f1)"
  done ) > "$work/$base.mf"

# The order inside the archive is part of the format: descriptor, manifest, then the disk.
tar --format=ustar --create --file "$artifact" --directory "$work" \
  "$base.ovf" "$base.mf" "$base-disk1.vmdk"
tar --list --file "$artifact" > "$output_dir/.ova-contents.txt"
head -n 1 "$output_dir/.ova-contents.txt" | grep -q "\.ovf$" ||
  fail "the descriptor is not the first member of the archive"

enforce_release_ceiling "$artifact" ova
write_manifest "$artifact" ova "$disk" \
  "  \"ova\": {\"descriptor\": \"$base.ovf\", \"disk\": \"$base-disk1.vmdk\", \"disk_subformat\": \"streamOptimized\", \"capacity_bytes\": $capacity, \"import_proven\": false}"
rm -rf "$work"
