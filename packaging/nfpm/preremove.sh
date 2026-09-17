#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Runs before the package is removed (not on APK upgrade — that is
# apk-preupgrade.sh). Package type comes from the packaged stamp or installed
# unit, never from host systemctl presence. Preserve leaves data/config/keys
# and their identity; apk/dpkg/rpm then remove package-owned paths.
set -eu

pkg_init() {
  if [ -r /usr/lib/olivares/package-init ]; then
    read -r OLIVARES_PKG_INIT < /usr/lib/olivares/package-init || return 1
    case "$OLIVARES_PKG_INIT" in
      systemd|openrc) return 0 ;;
      *) echo "error: unknown package-init: $OLIVARES_PKG_INIT" >&2; return 1 ;;
    esac
  fi
  if [ -x /etc/init.d/olivares ] && [ ! -e /usr/lib/systemd/system/olivares.service ]; then
    OLIVARES_PKG_INIT=openrc
    return 0
  fi
  if [ -e /usr/lib/systemd/system/olivares.service ] && [ ! -e /etc/init.d/olivares ]; then
    OLIVARES_PKG_INIT=systemd
    return 0
  fi
  echo "error: cannot determine package init from installed units" >&2
  return 1
}

if ! pkg_init; then
  # Legacy package without stamp or recognizable unit: keep the old systemd
  # stop when that tool is the service manager for a systemd unit still on disk.
  if [ -e /usr/lib/systemd/system/olivares.service ] && command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now olivares >/dev/null 2>&1 || true
  fi
  exit 0
fi

printf '%s\n' "$OLIVARES_PKG_INIT" > /run/olivares.pkg-removed-init
chmod 0600 /run/olivares.pkg-removed-init

if [ "$OLIVARES_PKG_INIT" = openrc ]; then
  if command -v rc-service >/dev/null 2>&1 && [ -x /etc/init.d/olivares ]; then
    if rc-service olivares status >/dev/null 2>&1; then
      rc-service olivares stop
    fi
  fi
  if command -v rc-update >/dev/null 2>&1; then
    rc-update del olivares default >/dev/null 2>&1 || true
  fi
  if [ -x /usr/bin/olivares ] && [ -r /var/lib/olivares/install-manifest.json ]; then
    /usr/bin/olivares uninstall --plan --data-dir /var/lib/olivares >/dev/null
  fi
  exit 0
fi

if [ -x /usr/bin/olivares ] && [ -r /var/lib/olivares/install-manifest.json ]; then
  /usr/bin/olivares uninstall --preserve --data-dir /var/lib/olivares
elif command -v systemctl >/dev/null 2>&1; then
  # Legacy systemd package without the v2 ownership record: retain the old
  # safe stop, but do not invent a path list or delete anything.
  systemctl disable --now olivares >/dev/null 2>&1 || true
fi
