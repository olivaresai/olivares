#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# nocloud-probe.sh — the unattended first boot, on a real machine this time.
#
# The appliance layer's own battery already boots a container from a NoCloud seed and asserts
# what first boot recorded. This boots the IMAGE from the SAME seed, on a virtual machine with
# firmware, a disk and cloud-init doing what it does on a hypervisor, and then asserts the same
# record with the SAME assertions - loaded from the delivered battery, not copied
# (appliance/test/lib/firstboot-predicates.sh).
#
# How the evidence leaves a machine nobody can log into. The appliance ships no credential by
# design, so there is no shell in the guest to read a file from. The probe passes systemd an
# extra unit through the firmware, as an SMBIOS type 11 credential
# (systemd.extra-unit.*, systemd.unit-dropin.*, documented for systemd 256 and later; Debian 13
# carries 257), and that unit prints the record, the journal and the flags to a SECOND serial
# port and powers the machine off. Nothing in the image changes: the observer arrives with the
# test, through a documented interface, and leaves with it.
#
# usage: nocloud-probe.sh ARTIFACT_DIR EVIDENCE_DIR
#        nocloud-probe.sh --check-preflight ARTIFACT_DIR
#   exit 0  every assertion held and every negative control failed
#   exit 1  an assertion failed or a negative control passed
#   exit 2  it could not be measured: no /dev/kvm, no emulator, no firmware, no artifact, or a
#           guest that never answered. An inability is never a pass.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
formats_json="$repo/appliance/images/formats/formats.json"
# The delivered carrier fixture: the seed the layer's own battery uses, byte for byte.
seed_source="$repo/appliance/answers/carriers/testdata/nocloud"

kvm_device=${APPLIANCE_KVM_DEVICE:-/dev/kvm}
accel=${APPLIANCE_BOOT_ACCEL:-kvm}
qemu=${APPLIANCE_QEMU:-qemu-system-x86_64}
ovmf_dir=${APPLIANCE_OVMF_DIR:-/usr/share/OVMF}
deadline=${APPLIANCE_FIRSTBOOT_TIMEOUT:-1200}
guest_memory=${APPLIANCE_GUEST_MEMORY:-4096}
guest_cpus=${APPLIANCE_GUEST_CPUS:-2}

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

# shellcheck source=appliance/test/lib/firstboot-predicates.sh
. "$here/lib/firstboot-predicates.sh"
# The label agetty prints on the console, read from the host layer's own banner (never a second literal here).
# shellcheck source=appliance/test/lib/console-label.sh
. "$here/lib/console-label.sh"
console_label=$(banner_label "$repo/appliance/layer/base/units/tty1-banner.txt") \
  || unmeasurable "the layer's banner (appliance/layer/base/units/tty1-banner.txt) names no console label"
readonly console_label

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

preflight() {
  local artifacts=$1
  case "$accel" in kvm|tcg) ;; *) unmeasurable "unknown accelerator: $accel" ;; esac
  if [ "$accel" = kvm ]; then
    [ -r "$kvm_device" ] && [ -w "$kvm_device" ] ||
      unmeasurable "$kvm_device is not readable and writable here. A first boot measured under emulation measures the emulator: set APPLIANCE_BOOT_ACCEL=tcg to take that cost on purpose."
  fi
  command -v "$qemu" >/dev/null 2>&1 || [ -x "$qemu" ] || unmeasurable "$qemu is not available"
  for tool in xorriso python3 jq base64; do
    command -v "$tool" >/dev/null 2>&1 || unmeasurable "$tool is not available"
  done
  ovmf_code="$ovmf_dir/OVMF_CODE_4M.fd"
  ovmf_vars="$ovmf_dir/OVMF_VARS_4M.fd"
  [ -r "$ovmf_code" ] && [ -r "$ovmf_vars" ] ||
    unmeasurable "$ovmf_dir does not carry the UEFI firmware: install the ovmf package"
  [ -r "$seed_source/user-data" ] && [ -r "$seed_source/meta-data" ] ||
    unmeasurable "the delivered NoCloud carrier fixture is not at $seed_source"
  [ -d "$artifacts" ] || unmeasurable "no artifact directory at $artifacts"
  disk_artifact="$artifacts/$(artifact_file qcow2)"
  [ -r "$disk_artifact" ] || unmeasurable "no disk artifact at $disk_artifact"
}

if [ "${1:-}" = --check-preflight ]; then
  preflight "${2:-}"
  say "PREFLIGHT ok: $accel, $qemu, $ovmf_dir, seed $seed_source"
  exit 0
fi

artifacts=${1:?usage: nocloud-probe.sh ARTIFACT_DIR EVIDENCE_DIR}
evidence=${2:?usage: nocloud-probe.sh ARTIFACT_DIR EVIDENCE_DIR}
mkdir -p "$evidence"
preflight "$artifacts"
firstboot_predicates_load || unmeasurable "the delivered first-boot battery's assertions could not be loaded"

# ---- the seed ------------------------------------------------------------------------------
# The seed cloud-init reads, from the carrier fixture the layer delivered: its cloud-config
# writes the answers document to the nocloud carrier path and sets the complete host name the
# answers declare. A seed of this script's own would be a second definition of the contract.
seed_dir="$evidence/seed"
rm -rf "$seed_dir"
mkdir -p "$seed_dir"
cp "$seed_source/meta-data" "$seed_source/user-data" "$seed_dir/"
xorriso -as mkisofs -output "$evidence/seed.iso" -volid CIDATA -joliet -rock \
  "$seed_dir/meta-data" "$seed_dir/user-data" > "$evidence/seed.iso.log" 2>&1 ||
  unmeasurable "the NoCloud seed medium could not be written"

# ---- the observer ----------------------------------------------------------------------------
# A unit the firmware hands to systemd, ordered after first boot and after the readiness unit
# that measures the product. It waits for both to stop moving, prints framed evidence on the
# second serial port and powers the machine off, so the run ends because the guest finished and
# not because a timeout fired.
probe_unit=$(cat <<'UNIT'
[Unit]
Description=Appliance boot probe of the image test harness (never part of the image)
After=olivares-appliance-firstboot.service olivares-appliance-readiness.service cloud-final.service
[Service]
Type=oneshot
ExecStart=/bin/sh -c 'for i in $(seq 1 180); do \
  state=$(systemctl show --property=ActiveState --value olivares-appliance-firstboot.service olivares-appliance-readiness.service | tr "\\n" " "); \
  case "$state" in *activating*) sleep 4 ;; *) break ;; esac; done; \
  { echo "<<<PROBE state.json"; cat /var/lib/olivares-appliance/state.json 2>/dev/null || echo "{}"; echo ">>>PROBE"; \
    echo "<<<PROBE ready"; if [ -e /var/lib/olivares-appliance/ready ]; then echo present; else echo absent; fi; echo ">>>PROBE"; \
    echo "<<<PROBE product.active"; systemctl is-active olivares.service 2>/dev/null || true; echo ">>>PROBE"; \
    echo "<<<PROBE firstboot.enabled"; systemctl is-enabled olivares-appliance-firstboot.service 2>/dev/null || true; echo ">>>PROBE"; \
    echo "<<<PROBE cloud-init.json"; cloud-init status --format json 2>/dev/null || echo "{}"; echo ">>>PROBE"; \
    echo "<<<PROBE journal.txt"; journalctl --boot --no-pager 2>/dev/null || true; echo ">>>PROBE"; \
    tok=/var/lib/olivares-appliance/setup-token; echo "<<<PROBE setup-token"; \
    if [ -r "$tok" ]; then printf 'shape '; head -c 4 "$tok"; printf '\n'; \
      printf 'mode %s owner %s\n' "$(stat -c %a "$tok")" "$(stat -c %U "$tok")"; \
    else echo absent; fi; echo ">>>PROBE"; \
    echo "<<<PROBE end"; echo ">>>PROBE"; } > /dev/ttyS1; sync; systemctl poweroff --no-block'
UNIT
)
dropin=$'[Unit]\nWants=olivares-appliance-probe.service\n'

mkdir -p "$evidence/guest"
cp "$ovmf_vars" "$evidence/guest/vars.fd"
console="$evidence/guest/console.log"
probe_log="$evidence/guest/probe.log"
: > "$console"
: > "$probe_log"

args=("$qemu" -machine "q35,$( [ "$accel" = tcg ] && printf accel=tcg || printf accel=kvm )"
  -m "$guest_memory" -smp "$guest_cpus"
  -display none -monitor none -no-reboot -rtc base=utc
  -serial "file:$console" -serial "file:$probe_log"
  -drive "if=pflash,format=raw,unit=0,readonly=on,file=$ovmf_code"
  -drive "if=pflash,format=raw,unit=1,file=$evidence/guest/vars.fd"
  -drive "if=virtio,format=qcow2,file=$disk_artifact,snapshot=on"
  -drive "if=none,id=seed,format=raw,readonly=on,file=$evidence/seed.iso"
  -device ide-cd,drive=seed
  -smbios "type=11,value=io.systemd.credential.binary:systemd.extra-unit.olivares-appliance-probe.service=$(printf '%s' "$probe_unit" | base64 -w0)"
  -smbios "type=11,value=io.systemd.credential.binary:systemd.unit-dropin.multi-user.target=$(printf '%s' "$dropin" | base64 -w0)")
printf '%s\n' "${args[*]}" > "$evidence/guest/qemu.cmd"

"${args[@]}" > "$evidence/guest/qemu.stdout" 2> "$evidence/guest/qemu.stderr" &
guest=$!
children+=("$guest")
waited=0
while [ "$waited" -lt "$deadline" ]; do
  grep -q '<<<PROBE end' "$probe_log" 2>/dev/null && break
  kill -0 "$guest" 2>/dev/null || break
  sleep 5
  waited=$((waited + 5))
done
kill "$guest" 2>/dev/null
wait "$guest" 2>/dev/null
printf '%s\n' "$waited" > "$evidence/guest/seconds"

grep -q '<<<PROBE end' "$probe_log" ||
  unmeasurable "the guest never answered on its probe console after ${waited}s: read $console"

# ---- the evidence, in the files the delivered assertions read ---------------------------------
python3 - "$probe_log" "$evidence" <<'PY'
import os, re, sys
log, out = sys.argv[1], sys.argv[2]
text = open(log, errors="replace").read()
for name, body in re.findall(r"<<<PROBE (\S+)\n(.*?)>>>PROBE", text, re.S):
    if name == "end":
        continue
    with open(os.path.join(out, name), "w") as handle:
        handle.write(body)
PY
for file in state.json ready product.active journal.txt setup-token; do
  [ -s "$evidence/$file" ] || unmeasurable "the guest sent no $file"
done

# ---- the assertions, the delivered ones --------------------------------------------------------
# The setup-token seam is closed: first boot mints the product's one-time token through its
# owner and delivers it to a root-only file on the machine, so the record reaches ready and
# the product starts. The delivery itself is asserted below on the file the guest sent: its
# SHAPE (the owner's olst_ prefix, never the bytes), its mode and its owner.
check unattended_first_boot_completes record_is "$evidence/state.json" ready ""
control unattended_first_boot_completes record_is \
  "$(counterfeit "$evidence/state.json" '.state = "refused"; .stage = "prepare-setup-delivery"')" ready ""

check unattended_first_boot_completed_every_stage_in_order completed_are "$evidence/state.json" \
  prepare-identity verify-host-settings hand-over-host-settings generate-product-config initialize-storage \
  prepare-setup-delivery verify-firewall start-services measure-readiness
control unattended_first_boot_completed_every_stage_in_order completed_are \
  "$(counterfeit "$evidence/state.json" '.completed |= reverse')" \
  prepare-identity verify-host-settings hand-over-host-settings generate-product-config initialize-storage \
  prepare-setup-delivery verify-firewall start-services measure-readiness

check the_setup_token_was_delivered_on_the_machine holds "$evidence/setup-token" "shape olst"
control the_setup_token_was_delivered_on_the_machine holds "$(counterfeit_text 'shape nope')" "shape olst"
check the_delivered_token_is_root_only holds "$evidence/setup-token" "mode 600 owner root"
control the_delivered_token_is_root_only holds "$(counterfeit_text 'mode 644 owner root')" "mode 600 owner root"

check unattended_first_boot_verified_what_cloud_init_applied reason_quotes_none "$evidence/state.json" \
  "cloud-init did not" "hostname does not"
control unattended_first_boot_verified_what_cloud_init_applied reason_quotes_none \
  "$(counterfeit "$evidence/state.json" '.reason = "cloud-init did not complete"')" "cloud-init did not"

check unattended_first_boot_printed_no_setup_token no_setup_token "$evidence/journal.txt"
control unattended_first_boot_printed_no_setup_token no_setup_token \
  "$(counterfeit_lines "$evidence/journal.txt" '  Token:    olst_ABCDEFGHIJKLMNOPQRSTUVWXYZ234567')"

check readiness_was_measured_and_marked is "$evidence/ready" present
control readiness_was_measured_and_marked is "$(counterfeit_text absent)" present

check the_console_showed_the_label holds "$console" "$console_label"
control the_console_showed_the_label holds "$(counterfeit_text 'login:')" "$console_label"

say "failed: $failures; unmeasured: $unmeasured"
[ "$failures" -gt 0 ] && exit 1
[ "$unmeasured" -gt 0 ] && exit 2
exit 0
