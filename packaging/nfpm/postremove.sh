#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Runs after the package is removed. Reloads systemd only when this package
# shipped a systemd unit. It deliberately does NOT delete /var/lib/olivares or
# the `olivares` user. That directory holds the append-only audit ledger, the
# audit signing key and TLS material — silently erasing audit data on an
# uninstall would be exactly the kind of dishonest behavior this product
# refuses. To purge, use the manifest-checked command BEFORE removing the
# package:
#   sudo olivares uninstall --purge --data-dir /var/lib/olivares --yes
set -eu

# The unit and package-init stamp are already gone. preremove records the
# packaged init in /run so this step does not guess from host systemctl.
if [ -r /run/olivares.pkg-removed-init ]; then
  read -r olivares_removed_init < /run/olivares.pkg-removed-init || olivares_removed_init=
  rm -f /run/olivares.pkg-removed-init
  if [ "$olivares_removed_init" = systemd ] && command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload >/dev/null 2>&1 || true
  fi
  exit 0
fi
# Legacy packages that predate the removal stamp: reload systemd only when
# that tool exists. This path is not used to classify an APK.
if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || true
fi
