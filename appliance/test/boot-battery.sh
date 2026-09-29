#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# boot-battery.sh — the acceptance oracle of the image: it boots, three ways, and reads what
# the machine says on its serial console.
#
# The oracle is one sentence: an appliance that does not reach multi-user.target and show the
# console label on the serial console has not booted. Three firmware paths answer it on amd64
# - legacy BIOS, UEFI, and UEFI with Secure Boot on - and a fourth case installs the medium
# the way an owner does and then boots what it installed.
#
#   bios             the disk under SeaBIOS: the legacy path KIWI keeps on the same disk (eficsm)
#   uefi             the disk under OVMF: shim and the signed GRUB, without enforcement
#   uefi-secureboot  the disk under OVMF built for Secure Boot with the Microsoft keys enrolled.
#                    The firmware refuses anything the Microsoft UEFI CA did not sign, so a
#                    recipe without shim-signed cannot reach the label here - that is the
#                    negative control of this case, and it is a change of the recipe, not of
#                    this script.
#   iso-install      the installer medium under the same Secure Boot firmware, unattended, onto
#                    a blank disk, and then a boot of what it wrote.
#
# It refuses rather than passing. Without /dev/kvm there is no measurement here: QEMU without
# acceleration boots minutes slower and every timeout in this file would be measuring the
# emulator instead of the image. APPLIANCE_BOOT_ACCEL=tcg says so explicitly and takes that
# cost on purpose; the default does not guess.
#
# usage: boot-battery.sh ARTIFACT_DIR EVIDENCE_DIR [CASE...]
#        boot-battery.sh --list-cases
#        boot-battery.sh --check-preflight ARTIFACT_DIR
#   exit 0  every assertion held and every negative control failed
#   exit 1  an assertion failed or a negative control passed
#   exit 2  it could not be measured: no /dev/kvm, no emulator, no firmware, no artifact.
#           An inability is never a pass.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
formats_json="$repo/appliance/images/formats/formats.json"

kvm_device=${APPLIANCE_KVM_DEVICE:-/dev/kvm}
accel=${APPLIANCE_BOOT_ACCEL:-kvm}
qemu=${APPLIANCE_QEMU:-qemu-system-x86_64}
qemu_img=${APPLIANCE_QEMU_IMG:-qemu-img}
ovmf_dir=${APPLIANCE_OVMF_DIR:-/usr/share/OVMF}
boot_deadline=${APPLIANCE_BOOT_TIMEOUT:-900}
install_deadline=${APPLIANCE_INSTALL_TIMEOUT:-1800}
guest_memory=${APPLIANCE_GUEST_MEMORY:-4096}
guest_cpus=${APPLIANCE_GUEST_CPUS:-2}

# The label the appliance layer installs in /etc/issue.d and agetty prints on every console,
# including the serial one, read from the layer's own banner and never kept here as a second
# literal (battery-selftest.sh checks that it is read). A banner that names no label leaves
# nothing to wait for: the battery cannot measure (below).
# shellcheck source=appliance/test/lib/console-label.sh
. "$here/lib/console-label.sh"
console_label=$(banner_label "$repo/appliance/layer/base/units/tty1-banner.txt") || console_label=""
readonly console_label
# What a boot case waits for: the label, the last thing it asserts. The serial getty prints it
# once the boot's jobs are dispatched, which on an appliance's first boot is after first boot's
# own unit; multi-user.target comes before that. The deadline bounds the wait, and a label that
# never comes fails at the deadline.
readonly boot_complete=$console_label
# systemd prints the target's description when it reaches it; where a unit carries none it
# prints the unit name. Both spellings of multi-user.target are the same event.
readonly boot_target='Reached target.*(Multi-User System|multi-user\.target)'
# A kernel whose firmware verified the boot chain says so: "secureboot: Secure boot enabled".
# mokutil calls the same state "SecureBoot enabled" from a shell this battery never has,
# because the appliance ships no credential to log in with.
readonly secure_boot_on='[Ss]ecure.?[Bb]oot enabled'

cases=(bios uefi uefi-secureboot iso-install)
failures=0
unmeasured=0
children=()

say() { printf '%s\n' "$*"; }
cleanup() {
  local pid
  for pid in "${children[@]:-}"; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null
  done
}
trap cleanup EXIT

unmeasurable() { say "UNMEASURED: $*"; exit 2; }
unable() { say "UNMEASURED $*"; unmeasured=$((unmeasured + 1)); }
[ -n "$console_label" ] || unmeasurable "the layer's banner (appliance/layer/base/units/tty1-banner.txt) names no console label"

check() {
  local name=$1
  shift
  if "$@"; then say "PASS $name"; else say "FAIL $name"; failures=$((failures + 1)); fi
}
control() {
  local name=$1
  shift
  if "$@" >/dev/null 2>&1; then
    say "FAIL control:$name accepted counterfeit evidence"
    failures=$((failures + 1))
  else
    say "PASS control:$name"
  fi
}

# ---- the assertions ------------------------------------------------------------------------
# Each reads a captured serial console log, so each has a counterfeit: a log with the line
# removed, or a log that never existed.
reached_target() { grep -Eq "$boot_target" "$1"; }
shows_the_label() { grep -Fq "$console_label" "$1"; }
secure_boot_was_on() { grep -Eq "$secure_boot_on" "$1"; }
no_setup_token() { ! grep -Eq 'olst_[A-Z2-7]{16,}' "$1"; }
no_panic() { ! grep -Eq 'Kernel panic|Attempted to kill init|emergency mode' "$1"; }
# The installer wrote the target disk: its second sector holds a GPT header ("EFI PART"), which
# the blank disk this battery created does not. It reads the disk, not the serial log: KIWI's
# installer prints almost nothing to the serial console, and words like "install" are in every
# boot log. qemu-img dd takes count as an absolute input offset (qemu-img.c img_dd, v8.2.2 and
# v10.0.0: size = count * bs, the copy runs from skip * bs to size), so `bs=512 count=2` copies
# LBA 0 and LBA 1 and bytes 512-519 of its output are LBA 1's; `skip=1 count=1` copies nothing.
# The caller reads a disk only after the guest that wrote it has exited. The read's exit and
# stderr, and the disk's identity before and after it, go to DISK.read.txt; a read that changed
# the disk is no evidence.
disk_identity() {
  printf 'path %s inode %s bytes %s sha256 %s' "$1" "$(stat -c %i "$1" 2>&1)" "$(stat -c %s "$1" 2>&1)" \
    "$(sha256sum "$1" 2>&1 | cut -d' ' -f1)"
}
installed_the_disk() {
  local disk=$1 record="$1.read.txt" header before after err rc=0
  header=$(mktemp -u "$evidence/gpt-header.XXXXXX")
  before=$(disk_identity "$disk")
  err=$("$qemu_img" dd -f qcow2 -O raw bs=512 count=2 if="$disk" of="$header" 2>&1) || rc=$?
  after=$(disk_identity "$disk")
  local lba1
  lba1=$(head -c 520 "$header" 2>/dev/null | tail -c 8 | tr -d '\000')
  {
    printf 'before: %s\n' "$before"
    printf 'command: %s dd -f qcow2 -O raw bs=512 count=2 if=%s of=%s\n' "$qemu_img" "$disk" "$header"
    printf 'exit: %s\n' "$rc"
    printf 'stderr: %s\n' "${err:-(empty)}"
    printf 'output bytes: %s\n' "$(stat -c %s "$header" 2>&1)"
    printf 'bytes 512-519: %s\n' "${lba1:-(none)}"
    printf 'after: %s\n' "$after"
  } > "$record"
  rm -f "$header"
  [ "$rc" -eq 0 ] && [ "$before" = "$after" ] && [ "$lba1" = "EFI PART" ]
}
# installer_finished DIR DISK — the installer guest in DIR ended on its hand-over, its process was
# waited for, and then DISK holds the partition table the installer writes.
installer_finished() {
  grep -qx 'ended: hand-over' "$1/qemu-exit.txt" 2>/dev/null && installed_the_disk "$2"
}

counterfeit_without() {
  local out
  out=$(mktemp "$evidence/counterfeit.XXXXXX")
  grep -Ev "$2" "$1" > "$out" 2>/dev/null || true
  printf '%s\n' "$out"
}
# counterfeit_blank_disk — a target disk as this battery creates it, which no installer wrote.
counterfeit_blank_disk() {
  local out
  out=$(mktemp -u "$evidence/counterfeit-disk.XXXXXX").qcow2
  "$qemu_img" create -f qcow2 "$out" 24G >/dev/null 2>&1 || true
  printf '%s\n' "$out"
}
counterfeit_with() {
  local out
  out=$(mktemp "$evidence/counterfeit.XXXXXX")
  cat "$1" > "$out" 2>/dev/null || true
  printf '%s\n' "$2" >> "$out"
  printf '%s\n' "$out"
}

# ---- the fixture ---------------------------------------------------------------------------
# The artifact of one format for the edition being booted (APPLIANCE_EDITION, the server
# edition when nothing named one); an edition the declaration does not carry refuses here.
artifact_file() {
  python3 - "$formats_json" "$1" "${APPLIANCE_EDITION:-server}" <<'PY'
import json, sys
body = "\n".join(l for l in open(sys.argv[1]).read().splitlines() if not l.strip().startswith("//"))
edition = sys.argv[3]
for artifact in json.loads(body)["artifacts"]:
    if artifact["format"] == sys.argv[2] and artifact.get("edition", "server") == edition:
        print(artifact["file"])
        break
else:
    raise SystemExit("formats.json declares no format %s for the %s edition" % (sys.argv[2], edition))
PY
}

accel_args() {
  if [ "$accel" = tcg ]; then
    printf 'accel=tcg'
  else
    printf 'accel=kvm'
  fi
}

# run_guest_until NAME DEADLINE EVENT PREDICATE ARG -- QEMU ARGS... — start one guest and wait until
# PREDICATE LOG ARG holds (the EVENT), the guest exits by itself, or the deadline passes. A guest
# still running is then stopped, and its process is always waited for, so nothing reads its disk
# while QEMU holds it. qemu-exit.txt records how it ended (EVENT, exited or deadline), the exit
# status the wait returned and the seconds waited. The log is the evidence of what the guest did;
# the exit status of a guest the battery stopped is not a verdict.
run_guest_until() {
  local name=$1 deadline=$2 event=$3 predicate=$4 arg=$5
  shift 5
  [ "$1" = "--" ] && shift
  local dir="$evidence/$name"
  mkdir -p "$dir"
  local log="$dir/serial.log"
  : > "$log"
  printf '%s\n' "$*" > "$dir/qemu.cmd"
  "$@" > "$dir/qemu.stdout" 2> "$dir/qemu.stderr" &
  local pid=$!
  children+=("$pid")
  local waited=0 ended=deadline
  while [ "$waited" -lt "$deadline" ]; do
    if "$predicate" "$log" "$arg"; then
      ended=$event
      break
    fi
    if ! kill -0 "$pid" 2>/dev/null; then
      ended=exited
      break
    fi
    sleep 2
    waited=$((waited + 2))
  done
  [ "$ended" = exited ] || kill "$pid" 2>/dev/null
  local status=0
  wait "$pid" 2>/dev/null || status=$?
  printf 'ended: %s\nexit: %s\nseconds: %s\n' "$ended" "$status" "$waited" > "$dir/qemu-exit.txt"
  printf '%s\n' "$waited" > "$dir/seconds"
  printf '%s\n' "$log"
}

# log_matches LOG PATTERN — the serial log holds a line matching PATTERN.
log_matches() { grep -Eq "$2" "$1" 2>/dev/null; }

# run_guest NAME DEADLINE MARKER -- QEMU ARGS... — run_guest_until the serial log holds MARKER.
run_guest() {
  local name=$1 deadline=$2 marker=$3
  shift 3
  run_guest_until "$name" "$deadline" marker log_matches "$marker" "$@"
}

# installer_handed_over LOG — the installer's end: KIWI's kiwi-dump-reboot-system.sh (KIWI 11.0.4,
# dracut module 59kiwi-dump-reboot) kexecs into the installed system once the dump and its check
# have passed, when the type sets none of oem-reboot, oem-shutdown and their interactive forms, as
# this recipe's does not. Nothing resets, so no firmware and no GRUB print; the installed system's
# kernel prints "Linux version" after the installer mounted its medium at /run/install. A failed
# dump or check ends in `reboot -f`, which under -no-reboot ends QEMU without that kernel.
installer_handed_over() {
  awk '/\/run\/install/ { medium = 1 } medium && /Linux version/ { found = 1 } END { exit !found }' "$1" 2>/dev/null
}

# base_args NAME [secure] — the arguments every guest of this battery shares, in QEMU_ARGS:
# no screen, one serial console captured to a file, no reboot, so a machine that decides to
# reboot ends the run instead of looping. "secure" adds what a Secure Boot firmware needs -
# system management mode, and a variable store only that mode may write - because a variable
# store the guest can rewrite is not an enrolled key, it is a suggestion.
base_args() {
  local name=$1 mode=${2:-plain} dir="$evidence/$1"
  mkdir -p "$dir"
  if [ "$mode" = secure ]; then
    QEMU_ARGS=("$qemu" -machine "q35,smm=on,$(accel_args)"
      -global driver=cfi.pflash01,property=secure,value=on
      -global ICH9-LPC.disable_s3=1)
  else
    QEMU_ARGS=("$qemu" -machine "q35,$(accel_args)")
  fi
  QEMU_ARGS+=(-m "$guest_memory" -smp "$guest_cpus"
    -display none -serial "file:$dir/serial.log" -monitor none
    -no-reboot -rtc base=utc)
}

# with_ovmf NAME CODE VARS — the firmware, and a writable copy of its variable store so the
# artifact and the packaged firmware are never modified by a boot.
with_ovmf() {
  local name=$1 code=$2 vars=$3 dir="$evidence/$1"
  cp "$vars" "$dir/vars.fd"
  QEMU_ARGS+=(-drive "if=pflash,format=raw,unit=0,readonly=on,file=$code"
    -drive "if=pflash,format=raw,unit=1,file=$dir/vars.fd")
}

# ---- the cases -------------------------------------------------------------------------------
assert_booted() {
  local name=$1 log=$2
  check "${name}_reaches_multi_user_target" reached_target "$log"
  control "${name}_reaches_multi_user_target" reached_target "$(counterfeit_without "$log" "$boot_target")"
  check "${name}_shows_the_console_label" shows_the_label "$log"
  control "${name}_shows_the_console_label" shows_the_label "$(counterfeit_without "$log" "$console_label")"
  check "${name}_boots_without_a_panic" no_panic "$log"
  control "${name}_boots_without_a_panic" no_panic \
    "$(counterfeit_with "$log" 'Kernel panic - not syncing: VFS: Unable to mount root fs')"
  check "${name}_prints_no_setup_token" no_setup_token "$log"
  control "${name}_prints_no_setup_token" no_setup_token \
    "$(counterfeit_with "$log" '  Token:    olst_ABCDEFGHIJKLMNOPQRSTUVWXYZ234567')"
}

case_bios() {
  base_args bios
  QEMU_ARGS+=(-drive "if=virtio,format=qcow2,file=$disk_artifact,snapshot=on")
  local log
  log=$(run_guest bios "$boot_deadline" "$boot_complete" -- "${QEMU_ARGS[@]}")
  assert_booted bios "$log"
}

case_uefi() {
  base_args uefi
  with_ovmf uefi "$ovmf_code" "$ovmf_vars"
  QEMU_ARGS+=(-drive "if=virtio,format=qcow2,file=$disk_artifact,snapshot=on")
  local log
  log=$(run_guest uefi "$boot_deadline" "$boot_complete" -- "${QEMU_ARGS[@]}")
  assert_booted uefi "$log"
}

case_uefi_secureboot() {
  base_args uefi-secureboot secure
  with_ovmf uefi-secureboot "$ovmf_secboot_code" "$ovmf_ms_vars"
  QEMU_ARGS+=(-drive "if=virtio,format=qcow2,file=$disk_artifact,snapshot=on")
  local log
  log=$(run_guest uefi-secureboot "$boot_deadline" "$boot_complete" -- "${QEMU_ARGS[@]}")
  assert_booted uefi-secureboot "$log"
  # The firmware enrolled the Microsoft keys and enforces them: a chain it did not verify never
  # reaches a kernel at all, so a recipe without shim-signed cannot pass the assertions above.
  # The kernel then says which state it was booted in, which is the difference between "Secure
  # Boot was on" and "Secure Boot was never asked about".
  check secure_boot_was_enforced secure_boot_was_on "$log"
  control secure_boot_was_enforced secure_boot_was_on "$(counterfeit_without "$log" "$secure_boot_on")"
}

case_iso_install() {
  local dir="$evidence/iso-install"
  mkdir -p "$dir"
  local target="$dir/installed.qcow2"
  rm -f "$target"
  "$qemu_img" create -f qcow2 "$target" 24G > "$dir/qemu-img.txt" 2>&1 ||
    { unable "iso-install: the target disk could not be created"; return; }

  # The medium installs itself: oem-unattended writes the disk image to the only disk it finds,
  # checks it, and kexecs into the installed system. The case ends on that hand-over, stops the
  # guest and waits for its process; a guest stopped at the deadline, or one that exited before
  # the hand-over, is no finished installer, whatever the disk holds.
  base_args iso-install secure
  with_ovmf iso-install "$ovmf_secboot_code" "$ovmf_ms_vars"
  QEMU_ARGS+=(-drive "if=virtio,format=qcow2,file=$target"
    -drive "if=none,id=medium,format=raw,readonly=on,file=$iso_artifact"
    -device ide-cd,drive=medium,bootindex=0)
  local log
  log=$(run_guest_until iso-install "$install_deadline" hand-over installer_handed_over - -- "${QEMU_ARGS[@]}")
  # The disk the installer wrote holds a partition table; the counterfeit is a blank disk. The
  # assertion that matters most is the next one, because a disk that boots to the label was
  # written correctly.
  check iso_medium_ran_the_installer installer_finished "$dir" "$target"
  control iso_medium_ran_the_installer installer_finished "$dir" "$(counterfeit_blank_disk)"

  # What it installed is what an owner boots, under the same firmware.
  base_args iso-install-boot secure
  with_ovmf iso-install-boot "$ovmf_secboot_code" "$ovmf_ms_vars"
  QEMU_ARGS+=(-drive "if=virtio,format=qcow2,file=$target")
  local booted
  booted=$(run_guest iso-install-boot "$boot_deadline" "$boot_complete" -- "${QEMU_ARGS[@]}")
  assert_booted iso-install-boot "$booted"
  check iso_installed_disk_is_secure_booted secure_boot_was_on "$booted"
  control iso_installed_disk_is_secure_booted secure_boot_was_on \
    "$(counterfeit_without "$booted" "$secure_boot_on")"
}

# ---- the preflight ---------------------------------------------------------------------------
preflight() {
  local artifacts=$1
  case "$accel" in kvm|tcg) ;; *) unmeasurable "unknown accelerator: $accel" ;; esac
  if [ "$accel" = kvm ]; then
    [ -r "$kvm_device" ] && [ -w "$kvm_device" ] ||
      unmeasurable "$kvm_device is not readable and writable here. This battery measures an image, not an emulator: set APPLIANCE_BOOT_ACCEL=tcg to take the slow path on purpose."
  fi
  command -v "$qemu" >/dev/null 2>&1 || [ -x "$qemu" ] ||
    unmeasurable "$qemu is not available"
  command -v "$qemu_img" >/dev/null 2>&1 || [ -x "$qemu_img" ] ||
    unmeasurable "$qemu_img is not available"
  command -v python3 >/dev/null 2>&1 || unmeasurable "python3 is not available"

  ovmf_code="$ovmf_dir/OVMF_CODE_4M.fd"
  ovmf_vars="$ovmf_dir/OVMF_VARS_4M.fd"
  ovmf_secboot_code="$ovmf_dir/OVMF_CODE_4M.secboot.fd"
  ovmf_ms_vars="$ovmf_dir/OVMF_VARS_4M.ms.fd"
  local file
  for file in "$ovmf_code" "$ovmf_vars" "$ovmf_secboot_code" "$ovmf_ms_vars"; do
    [ -r "$file" ] || unmeasurable "$file is not there: install the ovmf package for the UEFI and Secure Boot cases"
  done

  [ -d "$artifacts" ] || unmeasurable "no artifact directory at $artifacts"
  disk_artifact="$artifacts/$(artifact_file qcow2)"
  iso_artifact="$artifacts/$(artifact_file iso)"
  [ -r "$disk_artifact" ] || unmeasurable "no disk artifact at $disk_artifact"
  [ -r "$iso_artifact" ] || unmeasurable "no installer medium at $iso_artifact"
}

# ---- the command line -------------------------------------------------------------------------
if [ "${1:-}" = --list-cases ]; then
  printf '%s\n' "${cases[@]}"
  exit 0
fi
if [ "${1:-}" = --check-preflight ]; then
  evidence=${TMPDIR:-/tmp}
  preflight "${2:-}"
  say "PREFLIGHT ok: $accel, $qemu, $ovmf_dir"
  exit 0
fi

artifacts=${1:?usage: boot-battery.sh ARTIFACT_DIR EVIDENCE_DIR [CASE...]}
evidence=${2:?usage: boot-battery.sh ARTIFACT_DIR EVIDENCE_DIR [CASE...]}
shift 2
selected=("$@")
[ ${#selected[@]} -gt 0 ] || selected=("${cases[@]}")

mkdir -p "$evidence"
preflight "$artifacts"
say "boot battery: $accel acceleration, cases: ${selected[*]}"

for name in "${selected[@]}"; do
  case $name in
    bios) case_bios ;;
    uefi) case_uefi ;;
    uefi-secureboot) case_uefi_secureboot ;;
    iso-install) case_iso_install ;;
    *) unmeasurable "unknown case: $name" ;;
  esac
done

say "failed: $failures; unmeasured: $unmeasured"
[ "$failures" -gt 0 ] && exit 1
[ "$unmeasured" -gt 0 ] && exit 2
exit 0
