#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# FIXTURE, not a product hook: the pre-OpenRC post-install, reduced to the lines
# scripts/test-nfpm-openrc.sh reads. It records init=systemd for EVERY package format,
# APK included, and the only service manager it knows is systemctl. It is built into a
# throwaway package and inspected; it is never installed anywhere.
# Deliberately wrong — do not correct it. See README.md in this directory.
set -eu

mkdir -p /var/lib/olivares /etc/olivares
cat > /var/lib/olivares/install-manifest.json <<MANIFEST
{
  "schema": "olivares.ai/local-install/v2",
  "mode": "system",
  "init": "systemd",
  "data_dir": "/var/lib/olivares",
  "config": "/etc/olivares/olivares.env",
  "files": [
    {"path": "/usr/bin/olivares", "role": "binary", "mode": "0755", "managed": false},
    {"path": "/etc/olivares/olivares.env", "role": "config", "mode": "0640", "managed": false},
    {"path": "/usr/lib/systemd/system/olivares.service", "role": "unit", "mode": "0644", "managed": false}
  ],
  "manifest": "/var/lib/olivares/install-manifest.json"
}
MANIFEST
chmod 0640 /var/lib/olivares/install-manifest.json

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || true
fi

cat <<'NOTE'
Olivares AI installed.
  Start it:   sudo systemctl enable --now olivares
NOTE
