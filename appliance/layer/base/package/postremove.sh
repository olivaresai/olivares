#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Runs after olivares-appliance-base is removed or purged (dpkg) or erased or upgraded (rpm). It
# reloads systemd so the removed units are forgotten and, on dpkg's purge, drops the markers the
# removal kept for a reinstallation. rpm has no purge: after an erase the markers stay, as after
# dpkg's removal. The first-boot record stays: it belongs to this instance.
set -eu

case "${1:-}" in
  '' | *[!0-9]*) ;;
  *)
    if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
      systemctl daemon-reload >/dev/null 2>&1 ||
        { printf 'olivares-appliance-base: rpm %%postun daemon-reload failed\n' >&2 || :; }
    fi
    exit 0
    ;;
esac

if [ "${1:-}" = purge ]; then
  rm -f /var/lib/olivares-appliance/.firstboot-enabled-at-removal /var/lib/olivares-appliance/.package-removed
fi
if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || true
fi
