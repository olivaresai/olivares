#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# The storage inventory's hosted leg: run as root in the privileged Debian 13 systemd container
# of the appliance-host-hosted job, with udisks2, udisks2-lvm2 and lvm2 installed and udisksd on
# the system bus, from a checkout of the candidate commit. Prepared by the author, never run
# locally. It runs the storage inventory's named tests, replaying the virtio-style blank-serial
# fixture, and the hosted test that attaches loop devices to a file in its own temporary
# directory. A skipped test, an empty selection or a missing PASS line is a failed run.
# Exit 0: every named test passed; 1: a test failed, was skipped or was not selected; 2: the leg
# could not run.
set -euo pipefail
out=${1:?new evidence directory}
head=${2:?full candidate commit}
[[ $head =~ ^[0-9a-f]{40}$ && ! -e $out ]] || exit 2
[[ $(id -u) == 0 ]] || exit 2
repo=$(cd "$(dirname "$0")/../../.." && pwd)
[[ $(git -C "$repo" rev-parse HEAD) == "$head" ]] || exit 2
for tool in go git busctl dpkg-query /usr/sbin/losetup; do command -v "$tool" > /dev/null || exit 2; done
mkdir -p "$out"
dpkg-query -W udisks2 udisks2-lvm2 lvm2 > "$out/packages.txt" 2>&1 || exit 2
busctl --system status org.freedesktop.UDisks2 > "$out/udisks2-status.txt" 2>&1 || exit 2
unit=(
  TestInventory_MarksTheSystemDiskAndEveryConsumer
  TestInventory_OffersOnlyOperationsTheHostReports
  TestIdentity_FallsBackWhenWWNSerialAndIdAreBlank
  TestIdentity_BlankIdentityPlanIsValidInThisBootOnly
  TestIdentity_LoopDeviceUsesItsBackingFile
  TestIdentity_LoopOffsetOrInodeChangedIsRefused
  TestIdentity_AmbiguousTargetRefuses
  TestInventory_LvmModuleIsEnabledBeforeLvmReads
)
hosted=(TestHosted_LoopDeviceIsNamedByItsKernelFileAndRefusedWhenReattached)
selection() { local IFS='|'; printf '^(%s)$' "$*"; }
set +e
(cd "$repo/appliance" && go test -count=1 -v -run "$(selection "${unit[@]}")" ./layer/storage/) > "$out/unit.log" 2>&1
unit_exit=$?
(cd "$repo/appliance" && go test -count=1 -v -tags appliance_host_hosted -run "$(selection "${hosted[@]}")" ./layer/storage/) \
  > "$out/hosted.log" 2>&1
hosted_exit=$?
set -e
verdict=0
[[ $unit_exit == 0 && $hosted_exit == 0 ]] || verdict=1
for log in unit.log hosted.log; do
  if grep -q -e '--- SKIP' -e 'no tests to run' "$out/$log"; then verdict=1; fi
done
for name in "${unit[@]}"; do grep -q -- "^--- PASS: $name " "$out/unit.log" || verdict=1; done
for name in "${hosted[@]}"; do grep -q -- "^--- PASS: $name " "$out/hosted.log" || verdict=1; done
printf '%s unit=%s hosted=%s verdict=%s\n' "$head" "$unit_exit" "$hosted_exit" "$verdict" > "$out/result.txt"
exit "$verdict"
