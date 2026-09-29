#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Runs after olivares-appliance-base is configured (dpkg) or installed (rpm). On a first
# installation it enables the first-boot unit, so the next boot runs it, and it starts nothing:
# installing the layer into an image root, or onto a running host, has no immediate effect. It
# never enables the product unit; first boot does that once every prerequisite is measured. An
# upgrade keeps the operator's enablement choice, and a reinstallation after removal restores
# the one the removal found (preremove.sh leaves markers for it).
#
# The two package managers name the step differently, and both are read here:
#   dpkg  postinst configure VERSION  VERSION is the one last configured: empty on a first
#                                     installation or after a purge, set on an upgrade and on a
#                                     reinstallation after a removal; the abort-* steps change
#                                     nothing.
#   rpm   %post COUNT                 the number of this package's instances when the step
#                                     completes: 1 on an installation, 2 or more on an upgrade.
set -eu

enabled_at_removal=/var/lib/olivares-appliance/.firstboot-enabled-at-removal
removed=/var/lib/olivares-appliance/.package-removed
installed=/var/lib/olivares-appliance/.package-installed
rpm=no

case "${1:-configure}" in
  configure)
    if [ -z "${2:-}" ]; then step=install; else step=upgrade; fi
    ;;
  [1-9] | [1-9][0-9])
    rpm=yes
    if [ "$1" -eq 1 ]; then step=install; else step=upgrade; fi
    ;;
  *) exit 0 ;;
esac

# A removal's markers make this a reinstallation, whatever the arguments say: dpkg passes the
# kept configuration's version, rpm passes 1. The enablement the removal found comes back.
if [ -e "$removed" ] || [ -e "$enabled_at_removal" ]; then
  if [ -e "$enabled_at_removal" ]; then enable=yes; else enable=no; fi
elif [ "$rpm" = yes ] && [ -e "$installed" ]; then
  # With no removal marker, an earlier RPM installation is not a fresh installation.
  enable=no
elif [ "$step" = install ]; then
  enable=yes
else
  enable=no
fi

# RPM scriptlets must finish successfully even when a host operation fails. Record a first
# installation before enabling: if a later erase cannot write its removal marker, the retained
# installation marker still prevents a reinstallation from enabling a previously disabled unit.
if [ "$rpm" = yes ]; then
  rpm_warn() { printf 'olivares-appliance-base: rpm %%post %s failed\n' "$1" >&2 || :; }
  if ! ( : > "$installed" ); then
    rpm_warn 'installation marker write'
    enable=no
  fi
  if [ ! -e /etc/olivares-portal ]; then
    install -d -m 0755 -o root -g root /etc/olivares-portal || rpm_warn install
  fi
  if command -v systemctl >/dev/null 2>&1; then
    if [ -d /run/systemd/system ]; then
      systemctl daemon-reload || rpm_warn daemon-reload
    fi
    if [ "$enable" = yes ]; then
      systemctl enable olivares-appliance-firstboot.service || rpm_warn enable
    fi
  fi
  if [ -f /var/lib/olivares-appliance/state.json ]; then
    appliance-firstboot hand-over-host-settings || rpm_warn 'host-settings handoff'
  fi
  rm -f "$enabled_at_removal" "$removed" || rpm_warn 'removal marker cleanup'
  exit 0
fi

# The Appliance Console's directory, where first boot publishes the console's selection. The
# first-boot units may write it but cannot create it (ProtectSystem=strict), and their UMask=0077
# would make it unreadable to the console's account, so it is created here, root-owned and 0755.
# An existing directory is left as it is: it may already hold the console's TLS files.
if [ ! -e /etc/olivares-portal ]; then
  install -d -m 0755 -o root -g root /etc/olivares-portal
fi

if command -v systemctl >/dev/null 2>&1; then
  if [ -d /run/systemd/system ]; then
    systemctl daemon-reload
  fi
  if [ "$enable" = yes ]; then
    systemctl enable olivares-appliance-firstboot.service
  fi
fi
if [ -f /var/lib/olivares-appliance/state.json ]; then
  appliance-firstboot hand-over-host-settings || printf '%s\n' 'olivares-appliance-base: host-settings handoff refused; inspect first-boot status' >&2
fi
rm -f "$enabled_at_removal" "$removed"
