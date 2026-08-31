#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Runs after the package is removed: reload systemd so the dropped unit is forgotten.
#
# It deliberately does NOT delete /var/lib/olivares or the `olivares` user. That directory
# holds the append-only audit ledger, the audit signing key and TLS material — silently
# erasing audit data on an uninstall would be exactly the kind of dishonest behavior this
# product refuses. Purge by hand if you really mean to:
#   sudo rm -rf /var/lib/olivares && sudo userdel olivares && sudo groupdel olivares
set -eu

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || true
fi
