#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Runs after the .deb/.rpm/.apk installs. Creates a hardened, no-login system user and
# the data directory, then reloads systemd. It deliberately does NOT enable or start the
# service — running a governance control plane is an explicit operator decision.
set -eu

# Create the service group + user across glibc (useradd/groupadd) and Alpine/busybox
# (addgroup/adduser) toolchains. Idempotent: skip if they already exist.
if ! getent group olivares >/dev/null 2>&1; then
  groupadd --system olivares 2>/dev/null || addgroup -S olivares 2>/dev/null || true
fi
if ! getent passwd olivares >/dev/null 2>&1; then
  useradd --system --gid olivares --home-dir /var/lib/olivares \
          --shell /usr/sbin/nologin --comment "Olivares AI" olivares 2>/dev/null \
    || adduser -S -D -H -G olivares -h /var/lib/olivares -s /sbin/nologin olivares 2>/dev/null \
    || true
fi

mkdir -p /var/lib/olivares /etc/olivares
chown -R olivares:olivares /var/lib/olivares 2>/dev/null || true
chmod 0750 /var/lib/olivares 2>/dev/null || true

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || true
fi

cat <<'EOF'
Olivares AI installed.
  1. Review /etc/olivares/olivares.env (listeners default to loopback-only).
  2. Start it:   sudo systemctl enable --now olivares
  3. First-boot setup token is printed to the journal:  journalctl -u olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'
Docs: https://github.com/olivaresai/olivares  ·  verify a release: scripts/verify-release.sh
EOF
