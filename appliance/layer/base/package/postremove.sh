#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Runs after olivares-appliance-base is removed or purged. It reloads systemd so the removed
# units are forgotten and, on purge, drops the enablement marker the removal kept for a
# reinstallation. The first-boot record stays: it belongs to this instance.
set -eu

marker=/var/lib/olivares-appliance/.firstboot-enabled-at-removal

if [ "${1:-}" = purge ]; then
  rm -f "$marker"
fi
if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || true
fi
