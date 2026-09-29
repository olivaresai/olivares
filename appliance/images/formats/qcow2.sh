#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# qcow2.sh — the disk a hypervisor with KVM runs directly.
#
# It is the same disk the installer medium carries, in the format QEMU, Proxmox and libvirt
# take without converting anything. Compressed, because an appliance disk is mostly empty and
# the ceiling is measured on the file that is published, not on the size it claims.
#
# usage: qcow2.sh [--target-dir DIR] [--output-dir DIR]
set -euo pipefail
assembly=qcow2.sh
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=appliance/images/formats/common.sh
. "$here/common.sh"

repo=$(cd "$here/../../.." && pwd)
target_dir=${TARGET_DIR:-$repo/dist/appliance}
output_dir=${OUTPUT_DIR:-$target_dir}
while [ $# -gt 0 ]; do
  case $1 in
    --target-dir) target_dir=${2:?}; shift 2 ;;
    --output-dir) output_dir=${2:?}; shift 2 ;;
    *) unmeasurable "unknown option: $1" ;;
  esac
done

require_tool qemu-img sha256sum stat python3
mkdir -p "$output_dir"

disk=$(find_disk "$target_dir")
name=$(formats_query qcow2 file)
artifact="$output_dir/$name"

qemu-img convert -p -f raw -O qcow2 -c -o compat=1.1 "$disk" "$artifact"
qemu-img check "$artifact" >/dev/null || fail "the converted disk does not check out"
info=$(qemu-img info --output=json "$artifact")
virtual=$(printf '%s' "$info" | python3 -c 'import json,sys; print(json.load(sys.stdin)["virtual-size"])')

enforce_release_ceiling "$artifact" qcow2
write_manifest "$artifact" qcow2 "$disk" \
  "  \"disk\": {\"virtual_size_bytes\": $virtual, \"compressed\": true, \"compat\": \"1.1\"}"
