#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Runs after olivares-appliance-base is configured. On a first installation it enables the
# first-boot unit, so the next boot runs it, and it starts nothing: installing the layer into
# an image root, or onto a running host, has no immediate effect. It never enables the
# product unit; first boot does that once every prerequisite is measured. An upgrade keeps
# the operator's enablement choice, and a reinstallation after removal restores the one the
# removal found (preremove.sh leaves a marker when the unit was enabled).
set -eu

marker=/var/lib/olivares-appliance/.firstboot-enabled-at-removal

case "${1:-configure}" in
  configure) ;;
  *) exit 0 ;;
esac

if command -v systemctl >/dev/null 2>&1; then
  if [ -d /run/systemd/system ]; then
    systemctl daemon-reload
  fi
  # Debian passes the previously configured version as $2; it is empty on a first install.
  if [ -z "${2:-}" ] || [ -e "$marker" ]; then
    systemctl enable olivares-appliance-firstboot.service
  fi
fi
rm -f "$marker"
