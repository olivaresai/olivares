#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# iso.sh — the installer medium, checked and named.
#
# KIWI builds it: the type declares installiso="true", so the hybrid medium carries the disk
# image and installs it unattended. This assembly does not make a boot chain - making one by
# hand outside the recipe is exactly the mistake this directory exists to prevent - it checks
# that the medium KIWI left really is bootable BOTH ways, gives it the published name, holds
# it to the ceiling and writes its manifest.
#
# What it can check here and what it cannot: that the medium declares a BIOS boot image and
# an EFI boot image, that the EFI boot image carries the boot loader the firmware looks for,
# and that its installer initrd carries KIWI's kiwi-dump and kiwi-dump-reboot dracut modules.
# The El Torito report and the EFI boot image's listing go to the log on every run: the medium
# is not uploaded, so a refusal is read from the log. Whether that loader is the shim the
# Microsoft UEFI CA signed is not a property of the file that a checksum can show - it is
# shown by booting it with Secure Boot on, which the boot battery does
# (appliance/test/boot-battery.sh, case uefi-secureboot).
#
# usage: iso.sh [--target-dir DIR] [--output-dir DIR]
set -euo pipefail
assembly=iso.sh
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

require_tool xorriso sha256sum stat python3
mkdir -p "$output_dir"

medium=$(find_install_iso "$target_dir")
name=$(formats_query iso file)
artifact="$output_dir/$name"

# The two El Torito boot images of a hybrid medium: one the legacy BIOS loads, one the EFI
# firmware loads. A medium with only one of them boots on half the machines the recipe claims.
report="$output_dir/.el-torito.txt"
xorriso -indev "$medium" -report_el_torito plain > "$report" 2>&1 ||
  unmeasurable "xorriso could not read $medium"
printf '%s: the El Torito report of %s:\n' "$assembly" "$(basename "$medium")"
head -n 40 "$report" | sed 's/^/  /'
grep -qi 'BIOS' "$report" || fail "the medium declares no legacy BIOS boot image: $(cat "$report")"
grep -qi 'UEFI\|EFI' "$report" || fail "the medium declares no EFI boot image: $(cat "$report")"

# The loader the EFI firmware looks for by name on a removable medium, \EFI\BOOT\BOOTX64.EFI,
# looked up where the firmware looks for it: in the El Torito EFI boot image, a FAT file system
# whose names match in any case (UEFI 2.11, 3.5.1.1, 13.3.1.2 and 13.3.2.1). KIWI writes it as
# bootx64.efi; the medium's own tree, which the firmware does not read, keeps that exact name.
images="$output_dir/.boot-images"
rm -rf "$images"
xorriso -osirrox on -indev "$medium" -extract_boot_images "$images" >/dev/null 2>&1 ||
  unmeasurable "xorriso could not extract the boot images of $medium"
efi_images=()
while IFS= read -r file; do efi_images+=("$file"); done < <(find "$images" -maxdepth 1 -name 'eltorito_img*_uefi.img' | sort)
[ ${#efi_images[@]} -eq 1 ] ||
  unmeasurable "expected one El Torito EFI boot image in $medium, xorriso extracted ${#efi_images[@]}"
loader="$output_dir/.bootx64.efi"
rm -f "$loader"
status=0
listing=$(python3 "$here/efi_image.py" "${efi_images[0]}" --require /EFI/BOOT/BOOTX64.EFI --extract "$loader") || status=$?
rm -rf "$images"
printf '%s: the El Torito EFI boot image %s holds:\n' "$assembly" "$(basename "${efi_images[0]}")"
printf '%s\n' "$listing" | sed 's/^/  /'
case $status in
  0) ;;
  1) fail "the El Torito EFI boot image carries no /EFI/BOOT/BOOTX64.EFI in any case, or an empty one, so no EFI firmware will boot it" ;;
  *) unmeasurable "the El Torito EFI boot image could not be read (efi_image.py exit $status)" ;;
esac
loader_sha=$(sha256sum "$loader" | cut -d' ' -f1)
rm -f "$loader"

# The installer's own initrd. KIWI adds kiwi-dump and kiwi-dump-reboot to it only when dracut in
# the image root lists them; otherwise it logs a warning and the medium boots but cannot install.
# The module list dracut wrote into the initrd says what it carries.
initrd="$output_dir/.install-initrd"
rm -f "$initrd"
xorriso -osirrox on -indev "$medium" -extract /boot/x86_64/loader/initrd "$initrd" >/dev/null 2>&1 ||
  fail "the medium carries no installer initrd at /boot/x86_64/loader/initrd"
status=0
modules=$(python3 "$here/initrd_modules.py" "$initrd" --require kiwi-dump --require kiwi-dump-reboot) || status=$?
rm -f "$initrd"
case $status in
  0) printf '%s: the installer initrd carries kiwi-dump kiwi-dump-reboot (%s dracut modules)\n' "$assembly" \
       "$(printf '%s\n' "$modules" | grep -c .)" ;;
  1) fail "the installer initrd lacks KIWI's install modules, so the medium cannot install: $(printf '%s' "$modules" | tr '\n' ' ')" ;;
  *) unmeasurable "the installer initrd could not be read (initrd_modules.py exit $status)" ;;
esac

deliver "$medium" "$artifact"
enforce_release_ceiling "$artifact" iso
write_manifest "$artifact" iso "$medium" \
  "  \"boot\": {\"legacy_bios\": true, \"efi\": true, \"efi_loader_sha256\": \"$loader_sha\", \"secure_boot_proven_by\": \"appliance/test/boot-battery.sh case uefi-secureboot\"}"
