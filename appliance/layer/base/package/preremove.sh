#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Runs before olivares-appliance-base is removed. It disables the first-boot unit and
# nothing else: the first-boot record in /var/lib/olivares-appliance, the product
# configuration first boot generated and the product itself stay, because they belong to
# this instance and to the product package. When the unit was enabled it leaves a marker,
# so a reinstallation enables it again. An upgrade changes nothing here.
set -eu

marker=/var/lib/olivares-appliance/.firstboot-enabled-at-removal

case "${1:-remove}" in
  remove) ;;
  *) exit 0 ;;
esac

if command -v systemctl >/dev/null 2>&1; then
  if [ -d /var/lib/olivares-appliance ] &&
    systemctl --quiet is-enabled olivares-appliance-firstboot.service 2>/dev/null; then
    : > "$marker"
  fi
  systemctl disable olivares-appliance-firstboot.service >/dev/null 2>&1 || true
fi
