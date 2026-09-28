#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
set -eu
if command -v systemd-sysusers >/dev/null 2>&1; then
    systemd-sysusers /usr/lib/sysusers.d/olivares-appliance-portal.conf
fi
if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload
    systemctl enable olivares-net-guard.socket olivares-net-guard.service olivares-helper-netrestore.socket olivares-helper-netprobe.socket
    # The module helpers' sockets, for the Appliance Console's services, storage and firewall reads.
    systemctl enable olivares-helper-units.socket olivares-helper-storage.socket olivares-helper-firewall.socket
    # The firewall owner's boot load and its guard.
    systemctl enable olivares-firewall.service olivares-firewall-guard.service
fi
