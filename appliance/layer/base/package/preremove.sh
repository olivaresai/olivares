#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Runs before olivares-appliance-base is removed. It disables the first-boot unit and
# nothing else: the first-boot record in /var/lib/olivares-appliance, the product
# configuration first boot generated and the product itself stay, because they belong to
# this instance and to the product package. It leaves a marker that the package was removed
# and, when the unit was enabled, a second one, so a reinstallation restores that enablement.
# An upgrade changes nothing here.
#
# dpkg runs it as "prerm remove" on a removal and with "upgrade", "deconfigure" or
# "failed-upgrade" otherwise; rpm runs it as %preun with the number of this package's instances
# left when the step completes: 0 on an erase, 1 or more on an upgrade.
set -eu

state=/var/lib/olivares-appliance
enabled_at_removal=/var/lib/olivares-appliance/.firstboot-enabled-at-removal
removed=/var/lib/olivares-appliance/.package-removed

case "${1:-remove}" in
  remove) ;;
  0)
    rpm_warn() { printf 'olivares-appliance-base: rpm %%preun %s failed\n' "$1" >&2 || :; }
    if [ -d "$state" ]; then
      # The subshell keeps a failed special-builtin redirection from exiting dash itself.
      ( : > "$removed" ) || rpm_warn 'removal marker write'
    fi
    if command -v systemctl >/dev/null 2>&1; then
      if [ -d "$state" ] &&
        systemctl --quiet is-enabled olivares-appliance-firstboot.service 2>/dev/null; then
        ( : > "$enabled_at_removal" ) || rpm_warn 'enablement marker write'
      fi
      systemctl disable olivares-appliance-firstboot.service >/dev/null 2>&1 || rpm_warn disable
    fi
    exit 0
    ;;
  *) exit 0 ;;
esac

if [ -d "$state" ]; then
  : > "$removed"
fi
if command -v systemctl >/dev/null 2>&1; then
  if [ -d "$state" ] &&
    systemctl --quiet is-enabled olivares-appliance-firstboot.service 2>/dev/null; then
    : > "$enabled_at_removal"
  fi
  systemctl disable olivares-appliance-firstboot.service >/dev/null 2>&1 || true
fi
