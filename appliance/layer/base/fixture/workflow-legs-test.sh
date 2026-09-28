#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# workflow-legs-test.sh — the appliance-a1 workflow runs the base package's fixture for both of
# its families, each with its own package format and tools:
#   fixture         the deb: built with nfpm, inspected with dpkg-deb, staged as
#                   olivares-appliance-base.deb, installed by fixture/Containerfile (Debian 13),
#                   and the battery run on that image;
#   fixture-fedora  the rpm: built with nfpm, inspected with rpm -qip and -qlp inside the fixture's
#                   own digest-pinned Fedora 44 image, staged as olivares-appliance-base.rpm,
#                   installed by fixture/fedora/Containerfile, and the battery run on that image.
# Neither job runs the other family's package tool. Every action is pinned by a full commit SHA,
# and every evidence upload runs whatever the job did.
#
# usage: workflow-legs-test.sh [WORKFLOW]   (default: the repository's appliance-a1.yml)
#   exit 0 every check held, 1 one did not, 2 the workflow cannot be read
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
workflow=${1:-$here/../../../../.github/workflows/appliance-a1.yml}
[ -r "$workflow" ] || { echo "UNABLE: $workflow cannot be read"; exit 2; }
failures=0

# job NAME — the lines of one job under jobs:, from its key to the next job's key.
job() {
  awk -v name="$1" '
    /^jobs:/ { in_jobs = 1; next }
    in_jobs && /^  [A-Za-z0-9_-]+:[[:space:]]*$/ { current = substr($1, 1, length($1) - 1); next }
    in_jobs && /^[^[:space:]]/ { in_jobs = 0 }
    in_jobs && current == name { print }
  ' "$workflow"
}

check() {
  local name=$1
  shift
  if "$@"; then
    echo "PASS $name"
  else
    echo "FAIL $name"
    failures=$((failures + 1))
  fi
}
has() { grep -qF -- "$2" <<< "$1"; }
lacks() { ! grep -qE -- "$2" <<< "$1"; }

deb=$(job fixture)
rpm=$(job fixture-fedora)

check "the deb job exists" test -n "$deb"
check "the deb job builds the deb" has "$deb" "--packager deb --target dist/olivares-appliance-base.deb"
check "the deb job inspects it with dpkg-deb" has "$deb" "dpkg-deb --info dist/olivares-appliance-base.deb"
check "the deb job stages it" has "$deb" "cp bin/olivares dist/olivares-appliance-base.deb "
check "the deb job builds the Debian fixture" has "$deb" "--file appliance/layer/base/fixture/Containerfile --tag appliance-a1-fixture "
check "the deb job runs the battery on it" has "$deb" "firstboot-battery.sh appliance-a1-fixture "
check "the deb job runs no rpm tool" lacks "$deb" "--packager rpm|rpm -q|dnf "

check "the rpm job exists" test -n "$rpm"
check "the rpm job builds the rpm" has "$rpm" "--packager rpm --target dist/olivares-appliance-base.rpm"
check "the rpm job inspects its header" has "$rpm" "rpm -qip /dist/olivares-appliance-base.rpm"
check "the rpm job lists its files" has "$rpm" "rpm -qlp /dist/olivares-appliance-base.rpm"
check "the rpm job inspects it in the fixture's own Fedora image" has "$rpm" \
  "sed -n 's/^FROM //p' appliance/layer/base/fixture/fedora/Containerfile"
check "the rpm job stages it" has "$rpm" "cp bin/olivares dist/olivares-appliance-base.rpm "
check "the rpm job builds the Fedora fixture" has "$rpm" \
  "--file appliance/layer/base/fixture/fedora/Containerfile --tag appliance-a1-fixture-fedora "
check "the rpm job runs the battery on it" has "$rpm" "firstboot-battery.sh appliance-a1-fixture-fedora "
check "the rpm job runs no Debian tool" lacks "$rpm" "dpkg|apt-get|--packager deb"

# Every action by a full commit SHA; every upload whatever the job did.
unpinned=$(grep -E '^[[:space:]]*(- )?uses:' "$workflow" | grep -vE 'uses: [^@[:space:]]+@[0-9a-f]{40}([[:space:]]+#.*)?$' || true)
check "every action is pinned by a full commit SHA" test -z "$unpinned"
[ -z "$unpinned" ] || printf '  unpinned: %s\n' "$unpinned"
for j in fixture fixture-fedora; do
  block=$(job "$j")
  uploads=$(grep -c 'uses: actions/upload-artifact@' <<< "$block")
  always=$(grep -c 'if: always()' <<< "$block")
  check "$j uploads its evidence once, whatever the job did" test "$uploads" -eq 1 -a "$always" -eq 1
done

echo "failed: $failures"
[ "$failures" -eq 0 ]
