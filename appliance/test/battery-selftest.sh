#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# battery-selftest.sh — what the boot battery and the unattended first-boot probe promise
# BEFORE either of them boots anything.
#
# It runs on any machine: it starts no virtual machine, needs no /dev/kvm and no QEMU, and
# every assertion is about the scripts themselves. That is the point — the battery's first
# promise is that it REFUSES when it cannot measure, and a refusal is exactly what can be
# tested where there is nothing to boot. Each named assertion has a negative control: the
# same assertion fed counterfeit evidence must fail.
#
# usage: battery-selftest.sh [EVIDENCE_DIR]
#   exit 0  every assertion held and every negative control failed
#   exit 1  an assertion failed or a negative control passed
#   exit 2  an assertion could not be measured here (a missing tool). Never a pass.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
battery="$here/boot-battery.sh"
probe="$here/nocloud-probe.sh"
predicates="$here/lib/firstboot-predicates.sh"
delivered="$here/../layer/base/fixture/firstboot-battery.sh"
banner="$here/../layer/base/units/tty1-banner.txt"
evidence=${1:-${TMPDIR:-/tmp}/appliance-boot-selftest}
failures=0
unmeasured=0

mkdir -p "$evidence"

say() { printf '%s\n' "$*"; }
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
unable() { say "UNMEASURED $*"; unmeasured=$((unmeasured + 1)); }

# ---- the assertions ---------------------------------------------------------------------
parses() { bash -n "$1" 2>/dev/null; }

# refuses_naming SCRIPT WORD ENV... — the script refuses (exit 2) and its output names WORD.
refuses_naming() {
  local script=$1 word=$2
  shift 2
  local out status
  out=$(env "$@" bash "$script" --check-preflight "$evidence/artifacts" 2>&1)
  status=$?
  printf '%s\n' "$out" > "$evidence/preflight.$(basename "$script").txt"
  [ "$status" -eq 2 ] && printf '%s' "$out" | grep -Fq -- "$word"
}

# quiet_about SCRIPT WORD ENV... — the same refusal does NOT name WORD.
quiet_about() {
  local script=$1 word=$2
  shift 2
  local out status
  out=$(env "$@" bash "$script" --check-preflight "$evidence/artifacts" 2>&1)
  status=$?
  [ "$status" -eq 2 ] && ! printf '%s' "$out" | grep -Fq -- "$word"
}

lists_cases() {
  local want=$1
  [ "$(bash "$battery" --list-cases 2>/dev/null | head -3 | tr '\n' ' ')" = "$want " ]
}
names_case() { bash "$battery" --list-cases 2>/dev/null | grep -qx -- "$1"; }

# no_call_recorded FILE — the emulator spy was never run.
no_call_recorded() { [ ! -e "$1" ]; }

# refusal_ran_the_spy — the preflight refuses with an emulator spy on the command path; it
# prints the spy's log, which must not exist.
refusal_ran_the_spy() {
  local spy="$evidence/qemu-spy" log="$evidence/qemu-spy.log"
  rm -f "$log"
  printf '#!/bin/sh\necho called >> %s\n' "$log" > "$spy"
  chmod 0755 "$spy"
  env APPLIANCE_KVM_DEVICE="$evidence/no-such-kvm" APPLIANCE_QEMU="$spy" \
    bash "$battery" --check-preflight "$evidence/artifacts" >/dev/null 2>&1
  printf '%s\n' "$log"
}

holds() { grep -Fq -- "$2" "$1"; }
sources_delivered() { grep -Fq -- 'layer/base/fixture/firstboot-battery.sh' "$1"; }

defines_predicates() {
  local extracted="$evidence/extracted.sh"
  ( set -e; . "$predicates"; firstboot_predicates_extract > "$extracted" ) 2>/dev/null || return 1
  local name
  for name in check control record_is completed_are no_setup_token is holds same; do
    grep -Eq "^${name}\(\)" "$extracted" || return 1
  done
  ! grep -q 'docker' "$extracted"
}

predicate_answers_both_ways() {
  local record="$evidence/state.json"
  printf '%s\n' '{"state":"refused","stage":"prepare-setup-delivery","completed":[]}' > "$record"
  ( . "$predicates"; firstboot_predicates_load; record_is "$record" refused prepare-setup-delivery ) 2>/dev/null
}
predicate_refuses_a_counterfeit() {
  local record="$evidence/state-counterfeit.json"
  printf '%s\n' '{"state":"ready","stage":"measure-readiness","completed":[]}' > "$record"
  ( . "$predicates"; firstboot_predicates_load; record_is "$record" refused prepare-setup-delivery ) 2>/dev/null
}

# A battery reads its console label from the delivered banner through lib/console-label.sh, and keeps no literal of
# its own; the helper gives the banner's first line without agetty's host name escape.
reads_the_delivered_banner() {
  grep -Fq 'lib/console-label.sh' "$1" && grep -Fq 'appliance/layer/base/units/tty1-banner.txt' "$1" \
    && grep -Eq 'console_label=\$\(banner_label ' "$1" && ! grep -Eq "console_label=['\"][^'\"]" "$1"
}
label_is_the_banner_line() {
  local want got
  want=$(head -n 1 "$banner" | sed 's/[[:space:]]*\\n[[:space:]]*$//')
  got=$( . "$here/lib/console-label.sh"; banner_label "$1" )
  [ -n "$want" ] && [ "$got" = "$want" ]
}

counterfeit_file() {
  local out
  out=$(mktemp "$evidence/counterfeit.XXXXXX")
  printf '%s\n' "$1" > "$out"
  printf '%s\n' "$out"
}

# ---- what the battery promises ----------------------------------------------------------
check battery_parses parses "$battery"
control battery_parses parses "$(counterfeit_file 'if then fi')"

check battery_lists_the_three_firmware_cases lists_cases "bios uefi uefi-secureboot"
control battery_lists_the_three_firmware_cases lists_cases "bios uefi tcg"
check battery_names_the_installer_case names_case iso-install
control battery_names_the_installer_case names_case install-by-hand

check battery_refuses_without_kvm \
  refuses_naming "$battery" "$evidence/no-such-kvm" APPLIANCE_KVM_DEVICE="$evidence/no-such-kvm"
control battery_refuses_without_kvm \
  refuses_naming "$(counterfeit_file 'exit 2')" "$evidence/no-such-kvm" APPLIANCE_KVM_DEVICE="$evidence/no-such-kvm"

check battery_does_not_start_qemu_when_it_refuses no_call_recorded "$(refusal_ran_the_spy)"
control battery_does_not_start_qemu_when_it_refuses no_call_recorded "$(counterfeit_file called)"

check battery_under_tcg_refuses_for_the_missing_emulator_only \
  refuses_naming "$battery" "$evidence/no-such-qemu" \
  APPLIANCE_BOOT_ACCEL=tcg APPLIANCE_QEMU="$evidence/no-such-qemu" APPLIANCE_KVM_DEVICE="$evidence/no-such-kvm"
check battery_under_tcg_does_not_blame_kvm \
  quiet_about "$battery" "$evidence/no-such-kvm" \
  APPLIANCE_BOOT_ACCEL=tcg APPLIANCE_QEMU="$evidence/no-such-qemu" APPLIANCE_KVM_DEVICE="$evidence/no-such-kvm"
control battery_under_tcg_does_not_blame_kvm \
  quiet_about "$(counterfeit_file "printf '%s\\n' \"$evidence/no-such-kvm\"; exit 2")" "$evidence/no-such-kvm"

check battery_reads_the_console_label_of_the_delivered_banner reads_the_delivered_banner "$battery"
control battery_reads_the_console_label_of_the_delivered_banner reads_the_delivered_banner \
  "$(counterfeit_file "readonly console_label='Olivares AI appliance'")"
check the_console_label_is_the_delivered_banner_line label_is_the_banner_line "$banner"
control the_console_label_is_the_delivered_banner_line label_is_the_banner_line \
  "$(counterfeit_file 'Olivares AI appliance \n')"
check battery_asserts_the_boot_target holds "$battery" multi-user.target
control battery_asserts_the_boot_target holds "$(counterfeit_file 'reached the login prompt')" multi-user.target
check battery_asserts_secure_boot_was_on holds "$battery" "SecureBoot enabled"
control battery_asserts_secure_boot_was_on holds "$(counterfeit_file 'secure boot assumed')" "SecureBoot enabled"

# ---- what the unattended first-boot probe promises ---------------------------------------
check probe_parses parses "$probe"
check probe_reads_the_console_label_of_the_delivered_banner reads_the_delivered_banner "$probe"
control probe_reads_the_console_label_of_the_delivered_banner reads_the_delivered_banner \
  "$(counterfeit_file "holds \"\$console\" 'Olivares AI appliance'")"
control probe_parses parses "$(counterfeit_file 'while do done')"
check probe_refuses_without_kvm \
  refuses_naming "$probe" "$evidence/no-such-kvm" APPLIANCE_KVM_DEVICE="$evidence/no-such-kvm"
check probe_seeds_from_the_delivered_carrier_fixture holds "$probe" answers/carriers/testdata/nocloud
control probe_seeds_from_the_delivered_carrier_fixture holds "$(counterfeit_file 'a seed of its own')" answers/carriers/testdata/nocloud

check probe_reuses_the_delivered_first_boot_battery sources_delivered "$predicates"
control probe_reuses_the_delivered_first_boot_battery sources_delivered "$(counterfeit_file 'record_is() { :; }')"
if [ ! -r "$delivered" ]; then
  unable "the delivered first-boot battery is not at $delivered"
elif ! command -v jq >/dev/null 2>&1; then
  unable "jq is not available, so the reused predicates cannot be exercised here"
else
  check reused_predicates_are_the_delivered_ones defines_predicates
  check reused_predicate_reads_a_refusal predicate_answers_both_ways
  control reused_predicate_reads_a_refusal predicate_refuses_a_counterfeit
fi

say "failed: $failures; unmeasured: $unmeasured"
[ "$failures" -gt 0 ] && exit 1
[ "$unmeasured" -gt 0 ] && exit 2
exit 0
